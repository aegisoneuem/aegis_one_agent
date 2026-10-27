package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/pkg/softdistplan"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

type planWait struct {
	deviceID string
	terminal chan *pb.ActionStatus
}

func readMockDependency(lookup func(string) string) (*pb.SoftwareDeploySpec, error) {
	get := func(key string) string { return lookup(strings.Replace(key, "AEGIS_DEPLOY_", "AEGIS_DEPENDENCY_", 1)) }
	if get("AEGIS_DEPLOY_FILE") == "" && get("AEGIS_DEPLOY_CONTENT_ID") == "" && get("AEGIS_DEPLOY_NAME") == "" && get("AEGIS_DEPLOY_ID") == "" {
		return nil, nil
	}
	if action := strings.ToLower(strings.TrimSpace(get("AEGIS_DEPLOY_ACTION"))); action != "" && action != "install" {
		return nil, fmt.Errorf("dependency action must be install")
	}
	options, err := readSoftdistTestOptions(get)
	if err != nil {
		return nil, err
	}
	spec := &pb.SoftwareDeploySpec{PackageId: get("AEGIS_DEPLOY_ID"), Name: get("AEGIS_DEPLOY_NAME"), Action: "install", InstallerType: get("AEGIS_DEPLOY_TYPE"), FilePath: get("AEGIS_DEPLOY_FILE"), ContentId: get("AEGIS_DEPLOY_CONTENT_ID"),
		ExpectedHash: get("AEGIS_DEPLOY_HASH"), HashAlgorithm: get("AEGIS_DEPLOY_HASH_ALGO"), SilentArgs: get("AEGIS_DEPLOY_ARGS"), ProductCode: get("AEGIS_DEPLOY_PRODUCT_CODE"), UninstallKey: get("AEGIS_DEPLOY_UNINSTALL_KEY"), DisplayName: get("AEGIS_DEPLOY_DISPLAY_NAME"), Version: get("AEGIS_DEPLOY_VERSION"), Parameters: parseParams(get("AEGIS_DEPLOY_PARAMS")), TimeoutSeconds: options.timeout, SuccessExitCodes: options.successCodes, RequireSignature: truthy(get("AEGIS_DEPLOY_REQUIRE_SIG"))}
	if spec.PackageId == "" {
		spec.PackageId = "dependency-demo-r1"
	}
	ok := false
	defer func() {
		if !ok {
			clear(spec.GetExecution().GetPasswordUtf8())
			clear(spec.SecureParameters)
		}
	}()
	if _, err = applyPrivateDeployment(spec, get); err != nil {
		return nil, err
	}
	if _, err = applyMockBundle(spec, get); err != nil {
		return nil, err
	}
	if _, err = applyMockWorkflow(spec, get); err != nil {
		return nil, err
	}
	if _, err = applyMockDetection(spec, get); err != nil {
		return nil, err
	}
	if spec.ContentId != "" && spec.FilePath == "" && strings.TrimSpace(lookup("AEGIS_DEPENDENCY_SERVE_FILE")) == "" {
		return nil, fmt.Errorf("server-delivered dependency needs AEGIS_DEPENDENCY_SERVE_FILE")
	}
	if options.autoHash && spec.ExpectedHash == "" && (!strings.EqualFold(spec.InstallerType, "command") || spec.Bundle != nil) {
		path := lookup("AEGIS_DEPENDENCY_SERVE_FILE")
		if path == "" {
			path = spec.FilePath
		}
		if sum, e := fileSHA256(path); e == nil {
			spec.ExpectedHash = sum
			spec.HashAlgorithm = "sha256"
		}
	}
	ok = true
	return spec, nil
}

// beginDependencyPlan snapshots the two configured packages before the caller
// clears credentials. The reusable runner supports full DAGs; the mock exposes
// one dependency through environment fields to keep manual testing readable.
func (s *server) beginDependencyPlan(ctx context.Context, connection *liveAgent, main *pb.SoftwareDeploySpec) bool {
	dep, err := readMockDependency(os.Getenv)
	if err != nil {
		mockPrintf("Dependency settings refused: %v\n", err)
		return true
	}
	if dep == nil {
		return false
	}
	defer func() { clear(dep.GetExecution().GetPasswordUtf8()); clear(dep.SecureParameters) }()
	rootID := main.PackageId
	if rootID == "" {
		rootID = "main-package-r1"
	}
	if dep.PackageId == rootID {
		mockPrintln("Dependency plan refused: main and dependency must have distinct revision IDs")
		return true
	}
	plan, err := softdistplan.Build(rootID, map[string]softdistplan.Package{rootID: {Requires: []string{dep.PackageId}, Spec: main}, dep.PackageId: {Spec: dep}})
	if err != nil {
		mockPrintf("Dependency plan refused: %v\n", err)
		return true
	}
	timeout := int64(3600)
	if raw := os.Getenv("AEGIS_DEPLOY_PLAN_TIMEOUT"); raw != "" {
		timeout, err = strconv.ParseInt(raw, 10, 32)
		if err != nil || timeout <= 0 || timeout > 86400 {
			plan.Clear()
			mockPrintln("Dependency plan refused: AEGIS_DEPLOY_PLAN_TIMEOUT must be 1-86400 seconds")
			return true
		}
	}
	s.pendingMu.Lock()
	if s.planAttempted == nil {
		s.planAttempted = map[string]bool{}
	}
	if s.planAttempted[connection.deviceID] {
		s.pendingMu.Unlock()
		plan.Clear()
		mockPrintf("Dependency plan already attempted for %s; reconnect does not replay it\n", connection.deviceID)
		return true
	}
	s.planAttempted[connection.deviceID] = true
	s.pendingMu.Unlock()
	jobID := "softdist-plan-" + nshRandomID()
	mainContent, dependencyContent := os.Getenv("AEGIS_SERVE_SOFTWARE"), os.Getenv("AEGIS_DEPENDENCY_SERVE_FILE")
	dependencyID := dep.PackageId
	go func() {
		defer plan.Clear()
		planCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
		defer cancel()
		mockPrintf("PLAN %s device=%s order=%s\n", jobID, connection.deviceID, strings.Join(plan.PackageIDs(), " -> "))
		result := softdistplan.Run(planCtx, jobID, connection.deviceID, plan, func(ctx context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
			path := mainContent
			if spec.PackageId == dependencyID {
				path = dependencyContent
			}
			return s.executePlanPackage(ctx, connection, id, typ, spec, path)
		})
		mockPrintf("PLAN %s %s device=%s reboot_required=%v reason=%s\n", jobID, strings.ToUpper(result.Status), connection.deviceID, result.RebootRequired, result.Reason)
		// Result metadata contains no specifications, credentials or installer output.
		path := filepath.Join("logs", "softdist-plans", jobID+".json")
		if raw, e := json.MarshalIndent(result, "", "  "); e == nil {
			if e = os.MkdirAll(filepath.Dir(path), 0700); e == nil {
				e = os.WriteFile(path, raw, 0600)
			}
			if e != nil {
				mockPrintf("Could not write dependency plan result: %v\n", e)
			}
		}
	}()
	return true
}

func (s *server) executePlanPackage(ctx context.Context, connection *liveAgent, id, typ string, spec *pb.SoftwareDeploySpec, contentPath string) (*pb.ActionStatus, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wait := &planWait{deviceID: connection.deviceID, terminal: make(chan *pb.ActionStatus, 1)}
	contentKey := connection.deviceID + "\x00" + spec.ContentId
	s.pendingMu.Lock()
	if s.planPending == nil {
		s.planPending = map[string]*planWait{}
	}
	if _, exists := s.planPending[id]; exists {
		s.pendingMu.Unlock()
		return nil, fmt.Errorf("duplicate plan action")
	}
	s.planPending[id] = wait
	if contentPath != "" && spec.ContentId != "" {
		if s.planContent == nil {
			s.planContent = map[string]fileContent{}
		}
		s.planContent[contentKey] = fileContent{deviceID: connection.deviceID, path: contentPath, sha256: spec.ExpectedHash}
	}
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.planPending, id)
		delete(s.planContent, contentKey)
		s.pendingMu.Unlock()
	}()
	raw, err := proto.Marshal(spec)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	err = connection.send(&pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{CommandId: id, Type: typ, Payload: raw})}})
	clear(raw)
	clear(spec.GetExecution().GetPasswordUtf8())
	clear(spec.SecureParameters)
	if err != nil {
		return nil, err
	}
	mockPrintf("PLAN package=%s action_id=%s sent type=%s device=%s (signed)\n", spec.PackageId, id, typ, connection.deviceID)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case st := <-wait.terminal:
		mockPrintf("PLAN package=%s phase=%s exit_code=%d reboot_required=%v reason=%s\n", spec.PackageId, st.Phase, st.ExitCode, st.RebootRequired, st.Reason)
		return st, nil
	}
}

// The authenticated stream identity, not the message's claimed device ID,
// decides who can satisfy a pending package. Nonterminal updates cannot fill
// the terminal channel and crowd out the result.
func (s *server) deliverPlanStatus(authenticatedDevice string, st *pb.ActionStatus) bool {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	wait := s.planPending[st.ActionId]
	if wait == nil || wait.deviceID != authenticatedDevice || st.DeviceId != authenticatedDevice {
		return false
	}
	if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS && st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED && st.Phase != pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE {
		return false
	}
	select {
	case wait.terminal <- proto.Clone(st).(*pb.ActionStatus):
	default:
	}
	return true
}

func (s *server) planPackageContent(device, id, digest string) (string, bool, error) {
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	content, found := s.planContent[device+"\x00"+id]
	if found {
		if !strings.EqualFold(content.sha256, digest) {
			return "", true, fmt.Errorf("package digest does not match the dispatched task")
		}
		return content.path, true, nil
	}
	if s.planAttempted[device] {
		return "", true, fmt.Errorf("package is not authorized by an active plan action")
	}
	return "", false, nil
}
