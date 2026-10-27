package main

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/pkg/softdistplan"
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

type dependencyTestStream struct {
	pb.AgentControlPlane_ConnectServer
	t       *testing.T
	srv     *server
	device  string
	phase   pb.ActionPhase
	noReply bool
	calls   int
	order   []string
}

func (s *dependencyTestStream) Send(msg *pb.ServerMessage) error {
	s.calls++
	cmd := msg.GetCommand()
	if !commandsign.Verify(loadSignKey().Public().(ed25519.PublicKey), cmd.Type, cmd.CommandId, cmd.Payload, cmd.Signature) {
		s.t.Fatal("unsigned dependency action")
	}
	var spec pb.SoftwareDeploySpec
	if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
		return err
	}
	s.order = append(s.order, spec.PackageId)
	if s.noReply {
		return nil
	}
	// A flood of progress must never evict a terminal result. A different
	// authenticated device cannot satisfy this action even by spoofing its ID.
	for i := 0; i < 100; i++ {
		s.srv.deliverPlanStatus(s.device, &pb.ActionStatus{ActionId: cmd.CommandId, DeviceId: s.device, Phase: pb.ActionPhase_ACTION_PHASE_RUNNING})
	}
	st := &pb.ActionStatus{ActionId: cmd.CommandId, DeviceId: s.device, ActionType: "software_deploy", Phase: pb.ActionPhase_ACTION_PHASE_SUCCESS, FinishedAt: 1}
	if s.srv.deliverPlanStatus("other-device", st) {
		s.t.Fatal("other device satisfied plan action")
	}
	if s.calls == 1 {
		st.Phase = s.phase
	}
	if !s.srv.deliverPlanStatus(s.device, st) {
		s.t.Fatal("terminal plan result not routed")
	}
	return nil
}

func TestMockDependencyDispatchGateAndCorrelation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase pb.ActionPhase
		want  string
		calls int
	}{
		{"install", pb.ActionPhase_ACTION_PHASE_SUCCESS, "success", 2},
		{"already installed", pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, "success", 2},
		{"failed", pb.ActionPhase_ACTION_PHASE_FAILED, "failed", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &server{}
			stream := &dependencyTestStream{t: t, srv: srv, device: "device-a", phase: tc.phase}
			conn := &liveAgent{deviceID: stream.device, stream: stream}
			spec := func(name string) *pb.SoftwareDeploySpec {
				return &pb.SoftwareDeploySpec{Name: name, InstallerType: "command", FilePath: "reg.exe", UninstallKey: `HKLM64\` + name}
			}
			plan, err := softdistplan.Build("app", map[string]softdistplan.Package{"app": {Requires: []string{"runtime"}, Spec: spec("app")}, "runtime": {Spec: spec("runtime")}})
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Clear()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			res := softdistplan.Run(ctx, "job-1", stream.device, plan, func(ctx context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
				return srv.executePlanPackage(ctx, conn, id, typ, spec, "")
			})
			if res.Status != tc.want || stream.calls != tc.calls || stream.order[0] != "runtime" {
				t.Fatalf("%+v calls=%d order=%v", res, stream.calls, stream.order)
			}
			if len(srv.planPending) != 0 || len(srv.planContent) != 0 {
				t.Fatal("completed plan leaked correlation/content authorization")
			}
		})
	}
}

func TestPlanWaitCancellationAndContentAuthorization(t *testing.T) {
	srv := &server{planAttempted: map[string]bool{"a": true, "b": true}, planContent: map[string]fileContent{"a\x00runtime": {deviceID: "a", path: "runtime.msi", sha256: "pinned"}, "a\x00app": {deviceID: "a", path: "app.msi", sha256: "other"}}}
	if path, planned, err := srv.planPackageContent("a", "runtime", "pinned"); err != nil || !planned || path != "runtime.msi" {
		t.Fatal(path, planned, err)
	}
	for _, request := range [][3]string{{"a", "runtime", "wrong"}, {"b", "runtime", "pinned"}, {"a", "unknown", "pinned"}} {
		if _, _, err := srv.planPackageContent(request[0], request[1], request[2]); err == nil {
			t.Fatal("unauthorized package request allowed")
		}
	}
	stream := &dependencyTestStream{t: t, srv: srv, device: "a", noReply: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := srv.executePlanPackage(ctx, &liveAgent{deviceID: "a", stream: stream}, "cancelled-action", "software_deploy_v2", &pb.SoftwareDeploySpec{ContentId: "cancelled-content"}, "cancelled.msi")
	if err == nil || len(srv.planPending) != 0 {
		t.Fatal("cancelled action retained waiter")
	}
	if _, ok := srv.planContent["a\x00cancelled-content"]; ok {
		t.Fatal("cancelled action retained content authorization")
	}
}

func TestMockDependencySettings(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		fail bool
	}{
		{"absent", nil, false},
		{"command", map[string]string{"AEGIS_DEPENDENCY_TYPE": "command", "AEGIS_DEPENDENCY_FILE": "reg.exe", "AEGIS_DEPENDENCY_UNINSTALL_KEY": `HKLM64\runtime`}, false},
		{"independent context", map[string]string{"AEGIS_DEPENDENCY_TYPE": "command", "AEGIS_DEPENDENCY_FILE": "reg.exe", "AEGIS_DEPENDENCY_RUN_AS": "system", "AEGIS_DEPENDENCY_VERSION": "2.0"}, false},
		{"missing served file", map[string]string{"AEGIS_DEPENDENCY_CONTENT_ID": "runtime-r1", "AEGIS_DEPENDENCY_TYPE": "msi"}, true},
		{"invalid context", map[string]string{"AEGIS_DEPENDENCY_FILE": "reg.exe", "AEGIS_DEPENDENCY_RUN_AS": "unknown"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := readMockDependency(func(key string) string { return tc.env[key] })
			if (err != nil) != tc.fail {
				t.Fatal(err)
			}
			if spec != nil && len(spec.SecureParameters) > 0 {
				t.Fatal("unexpected inherited secrets")
			}
		})
	}
}
