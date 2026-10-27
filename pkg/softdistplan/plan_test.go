package softdistplan

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func packageSpec(id string) *pb.SoftwareDeploySpec {
	return &pb.SoftwareDeploySpec{Name: id, InstallerType: "command", FilePath: "reg.exe", SilentArgs: "fixture", UninstallKey: `HKLM64\` + id}
}

func terminal(device, id string, phase pb.ActionPhase) *pb.ActionStatus {
	return &pb.ActionStatus{ActionId: id, DeviceId: device, ActionType: "software_deploy", Phase: phase, FinishedAt: 1}
}

func TestBuildDiamondOrderAndImmutableSnapshot(t *testing.T) {
	catalog := map[string]Package{
		"app-r1":     {Requires: []string{"runtime-r1", "plugin-r1"}, Spec: packageSpec("app")},
		"runtime-r1": {Requires: []string{"common-r1"}, Spec: packageSpec("runtime")},
		"plugin-r1":  {Requires: []string{"common-r1"}, Spec: packageSpec("plugin")},
		"common-r1":  {Spec: packageSpec("common")},
	}
	plan, err := Build("app-r1", catalog)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Clear()
	if got := strings.Join(plan.PackageIDs(), ","); got != "common-r1,runtime-r1,plugin-r1,app-r1" {
		t.Fatal(got)
	}
	catalog["common-r1"].Spec.FilePath = "changed-after-plan"
	calls := 0
	res := Run(context.Background(), "job-1", "device-a", plan, func(_ context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
		calls++
		if typ != "software_deploy_v2" || spec.Execution.GetMode() != "system" || spec.FilePath != "reg.exe" {
			t.Fatalf("snapshot or version-aware explicit context lost: %+v", spec)
		}
		return terminal(device, id, pb.ActionPhase_ACTION_PHASE_SUCCESS), nil
	})
	if calls != 4 || res.Status != "success" {
		t.Fatalf("%d %+v", calls, res)
	}
}

func TestGraphRefusalsBeforeAnyExecution(t *testing.T) {
	for _, tc := range []struct {
		name    string
		catalog map[string]Package
		want    string
	}{
		{"cycle", map[string]Package{"root": {Requires: []string{"dep"}, Spec: packageSpec("root")}, "dep": {Requires: []string{"root"}, Spec: packageSpec("dep")}}, "circular"},
		{"missing", map[string]Package{"root": {Requires: []string{"missing"}, Spec: packageSpec("root")}}, "missing catalog"},
		{"unverifiable dependency", map[string]Package{"root": {Requires: []string{"dep"}, Spec: packageSpec("root")}, "dep": {Spec: &pb.SoftwareDeploySpec{InstallerType: "command", FilePath: "cmd.exe"}}}, "detection"},
		{"uninstall dependency", map[string]Package{"root": {Requires: []string{"dep"}, Spec: packageSpec("root")}, "dep": {Spec: &pb.SoftwareDeploySpec{Action: "uninstall", InstallerType: "command", FilePath: "cmd.exe", UninstallKey: "dep"}}}, "installation only"},
		{"unpinned dependency", map[string]Package{"root": {Requires: []string{"dep"}, Spec: packageSpec("root")}, "dep": {Spec: &pb.SoftwareDeploySpec{InstallerType: "msi", FilePath: "dep.msi", UninstallKey: "dep"}}}, "pinned payload hash"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := Build("root", tc.catalog)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%+v %v", p, err)
			}
			if len(p.PackageIDs()) != 0 {
				t.Fatal("invalid plan retained executable nodes")
			}
		})
	}
	catalog := map[string]Package{}
	for i := 0; i < MaxPackages+1; i++ {
		id := fmt.Sprintf("p%d", i)
		pkg := Package{Spec: packageSpec(id)}
		if i < MaxPackages {
			pkg.Requires = []string{fmt.Sprintf("p%d", i+1)}
		}
		catalog[id] = pkg
	}
	if _, err := Build("p0", catalog); err == nil {
		t.Fatal("oversized graph allowed")
	}
}

func TestDependencyOutcomesGateMain(t *testing.T) {
	for _, tc := range []struct {
		name      string
		phase     pb.ActionPhase
		reboot    bool
		transport bool
		want      string
		calls     int
	}{
		{"install then main", pb.ActionPhase_ACTION_PHASE_SUCCESS, false, false, "success", 2},
		{"already installed skip then main", pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE, false, false, "success", 2},
		{"dependency failure", pb.ActionPhase_ACTION_PHASE_FAILED, false, false, "failed", 1},
		{"reboot dependency", pb.ActionPhase_ACTION_PHASE_SUCCESS, true, false, "blocked", 1},
		{"uncertain disconnect", pb.ActionPhase_ACTION_PHASE_SUCCESS, false, true, "blocked", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := Build("root", map[string]Package{"root": {Requires: []string{"dep"}, Spec: packageSpec("root")}, "dep": {Spec: packageSpec("dep")}})
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Clear()
			calls := 0
			result := Run(context.Background(), "job-1", "device-a", plan, func(_ context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
				calls++
				if calls == 1 {
					if tc.transport {
						return nil, errors.New("disconnected")
					}
					st := terminal(device, id, tc.phase)
					st.RebootRequired = tc.reboot
					return st, nil
				}
				return terminal(device, id, pb.ActionPhase_ACTION_PHASE_SUCCESS), nil
			})
			if calls != tc.calls || result.Status != tc.want || result.RebootRequired != tc.reboot {
				t.Fatalf("calls=%d %+v", calls, result)
			}
		})
	}
}

func TestRejectWrongOrNonterminalResult(t *testing.T) {
	for _, mutation := range []func(*pb.ActionStatus){
		func(st *pb.ActionStatus) { st.DeviceId = "other-device" },
		func(st *pb.ActionStatus) { st.ActionId = "other-action" },
		func(st *pb.ActionStatus) { st.ActionType = "run_command" },
		func(st *pb.ActionStatus) { st.FinishedAt = 0 },
		func(st *pb.ActionStatus) { st.Phase = pb.ActionPhase_ACTION_PHASE_RUNNING },
	} {
		plan, _ := Build("root", map[string]Package{"root": {Requires: []string{"dep"}, Spec: packageSpec("root")}, "dep": {Spec: packageSpec("dep")}})
		calls := 0
		res := Run(context.Background(), "job-1", "device-a", plan, func(_ context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
			calls++
			st := terminal(device, id, pb.ActionPhase_ACTION_PHASE_SUCCESS)
			mutation(st)
			return st, nil
		})
		plan.Clear()
		if res.Status != "blocked" || calls != 1 {
			t.Fatalf("invalid result advanced main: %+v", res)
		}
	}
}

func TestCancellationAndCredentialsAreIsolated(t *testing.T) {
	dep := packageSpec("dep")
	main := packageSpec("main")
	main.Execution = &pb.SoftwareExecution{Mode: "specified_account", Username: "example", PasswordUtf8: []byte("private-password")}
	main.SecureParameters = map[string]string{"TOKEN": "main-only-token"}
	plan, err := Build("root", map[string]Package{"root": {Requires: []string{"dep"}, Spec: main}, "dep": {Spec: dep}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Clear()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	res := Run(ctx, "job-1", "device-a", plan, func(_ context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
		calls++
		if spec.Execution.Mode != "system" || len(spec.SecureParameters) > 0 || len(spec.Execution.PasswordUtf8) > 0 {
			t.Fatal("parent credentials inherited by dependency")
		}
		cancel()
		return terminal(device, id, pb.ActionPhase_ACTION_PHASE_SUCCESS), nil
	})
	if calls != 1 || res.Status != "cancelled" {
		t.Fatalf("%+v", res)
	}
	if string(main.Execution.PasswordUtf8) != "private-password" {
		t.Fatal("caller catalog mutated")
	}
	plan.Clear()
	if string(main.Execution.PasswordUtf8) != "private-password" {
		t.Fatal("clear mutated caller catalog")
	}
}

func TestActionIDsAreIsolatedByDeviceAndJob(t *testing.T) {
	plan, _ := Build("root", map[string]Package{"root": {Spec: packageSpec("root")}})
	defer plan.Clear()
	seen := map[string]bool{}
	for _, pair := range [][2]string{{"job-1", "device-a"}, {"job-1", "device-b"}, {"job-2", "device-a"}} {
		res := Run(context.Background(), pair[0], pair[1], plan, func(_ context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
			if seen[id] {
				t.Fatal("action ID collision across job/device")
			}
			seen[id] = true
			return terminal(device, id, pb.ActionPhase_ACTION_PHASE_SUCCESS), nil
		})
		if res.Status != "success" {
			t.Fatal(res)
		}
	}
}

func TestConflictingContentIdentityRejected(t *testing.T) {
	root, dep := packageSpec("root"), packageSpec("dep")
	for _, spec := range []*pb.SoftwareDeploySpec{root, dep} {
		spec.InstallerType, spec.FilePath, spec.ContentId = "msi", "", "shared-payload-r1"
	}
	root.ExpectedHash, dep.ExpectedHash = strings.Repeat("1", 64), strings.Repeat("2", 64)
	if _, err := Build("root", map[string]Package{"root": {Requires: []string{"dep"}, Spec: root}, "dep": {Spec: dep}}); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatal("one content ID was allowed to name two different installers")
	}
}

func TestAdvancedRulesQualifyDependencyAndSelectNewTask(t *testing.T) {
	dep := &pb.SoftwareDeploySpec{InstallerType: "command", FilePath: "cmd.exe", Detection: &pb.SoftwareDetection{Rules: []*pb.SoftwareDetectionRule{{Kind: "file_exists", Path: `C:\app\runtime.dll`}}}}
	plan, err := Build("app", map[string]Package{"app": {Requires: []string{"runtime"}, Spec: packageSpec("main")}, "runtime": {Spec: dep}})
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Clear()
	result := Run(context.Background(), "job-1", "device-a", plan, func(_ context.Context, device, id, typ string, spec *pb.SoftwareDeploySpec) (*pb.ActionStatus, error) {
		if spec.PackageId == "runtime" && (typ != "software_deploy_detect" || spec.Detection == nil) {
			t.Fatal("dependency lost required advanced detection")
		}
		return terminal(device, id, pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE), nil
	})
	if result.Status != "success" {
		t.Fatal(result)
	}
}
