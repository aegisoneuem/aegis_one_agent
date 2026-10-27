// Package softdistplan resolves and executes server-side package dependencies.
// The backend owns catalog snapshots, authorization, persistence and signing;
// agents continue to execute one signed SoftwareDeploySpec at a time.
package softdistplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/deployspec"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

const MaxPackages = 64

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var productGUID = regexp.MustCompile(`^\{[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}\}$`)

// Package is a pinned catalog revision. Requires refers to exact catalog keys,
// never a floating "latest" lookup. Each package carries its OWN execution and
// parameter settings; credentials are never inherited from its parent.
type Package struct {
	Requires []string
	Spec     *pb.SoftwareDeploySpec
}

type node struct {
	id, commandType string
	spec            *pb.SoftwareDeploySpec
}

// Plan is an immutable snapshot after Build. Use one plan per run and Clear it
// after use to release its private inputs. Do not serialize it into job logs.
type Plan struct {
	nodes []node
}

func (p *Plan) PackageIDs() []string {
	var out []string
	for _, n := range p.nodes {
		out = append(out, n.id)
	}
	return out
}

func clearSpec(spec *pb.SoftwareDeploySpec) {
	clear(spec.GetExecution().GetPasswordUtf8())
	clear(spec.GetSecureParameters())
}

func (p *Plan) Clear() {
	for _, n := range p.nodes {
		clearSpec(n.spec)
	}
	p.nodes = nil
}

// Build rejects the entire reachable graph before any command can be sent.
// A shared dependency in a diamond is emitted once in deterministic author order.
func Build(root string, catalog map[string]Package) (plan *Plan, err error) {
	plan = &Plan{}
	defer func() {
		if err != nil {
			plan.Clear()
		}
	}()
	state := map[string]uint8{}
	contentHashes := map[string]string{}
	var visit func(string, int) error
	visit = func(id string, depth int) error {
		if !identifier.MatchString(id) {
			return fmt.Errorf("invalid package revision identifier")
		}
		if state[id] == 1 {
			return fmt.Errorf("circular package dependency at %s", id)
		}
		if state[id] == 2 {
			return nil
		}
		if depth > MaxPackages || len(state) >= MaxPackages {
			return fmt.Errorf("dependency plan exceeds %d packages", MaxPackages)
		}
		pkg, ok := catalog[id]
		if !ok {
			return fmt.Errorf("missing catalog package revision %s", id)
		}
		if len(pkg.Requires) > MaxPackages {
			return fmt.Errorf("too many dependency references for %s", id)
		}
		typ, validationErr := validateSpec(pkg.Spec, id, id != root)
		if validationErr != nil {
			return fmt.Errorf("package %s: %w", id, validationErr)
		}
		if contentID := pkg.Spec.ContentId; contentID != "" {
			digest := strings.ToLower(pkg.Spec.ExpectedHash)
			if existing, ok := contentHashes[contentID]; ok && existing != digest {
				return fmt.Errorf("content ID %s names conflicting payload hashes", contentID)
			}
			contentHashes[contentID] = digest
		}
		state[id] = 1
		for _, dep := range pkg.Requires {
			if err := visit(dep, depth+1); err != nil {
				return err
			}
		}
		copy := proto.Clone(pkg.Spec).(*pb.SoftwareDeploySpec)
		copy.PackageId = id
		// Explicit SYSTEM also uses the worker's scope-aware/version-aware
		// detection instead of the legacy early MSI-presence-only shortcut.
		if copy.Execution == nil {
			copy.Execution = &pb.SoftwareExecution{Mode: "system", DetectionScope: "machine"}
		}
		plan.nodes = append(plan.nodes, node{id: id, commandType: typ, spec: copy})
		state[id] = 2
		return nil
	}
	err = visit(root, 1)
	return plan, err
}

func validateSpec(s *pb.SoftwareDeploySpec, id string, dependency bool) (string, error) {
	if s == nil {
		return "", fmt.Errorf("deployment specification is required")
	}
	if action := strings.ToLower(strings.TrimSpace(s.Action)); action != "" && action != "install" {
		return "", fmt.Errorf("dependency plans support installation only")
	}
	if s.TimeoutSeconds < 0 {
		return "", fmt.Errorf("package timeout must be nonnegative")
	}
	switch strings.ToLower(strings.TrimSpace(s.InstallerType)) {
	case "", "auto", "msi", "exe", "ps1", "bat", "cmd", "py", "python", "vbs", "command":
	default:
		return "", fmt.Errorf("unsupported installer type")
	}
	if s.ProductCode != "" && !productGUID.MatchString(strings.TrimSpace(s.ProductCode)) {
		return "", fmt.Errorf("invalid MSI product code")
	}
	if s.ProductCode != "" && (s.UninstallKey != "" || s.DisplayName != "") {
		return "", fmt.Errorf("use one detection selector family")
	}
	if dependency && s.Detection == nil && strings.TrimSpace(s.ProductCode) == "" && strings.TrimSpace(s.UninstallKey) == "" && strings.TrimSpace(s.DisplayName) == "" {
		return "", fmt.Errorf("dependency requires installed-product detection; exit code alone is insufficient")
	}
	if strings.TrimSpace(s.FilePath) == "" && strings.TrimSpace(s.ContentId) == "" {
		return "", fmt.Errorf("installer or command is required")
	}
	command := strings.EqualFold(strings.TrimSpace(s.InstallerType), "command")
	if command && s.Bundle == nil && strings.TrimSpace(s.FilePath) == "" {
		return "", fmt.Errorf("command requires file_path")
	}
	if !command || s.Bundle != nil {
		algo := strings.ReplaceAll(strings.ToLower(s.HashAlgorithm), "-", "")
		if algo == "" {
			algo = "sha256"
		}
		size := 32
		if algo == "sha512" && s.Bundle == nil {
			size = 64
		} else if algo != "sha256" {
			return "", fmt.Errorf("invalid payload hash algorithm")
		}
		b, err := hex.DecodeString(s.ExpectedHash)
		if err != nil || len(b) != size {
			return "", fmt.Errorf("file-based package requires its pinned payload hash")
		}
	}
	e := s.GetExecution()
	var sessionID *uint32
	if e != nil {
		sessionID = e.SessionId
	}
	if err := (softdist.Execution{Mode: e.GetMode(), Username: e.GetUsername(), Domain: e.GetDomain(), Password: e.GetPasswordUtf8(), SessionID: sessionID, DetectionScope: e.GetDetectionScope()}).Validate(); err != nil {
		return "", err
	}
	// Include every public step/bundle field in private-value leak validation.
	p := softdist.Params{Name: s.Name, FilePath: s.FilePath, SilentArgs: s.SilentArgs, ProductCode: s.ProductCode, UninstallKey: s.UninstallKey, DisplayName: s.DisplayName, Version: s.Version, Parameters: s.Parameters, SecureParameters: s.SecureParameters,
		PublicMetadata: []string{id, s.PackageId, s.ContentId, s.Action, s.InstallerType, s.ExpectedHash, s.HashAlgorithm, e.GetUsername(), e.GetDomain()}}
	if b := s.Bundle; b != nil {
		p.PublicMetadata = append(p.PublicMetadata, b.EntryPoint, b.Command, b.Destination, b.Cleanup)
		if command && (strings.TrimSpace(b.Command) == "" || b.EntryPoint != "") {
			return "", fmt.Errorf("command bundle requires command and no entry point")
		}
		if !command && (strings.TrimSpace(b.EntryPoint) == "" || b.Command != "") {
			return "", fmt.Errorf("installer bundle requires entry point and no external command")
		}
	}
	if s.Steps != nil {
		if s.Bundle == nil {
			return "", fmt.Errorf("workflow requires a bundle")
		}
		steps := softdist.DeploymentSteps{}
		for index, list := range [][]*pb.SoftwareCommandStep{s.Steps.Before, s.Steps.After} {
			for _, v := range list {
				step := softdist.CommandStep{Name: v.GetName(), Command: v.GetCommand(), Arguments: v.GetArguments(), TimeoutSeconds: v.GetTimeoutSeconds(), SuccessCodes: v.GetSuccessExitCodes(), UseSecureParameters: v.GetUseSecureParameters()}
				if index == 0 {
					steps.Before = append(steps.Before, step)
				} else {
					steps.After = append(steps.After, step)
				}
			}
		}
		if err := steps.Validate(); err != nil {
			return "", err
		}
		p.PublicMetadata = append(p.PublicMetadata, steps.PublicFields()...)
	}
	if s.Detection != nil {
		if err := deployspec.ValidateDetection(s); err != nil {
			return "", err
		}
		p.PublicMetadata = append(p.PublicMetadata, deployspec.Detection(s.Detection).PublicFields()...)
	}
	if err := softdist.ValidatePrivateInput(p); err != nil {
		return "", err
	}
	if len(e.GetPasswordUtf8()) > 0 {
		p.SecureParameters = map[string]string{"RUN_AS_PASSWORD": string(e.GetPasswordUtf8())}
		if err := softdist.ValidatePrivateInput(p); err != nil {
			return "", fmt.Errorf("account password must not appear in public task fields")
		}
	}
	if s.Detection != nil {
		return "software_deploy_detect", nil
	}
	if s.Steps != nil {
		return "software_deploy_workflow", nil
	}
	if s.Bundle != nil {
		return "software_deploy_bundle", nil
	}
	return "software_deploy_v2", nil
}

type ActionResult struct {
	PackageID      string `json:"package_id"`
	ActionID       string `json:"action_id"`
	Phase          string `json:"phase"`
	ExitCode       int32  `json:"exit_code"`
	RebootRequired bool   `json:"reboot_required"`
	Reason         string `json:"reason"`
}
type Result struct {
	JobID          string         `json:"job_id"`
	DeviceID       string         `json:"device_id"`
	Status         string         `json:"status"` // success, failed, blocked, cancelled
	Reason         string         `json:"reason"`
	RebootRequired bool           `json:"reboot_required"`
	Actions        []ActionResult `json:"actions"`
}

// Execute must authorize the target, sign/send the command and await a terminal
// SOFTWARE ActionStatus from that device. A timeout/disconnect is uncertain,
// never permission to send the next command or automatically retry an installer.
type Execute func(context.Context, string, string, string, *pb.SoftwareDeploySpec) (*pb.ActionStatus, error)

func Run(ctx context.Context, jobID, deviceID string, plan *Plan, execute Execute) Result {
	res := Result{JobID: jobID, DeviceID: deviceID, Status: "failed"}
	if !identifier.MatchString(jobID) || strings.TrimSpace(deviceID) == "" || plan == nil || len(plan.nodes) == 0 || execute == nil {
		res.Reason = "valid job, device, plan and executor are required"
		return res
	}
	for index, n := range plan.nodes {
		if ctx.Err() != nil {
			res.Status = "cancelled"
			res.Reason = "plan cancelled; remaining packages not dispatched"
			return res
		}
		digest := sha256.Sum256([]byte(jobID + "\x00" + deviceID + "\x00" + n.id))
		actionID := "sdplan-" + hex.EncodeToString(digest[:16])
		copy := proto.Clone(n.spec).(*pb.SoftwareDeploySpec)
		st, err := execute(ctx, deviceID, actionID, n.commandType, copy)
		clearSpec(copy)
		if err != nil {
			res.Status = "blocked"
			if ctx.Err() != nil {
				res.Status = "cancelled"
			}
			res.Reason = "package " + n.id + " result unavailable; remaining packages not dispatched"
			return res
		}
		if st == nil || st.DeviceId != deviceID || st.ActionId != actionID || st.ActionType != "software_deploy" || st.FinishedAt == 0 ||
			(st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS && st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED && st.Phase != pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE) {
			res.Status = "blocked"
			res.Reason = "invalid terminal result for package " + n.id
			return res
		}
		res.Actions = append(res.Actions, ActionResult{PackageID: n.id, ActionID: actionID, Phase: st.Phase.String(), ExitCode: st.ExitCode, RebootRequired: st.RebootRequired, Reason: st.Reason})
		res.RebootRequired = res.RebootRequired || st.RebootRequired || st.ExitCode == 3010 || st.ExitCode == 1641
		if st.Phase == pb.ActionPhase_ACTION_PHASE_FAILED {
			res.Reason = "package " + n.id + " failed; remaining packages not dispatched"
			return res
		}
		if index < len(plan.nodes)-1 && (st.RebootRequired || st.ExitCode == 3010 || st.ExitCode == 1641) {
			res.Status = "blocked"
			res.RebootRequired = true
			res.Reason = "dependency " + n.id + " requires reboot; remaining packages not dispatched"
			return res
		}
	}
	res.Status = "success"
	res.Reason = "dependencies satisfied; main package completed"
	return res
}
