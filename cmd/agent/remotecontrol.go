package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Remote Control — RC-0 (session plumbing).
//
// This is the signed-session lifecycle for remote control: it establishes,
// tracks, and tears down a session and enforces authorization + audit, but
// carries NO media yet (screen capture + pion/webrtc transport land in RC-1+).
//
// Delivery matches set_policy / audit_query: a signed `remote_control` command
// whose JSON lives in cmd.Payload. The command-envelope Ed25519 signature IS the
// base authorization (the receive loop only dispatches signature-verified
// commands), and the dispatch policy gate already refuses `remote_control` when
// the active agent policy disables the module — so an operator's own
// locked-down profile blocks remote control before this handler ever runs.
//
// Execution context (BigFix-style): an AGENT-BASED session runs in the agent's
// SYSTEM/admin context by default — a service-desk technician must be able to do
// admin work (install software, edit the registry, admin tasks) during a session,
// so a limited user context would make remote control useless for support. SYSTEM
// is the value, not a risk to remove. The controls are the visible indicator,
// full audit, RBAC-before-consent, the policy module-gate, and the session-scoped
// maintenance token — NOT privilege reduction. (The agentless / user-context path,
// AnyDesk-style with UAC elevation when the user is not admin, is RC-phase-2.)
//
// Security posture honored here (full addendum enforced across RC-0..RC-5):
//   - #1 execution context: agent-based session = SYSTEM/admin-capable (above).
//     `elevate` is informational for agent-based (already admin) and drives the
//     UAC prompt on the deferred agentless user-context path; it is always audited.
//   - #2 visible indicator: starting a session shows a non-dismissible indicator
//     (RC-0 logs it via rcIndicator; the on-screen UI lands in RC-4/5).
//   - #3 separate authorization: a time-boxed, session-scoped maintenance token
//     is required ON TOP of command signing. RC-0 enforces its presence + expiry
//     window; cryptographic binding (HMAC over session_id+operator, issued by the
//     console's maintenance-token authority) lands in RC-3 — documented, not faked.
//   - collaboration is one-controller + viewers: `join` adds a viewer,
//     `pass_control` explicitly moves the single control role.

// rcMaintTokenMaxTTL caps how far in the future a session token may be valid, so
// a token can never authorize a long-lived standing session.
const rcMaintTokenMaxTTL = time.Hour

// rcSpec is the JSON payload of a signed remote_control/v1 command.
type rcSpec struct {
	Version    int    `json:"version"`
	Action     string `json:"action"`      // start | stop | status | join | pass_control
	SessionID  string `json:"session_id"`
	Operator   string `json:"operator"`
	Mode       string `json:"mode"`        // unattended | attended (unattended default)
	MaintToken string `json:"maint_token"` // time-boxed session authz: v1.<expiryUnix>.<nonce>
	Elevate    bool   `json:"elevate,omitempty"`
	Target     string `json:"target,omitempty"` // operator to receive control (pass_control)

	// Reserved for RC-1 media signaling — unused in RC-0.
	SDP string   `json:"sdp,omitempty"`
	ICE []string `json:"ice,omitempty"`
}

// rcParticipant is one operator attached to a session. Exactly one is the
// controller (input authority); the rest are view-only collaborators.
type rcParticipant struct {
	Operator   string
	Controller bool
	JoinedAt   time.Time
}

// rcSession is the single active remote-control session (v1 allows one at a time;
// collaboration = multiple participants within it).
type rcSession struct {
	ID              string
	Mode            string
	ExecContext     string // "system" for agent-based (admin-capable); "user" for agentless (RC-phase-2)
	Elevated        bool
	StartedAt       time.Time
	TokenExpiry     time.Time
	InteractiveUser string
	UserPresent     bool
	Participants    []*rcParticipant
}

// rcExecSystem is the agent-based session context: the agent's SYSTEM/admin
// privilege, so a technician can perform admin tasks during the session.
const rcExecSystem = "system"

// rcIndicator is the mandatory visible-indicator surface (addendum #2). RC-0
// logs; the real on-screen, non-dismissible UI with a local one-click terminate
// lands in RC-4/5. It is an interface so tests inject a fake.
type rcIndicator interface {
	Show(operator string, startedAt time.Time)
	Hide()
}

type logIndicator struct{}

func (logIndicator) Show(operator string, startedAt time.Time) {
	slog.Warn("REMOTE CONTROL ACTIVE — visible indicator",
		"operator", operator, "since", startedAt.Format(time.RFC3339))
}
func (logIndicator) Hide() { slog.Info("remote control indicator cleared") }

// rcResult is the outcome of handling one remote_control action: the terminal
// ActionStatus phase/reason plus the audit event_type/decision to record.
type rcResult struct {
	Phase    pb.ActionPhase
	Reason   string
	Event    string // audit event_type
	Decision string // audit decision
	Detail   string // optional JSON (status snapshot)
}

func rcDenied(reason string) rcResult {
	return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_FAILED, Reason: reason, Event: "rc_session_denied", Decision: "denied"}
}

// rcManager owns the single active session. now / indicator / interactiveUser are
// injectable so the state machine is unit-testable without a clock, UI, or syscalls.
type rcManager struct {
	mu              sync.Mutex
	current         *rcSession
	now             func() time.Time
	indicator       rcIndicator
	interactiveUser func() (string, bool)
}

func newRCManager() *rcManager {
	return &rcManager{
		now:             time.Now,
		indicator:       logIndicator{},
		interactiveUser: detectInteractiveUser,
	}
}

// rcMgr is the process-wide remote-control session manager.
var rcMgr = newRCManager()

// validateMaintToken enforces the session token's format + expiry window (addendum
// #3). RC-0 verifies presence, a parseable future expiry, and that the expiry is
// within rcMaintTokenMaxTTL. Cryptographic binding to session_id+operator is RC-3.
func validateMaintToken(tok string, now time.Time) (time.Time, error) {
	parts := strings.Split(strings.TrimSpace(tok), ".")
	if len(parts) != 3 || parts[0] != "v1" || strings.TrimSpace(parts[2]) == "" {
		return time.Time{}, fmt.Errorf("malformed maintenance token (want v1.<expiryUnix>.<nonce>)")
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || exp <= 0 {
		return time.Time{}, fmt.Errorf("malformed maintenance token expiry")
	}
	expiry := time.Unix(exp, 0)
	if !expiry.After(now) {
		return time.Time{}, fmt.Errorf("maintenance token expired")
	}
	if expiry.Sub(now) > rcMaintTokenMaxTTL {
		return time.Time{}, fmt.Errorf("maintenance token TTL exceeds max allowed (%s)", rcMaintTokenMaxTTL)
	}
	return expiry, nil
}

func rcNormalizeMode(mode string) string {
	m := strings.ToLower(strings.TrimSpace(mode))
	if m == "attended" {
		return "attended"
	}
	return "unattended"
}

// handle applies one action under the lock and returns the result. Audit emission
// and ActionStatus sending are done by the caller (handler) so the manager stays
// dependency-free and testable.
func (m *rcManager) handle(spec rcSpec) rcResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	switch strings.ToLower(strings.TrimSpace(spec.Action)) {
	case "start":
		return m.start(spec, now)
	case "join":
		return m.join(spec, now)
	case "pass_control":
		return m.passControl(spec)
	case "stop":
		return m.stop(spec, now)
	case "status":
		return m.status(now)
	default:
		return rcDenied("unknown remote_control action " + strconv.Quote(spec.Action))
	}
}

func (m *rcManager) start(spec rcSpec, now time.Time) rcResult {
	if strings.TrimSpace(spec.SessionID) == "" || strings.TrimSpace(spec.Operator) == "" {
		return rcDenied("session_id and operator are required to start a session")
	}
	if m.current != nil {
		if m.current.ID == spec.SessionID {
			return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: "remote_control session already active (idempotent)", Event: "rc_session_started", Decision: "idempotent", Detail: m.snapshotJSON()}
		}
		return rcDenied(fmt.Sprintf("another remote_control session (%s) is active — concurrent sessions are not allowed; use join to collaborate", m.current.ID))
	}
	expiry, err := validateMaintToken(spec.MaintToken, now)
	if err != nil {
		return rcDenied(err.Error())
	}
	user, present := m.interactiveUser()
	sess := &rcSession{
		ID: spec.SessionID, Mode: rcNormalizeMode(spec.Mode), ExecContext: rcExecSystem, Elevated: spec.Elevate,
		StartedAt: now, TokenExpiry: expiry, InteractiveUser: user, UserPresent: present,
		Participants: []*rcParticipant{{Operator: spec.Operator, Controller: true, JoinedAt: now}},
	}
	m.current = sess
	m.indicator.Show(spec.Operator, now)
	// Agent-based session = SYSTEM/admin-capable (service-desk can do admin tasks);
	// the indicator + audit + RBAC + policy-gate are the controls.
	reason := fmt.Sprintf("remote_control session started (mode=%s, controller=%s, exec_context=%s, user_present=%t)", sess.Mode, spec.Operator, sess.ExecContext, present)
	return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: reason, Event: "rc_session_started", Decision: "started", Detail: m.snapshotJSON()}
}

func (m *rcManager) join(spec rcSpec, now time.Time) rcResult {
	if m.current == nil || m.current.ID != strings.TrimSpace(spec.SessionID) {
		return rcDenied("no active remote_control session with that session_id to join")
	}
	if strings.TrimSpace(spec.Operator) == "" {
		return rcDenied("operator is required to join a session")
	}
	if _, err := validateMaintToken(spec.MaintToken, now); err != nil {
		return rcDenied(err.Error())
	}
	for _, p := range m.current.Participants {
		if strings.EqualFold(p.Operator, spec.Operator) {
			return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: "operator already in session (idempotent)", Event: "rc_session_joined", Decision: "idempotent", Detail: m.snapshotJSON()}
		}
	}
	// One-controller model: a joiner is always a view-only collaborator.
	m.current.Participants = append(m.current.Participants, &rcParticipant{Operator: spec.Operator, Controller: false, JoinedAt: now})
	return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: fmt.Sprintf("operator %s joined as viewer (collaboration)", spec.Operator), Event: "rc_session_joined", Decision: "joined", Detail: m.snapshotJSON()}
}

func (m *rcManager) passControl(spec rcSpec) rcResult {
	if m.current == nil || m.current.ID != strings.TrimSpace(spec.SessionID) {
		return rcDenied("no active remote_control session with that session_id")
	}
	target := strings.TrimSpace(spec.Target)
	if target == "" {
		return rcDenied("target operator is required for pass_control")
	}
	var found *rcParticipant
	for _, p := range m.current.Participants {
		if strings.EqualFold(p.Operator, target) {
			found = p
			break
		}
	}
	if found == nil {
		return rcDenied("target operator is not a participant in this session")
	}
	for _, p := range m.current.Participants {
		p.Controller = false
	}
	found.Controller = true
	return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: "control passed to " + target, Event: "rc_control_passed", Decision: "passed", Detail: m.snapshotJSON()}
}

func (m *rcManager) stop(spec rcSpec, now time.Time) rcResult {
	if m.current == nil {
		return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, Reason: "no active remote_control session to stop", Event: "rc_session_ended", Decision: "noop"}
	}
	if m.current.ID != strings.TrimSpace(spec.SessionID) {
		return rcDenied("session_id does not match the active session")
	}
	dur := now.Sub(m.current.StartedAt)
	m.indicator.Hide()
	m.current = nil
	return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: fmt.Sprintf("remote_control session ended (duration=%s)", dur.Round(time.Second)), Event: "rc_session_ended", Decision: "ended"}
}

func (m *rcManager) status(now time.Time) rcResult {
	if m.current == nil {
		return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: "no active remote_control session", Event: "rc_session_status", Decision: "idle", Detail: `{"active":false}`}
	}
	// Fail-closed: an active session whose token has expired is torn down.
	if !m.current.TokenExpiry.After(now) {
		id := m.current.ID
		m.indicator.Hide()
		m.current = nil
		return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: "active session token expired — session closed (fail-closed)", Event: "rc_session_expired", Decision: "expired", Detail: fmt.Sprintf(`{"active":false,"expired_session":%q}`, id)}
	}
	return rcResult{Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, Reason: "remote_control session active", Event: "rc_session_status", Decision: "active", Detail: m.snapshotJSON()}
}

// rcSnapshot is the JSON status returned in ActionStatus.Detail.
type rcSnapshot struct {
	Active          bool               `json:"active"`
	SessionID       string             `json:"session_id,omitempty"`
	Mode            string             `json:"mode,omitempty"`
	ExecContext     string             `json:"exec_context,omitempty"`
	Elevated        bool               `json:"elevated,omitempty"`
	Controller      string             `json:"controller,omitempty"`
	Participants    []rcPartSnapshot   `json:"participants,omitempty"`
	StartedAt       int64              `json:"started_at,omitempty"`
	TokenExpiry     int64              `json:"token_expiry,omitempty"`
	InteractiveUser string             `json:"interactive_user,omitempty"`
	UserPresent     bool               `json:"user_present,omitempty"`
}

type rcPartSnapshot struct {
	Operator   string `json:"operator"`
	Controller bool   `json:"controller"`
	JoinedAt   int64  `json:"joined_at"`
}

// snapshotJSON must be called with m.mu held.
func (m *rcManager) snapshotJSON() string {
	s := m.current
	if s == nil {
		return `{"active":false}`
	}
	snap := rcSnapshot{
		Active: true, SessionID: s.ID, Mode: s.Mode, ExecContext: s.ExecContext, Elevated: s.Elevated,
		StartedAt: s.StartedAt.Unix(), TokenExpiry: s.TokenExpiry.Unix(),
		InteractiveUser: s.InteractiveUser, UserPresent: s.UserPresent,
	}
	for _, p := range s.Participants {
		if p.Controller {
			snap.Controller = p.Operator
		}
		snap.Participants = append(snap.Participants, rcPartSnapshot{Operator: p.Operator, Controller: p.Controller, JoinedAt: p.JoinedAt.Unix()})
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return `{"active":true}`
	}
	return string(b)
}

// handleRemoteControlCmd is the dispatcher entry point for a signed
// remote_control command. The policy module gate in dispatch() has already run.
func (d *dispatcher) handleRemoteControlCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec rcSpec
	if err := json.Unmarshal(cmd.Payload, &spec); err != nil || spec.Version != 1 {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionRemoteControl, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "invalid remote_control/v1 payload"))
		return
	}

	res := rcMgr.handle(spec)

	// An elevation request is ALWAYS audited (explicit + audited). For an
	// agent-based session the context is already SYSTEM/admin-capable, so the flag
	// is informational here; it drives the UAC prompt on the deferred agentless
	// user-context path.
	if spec.Elevate && strings.EqualFold(strings.TrimSpace(spec.Action), "start") {
		recordAuditEvent(ctx, d.outbox, auditlog.Event{
			DeviceID: d.cfg.DeviceID, EventType: "rc_elevation_requested", Decision: "requested",
			CommandID: cmd.CommandId, CommandType: cmd.Type, SignatureOK: true,
			Actor: attributionOrUnknown(cmd.Actor), Source: attributionOrUnknown(cmd.Source), IssuedAt: cmd.IssuedAt,
			Reason: "elevated privileges flagged for remote_control session " + spec.SessionID + " — agent-based sessions already run in SYSTEM/admin context; flag drives UAC on the agentless user-context path (RC-phase-2)",
		})
	}

	recordAuditEvent(ctx, d.outbox, auditlog.Event{
		DeviceID: d.cfg.DeviceID, EventType: res.Event, Decision: res.Decision,
		CommandID: cmd.CommandId, CommandType: cmd.Type, SignatureOK: true,
		Actor: attributionOrUnknown(cmd.Actor), Source: attributionOrUnknown(cmd.Source), IssuedAt: cmd.IssuedAt,
		Reason: res.Reason,
	})

	st := simpleActionStatus(cmd.CommandId, actionRemoteControl, d.cfg.DeviceID, "", res.Phase, res.Reason)
	if res.Detail != "" {
		st.Detail = res.Detail
	}
	sendStatus(d.outbox, st)
	slog.Info("remote_control", "command_id", cmd.CommandId, "action", spec.Action, "session", spec.SessionID, "decision", res.Decision)
}
