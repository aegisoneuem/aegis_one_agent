package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// Short unprotect PASSCODE (ManageEngine-style 8-10 char code) as a usable
// alternative to the long signed token. The server sets it via a SIGNED
// set_unprotect_secret command; the agent stores ONLY a slow PBKDF2 hash (never
// the passcode). stop/uninstall verify the typed passcode against the hash, with
// rate-limiting so a short code can't be brute-forced online.
//
// "Server decides static vs rotating": mode is carried for future "totp"
// (rotating) support; "static" is implemented now.
//
// Honest bound: a short passcode is a NUISANCE BARRIER (same class as
// ManageEngine's uninstall password), not crypto-grade. A local admin can read the
// hash and brute-force it offline (mitigated by the slow KDF) or set their own
// (mitigated only by the ACL + server re-assertion). The long Ed25519 token stays
// available for high-security deployments that want crypto-grade authorization.

const (
	passcodeIterations = 200_000 // PBKDF2 rounds — ~offline-brute-force cost per guess
	passcodeKeyLen     = 32
	passcodeSaltLen    = 16
)

type storedPasscode struct {
	Mode     string `json:"mode"` // "static" (future: "totp")
	Salt     []byte `json:"salt"`
	Iter     int    `json:"iter"`
	Hash     []byte `json:"hash"` // pbkdf2(passcode, salt, iter)
	IssuedAt int64  `json:"issued_at"`
}

var (
	passcodeMu    sync.RWMutex
	passcodeState *storedPasscode
)

func passcodeStorePath(secureDir string) string { return filepath.Join(secureDir, "unprotect.secret") }

// passcodeStoreExists reports whether an unprotect passcode has been set. Setting a
// passcode implies the service should refuse `sc stop` (you now have a passcode to
// stop WITH), so the service self-protects whenever this is true — even if the
// master AEGIS_TAMPER_PROTECT switch is off.
func passcodeStoreExists(secureDir string) bool {
	_, err := os.Stat(passcodeStorePath(secureDir))
	return err == nil
}

// pbkdf2SHA256 is a stdlib-only PBKDF2-HMAC-SHA256 (avoids an x/crypto dependency).
func pbkdf2SHA256(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLen + hashLen - 1) / hashLen
	var dk []byte
	buf := make([]byte, 4)
	for block := 1; block <= numBlocks; block++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(block))
		prf.Write(buf)
		u := prf.Sum(nil)
		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iter; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for i := range t {
				t[i] ^= u[i]
			}
		}
		dk = append(dk, t...)
	}
	return dk[:keyLen]
}

func passcodeConfigured() bool {
	passcodeMu.RLock()
	defer passcodeMu.RUnlock()
	return passcodeState != nil
}

func currentPasscodeIssuedAt() int64 {
	passcodeMu.RLock()
	defer passcodeMu.RUnlock()
	if passcodeState == nil {
		return 0
	}
	return passcodeState.IssuedAt
}

// verifyPasscode constant-time-compares the input against the stored hash.
func verifyPasscode(input string) bool {
	passcodeMu.RLock()
	s := passcodeState
	passcodeMu.RUnlock()
	if s == nil || len(s.Hash) == 0 {
		return false
	}
	h := pbkdf2SHA256([]byte(input), s.Salt, s.Iter, len(s.Hash))
	return hmac.Equal(h, s.Hash)
}

// loadStoredPasscode reads the persisted passcode hash at startup.
func loadStoredPasscode(secureDir string) {
	data, err := os.ReadFile(passcodeStorePath(secureDir))
	if err != nil {
		return
	}
	var s storedPasscode
	if err := json.Unmarshal(data, &s); err != nil || len(s.Hash) == 0 {
		return
	}
	passcodeMu.Lock()
	passcodeState = &s
	passcodeMu.Unlock()
	slog.Info("tamper-protect: unprotect passcode loaded", "mode", s.Mode, "issued_at", s.IssuedAt)
}

// handleSetUnprotectSecretCmd applies a signed set_unprotect_secret command
// (payload = JSON {mode, passcode, issued_at}). Anti-rollback on issued_at. The
// plaintext passcode is hashed on arrival and never stored.
func (d *dispatcher) handleSetUnprotectSecretCmd(ctx context.Context, _ pb.AgentControlPlaneClient, cmd *pb.Command) {
	var spec struct {
		Mode     string `json:"mode"`
		Passcode string `json:"passcode"`
		IssuedAt int64  `json:"issued_at"`
	}
	if err := json.Unmarshal(cmd.Payload, &spec); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "invalid unprotect-secret payload"))
		return
	}
	if spec.Mode == "" {
		spec.Mode = "static"
	}
	if spec.Mode != "static" {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "unsupported unprotect mode: "+spec.Mode))
		return
	}
	if len(spec.Passcode) < 4 {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "passcode too short"))
		return
	}
	if spec.IssuedAt <= currentPasscodeIssuedAt() {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "unprotect secret not newer than the active one (anti-rollback)"))
		return
	}
	salt := make([]byte, passcodeSaltLen)
	if _, err := rand.Read(salt); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "salt generation failed"))
		return
	}
	s := storedPasscode{
		Mode:     "static",
		Salt:     salt,
		Iter:     passcodeIterations,
		Hash:     pbkdf2SHA256([]byte(spec.Passcode), salt, passcodeIterations, passcodeKeyLen),
		IssuedAt: spec.IssuedAt,
	}
	if err := writePasscodeStore(d.cfg.SecureDir, s); err != nil {
		sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_FAILED, "persist passcode failed: "+err.Error()))
		return
	}
	passcodeMu.Lock()
	passcodeState = &s
	passcodeMu.Unlock()
	recordAuditEvent(ctx, d.outbox, auditlog.Event{
		DeviceID: d.cfg.DeviceID, EventType: "unprotect_secret_set", CommandID: cmd.CommandId,
		Actor: cmd.Actor, Source: cmd.Source, Decision: "applied", Reason: "unprotect passcode set (mode=static)",
	})
	slog.Info("tamper-protect: unprotect passcode set", "command_id", cmd.CommandId, "issued_at", spec.IssuedAt)
	sendStatus(d.outbox, simpleActionStatus(cmd.CommandId, actionSetUnprotect, d.cfg.DeviceID, "", pb.ActionPhase_ACTION_PHASE_SUCCESS, "unprotect passcode set"))
}

func writePasscodeStore(secureDir string, s storedPasscode) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(secureDir, 0o700); err != nil {
		return err
	}
	path := passcodeStorePath(secureDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ---- rate limiting (defends the short online passcode path) ----

var (
	rlMu    sync.Mutex
	rlFails int
	rlUntil time.Time
)

const (
	rlMaxFails = 5
	rlLockout  = 5 * time.Minute
)

func passcodeLockedOut() (bool, time.Duration) {
	rlMu.Lock()
	defer rlMu.Unlock()
	if time.Now().Before(rlUntil) {
		return true, time.Until(rlUntil)
	}
	return false, 0
}

func passcodeRecordFail() {
	rlMu.Lock()
	defer rlMu.Unlock()
	rlFails++
	if rlFails >= rlMaxFails {
		rlUntil = time.Now().Add(rlLockout)
		rlFails = 0
	}
}

func passcodeRecordSuccess() {
	rlMu.Lock()
	rlFails = 0
	rlUntil = time.Time{}
	rlMu.Unlock()
}

// authorizeUnprotect is the single gate for a protected operation (stop/uninstall):
// a valid short PASSCODE (if one is configured) OR a valid long signed TOKEN.
// Rate-limited to blunt online brute force of a short passcode.
func authorizeUnprotect(input, deviceID string) (bool, string) {
	if locked, d := passcodeLockedOut(); locked {
		return false, fmt.Sprintf("locked out — too many failed attempts, retry in %s", d.Round(time.Second))
	}
	if passcodeConfigured() && verifyPasscode(input) {
		passcodeRecordSuccess()
		return true, ""
	}
	if ok, _ := verifyUnprotectToken(input, deviceID); ok {
		passcodeRecordSuccess()
		return true, ""
	}
	passcodeRecordFail()
	return false, "invalid passcode/token"
}
