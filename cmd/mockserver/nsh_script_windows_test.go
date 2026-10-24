//go:build windows

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/aegisoneuem/aegis_one_agent/pkg/nsh"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

func TestNativePowerShellScriptUsesCommandContract(t *testing.T) {
	for _, shell := range []string{"powershell", "pwsh"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell + ".exe")
			if err != nil {
				t.Skip("interpreter not installed")
			}
			s := &nshSession{}
			e := newScriptEngine(s)
			e.vars["TAG"] = "daily-23oct"
			content := e.expand("Write-Host \"tag=${TAG} on $env:COMPUTERNAME\"\n# comment\n\nWrite-Host \"env tag = $env:AEGIS_PARAM_TAG\"\nWrite-Output 'quote \" preserved — नमस्ते'\nexit 7")
			text, picked := buildShellCommand(shell, content)
			if picked != shell {
				t.Fatalf("picked %s", picked)
			}
			// Same -Command contract as resolveShell on the agent.
			cmd := exec.Command(bin, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", text)
			cmd.Env = append(os.Environ(), "AEGIS_PARAM_TAG=daily-23oct")
			out, err := cmd.CombinedOutput()
			if ee, ok := err.(*exec.ExitError); !ok || ee.ExitCode() != 7 {
				t.Fatalf("exit=%v, output=%s", err, out)
			}
			for _, want := range []string{"tag=daily-23oct on ", "env tag = daily-23oct", "quote \" preserved"} {
				if !strings.Contains(string(out), want) {
					t.Fatalf("missing %q: %s", want, out)
				}
			}
		})
	}
}

// Dispatch through the real NSH foreach/HEREDOC parser and command correlation,
// executing native PowerShell with the same -Command contract as the agent.
type scriptExecutionStream struct {
	pb.AgentControlPlane_ConnectServer
	server  *server
	device  string
	results []*pb.ActionStatus
}

func (s *scriptExecutionStream) Send(msg *pb.ServerMessage) error {
	command := msg.GetCommand()
	var spec pb.RunCommandSpec
	if err := proto.Unmarshal(command.Payload, &spec); err != nil {
		return err
	}
	cmd := exec.Command("powershell.exe", "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", spec.Command)
	if spec.Shell == "cmd" {
		cmd = exec.Command("cmd.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /D /S /C "` + spec.Command + `"`}
	}
	cmd.Dir = spec.WorkingDir
	cmd.Env = os.Environ()
	for name, value := range spec.Parameters {
		cmd.Env = append(cmd.Env, "AEGIS_PARAM_"+strings.ToUpper(name)+"="+value)
	}
	out, err := cmd.CombinedOutput()
	phase, exit := pb.ActionPhase_ACTION_PHASE_SUCCESS, 0
	if err != nil {
		phase, exit = pb.ActionPhase_ACTION_PHASE_FAILED, 1
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		}
	}
	detail, _ := json.Marshal(map[string]any{"stdout": string(out)})
	st := &pb.ActionStatus{ActionId: command.CommandId, DeviceId: s.device, Phase: phase, ExitCode: int32(exit), Reason: fmt.Sprintf("exit=%d", exit), Detail: string(detail)}
	s.results = append(s.results, st)
	s.server.deliverActionStatus(st)
	return nil
}

func TestG2ForeachHeredocParametersAndUnhandledErrors(t *testing.T) {
	if _, err := exec.LookPath("powershell.exe"); err != nil {
		t.Skip("PowerShell not installed")
	}
	t.Setenv("REGION", "apac")
	for _, badCommand := range []bool{false, true} {
		t.Run(fmt.Sprintf("badCommand=%v", badCommand), func(t *testing.T) {
			srv := &server{byID: make(map[string]*deviceRecord), liveAgents: make(map[string]*liveAgent), pending: make(map[string]chan *pb.ActionStatus)}
			streams := []*scriptExecutionStream{}
			for _, id := range []string{"vm-a", "vm-b"} {
				srv.byID[id] = &deviceRecord{DeviceID: id, Hostname: id}
				stream := &scriptExecutionStream{server: srv, device: id}
				streams = append(streams, stream)
				srv.liveAgents[id] = &liveAgent{deviceID: id, stream: stream}
			}
			sess := &nshSession{srv: srv, timeout: 20, shell: "cmd"}
			sess.targets = []string{"vm-b"}
			sess.engine = newScriptEngine(sess)
			body := `Write-Output "host=${host}, region=${REGION}, env=$env:AEGIS_PARAM_REGION"`
			if badCommand {
				body = "Get-AegisDeliberatelyMissingCmdlet\nWrite-Output 'SHOULD_NOT_RUN'"
			}
			script := "let REGION = ${env:REGION}\nuse powershell\non-error continue\nforeach host in *\nscript powershell until BLOCK\n" + body + "\nBLOCK\nend\n"
			sess.readLoop(strings.NewReader(script), false)
			if len(sess.targets) != 1 || sess.targets[0] != "vm-b" {
				t.Fatalf("foreach changed caller target: %v", sess.targets)
			}
			for _, stream := range streams {
				if len(stream.results) != 1 {
					t.Fatalf("%s results=%d", stream.device, len(stream.results))
				}
				st := stream.results[0]
				if badCommand {
					if st.Phase != pb.ActionPhase_ACTION_PHASE_FAILED || st.ExitCode == 0 || strings.Contains(st.Detail, "SHOULD_NOT_RUN") {
						t.Fatalf("error reported incorrectly: %+v", st)
					}
				} else if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS || !strings.Contains(st.Detail, "host="+stream.device+", region=apac, env=apac") {
					t.Fatalf("wrong per-host output: %+v", st)
				}
			}
		})
	}
}

func TestNSHWorkingDirectoryPersistsPerDevice(t *testing.T) {
	for _, shell := range []string{"cmd", "powershell"} {
		t.Run(shell, func(t *testing.T) {
			srv := &server{byID: make(map[string]*deviceRecord), liveAgents: make(map[string]*liveAgent), pending: make(map[string]chan *pb.ActionStatus)}
			for _, id := range []string{"vm-a", "vm-b"} {
				srv.byID[id] = &deviceRecord{DeviceID: id, Hostname: id}
				srv.liveAgents[id] = &liveAgent{deviceID: id, stream: &scriptExecutionStream{server: srv, device: id}}
			}
			dir := filepath.Join(t.TempDir(), "folder with spaces and ' quote")
			child := filepath.Join(dir, "child")
			if err := os.MkdirAll(child, 0755); err != nil {
				t.Fatal(err)
			}
			sess := &nshSession{srv: srv, timeout: 20, shell: shell, targets: []string{"vm-a"}}
			sess.engine = newScriptEngine(sess)
			sess.verbCD(`"` + dir + `"`)
			if !strings.EqualFold(sess.workingDirs["vm-a"], dir) {
				t.Fatalf("directory=%q want=%q", sess.workingDirs["vm-a"], dir)
			}
			sess.verbCD("child")
			if !strings.EqualFold(sess.workingDirs["vm-a"], child) {
				t.Fatalf("relative directory=%q", sess.workingDirs["vm-a"])
			}
			sess.verbCD("missing-directory")
			if sess.workingDirs["vm-a"] != child || sess.engine.vars["exit"] == "0" {
				t.Fatalf("failed cd changed directory or hid error: %v", sess.workingDirs)
			}
			sess.verbCD(`\\vm-b`)
			if sess.workingDirs["vm-b"] != "" {
				t.Fatal("CWD leaked across devices")
			}
			sess.verbCD(`\\vm-a`)
			sess.changeWorkingDir("")
			if !strings.EqualFold(sess.workingDirs["vm-a"], child) {
				t.Fatalf("CWD not restored for vm-a: %q", sess.workingDirs["vm-a"])
			}
			sess.verbCD(`\\`)
			if len(sess.targets) != 0 {
				t.Fatal("cd \\\\ did not clear targets")
			}
		})
	}
}

func TestSharedJobRunnerExecutesNativePowerShellPerTarget(t *testing.T) {
	srv := &server{byID: make(map[string]*deviceRecord), liveAgents: make(map[string]*liveAgent), pending: make(map[string]chan *pb.ActionStatus)}
	for _, id := range []string{"vm-a", "vm-b"} {
		srv.byID[id] = &deviceRecord{DeviceID: id, Hostname: id}
		srv.liveAgents[id] = &liveAgent{deviceID: id, stream: &scriptExecutionStream{server: srv, device: id}}
	}
	script := "use powershell\nforeach host in *\nscript powershell until END\nWrite-Output 'target=${host}, tag=${TAG}'\nWrite-Output $env:AEGIS_PARAM_TAG\nEND\nend\n"
	result := newMockNSHRunner(srv).Run(context.Background(), nsh.Job{ID: "native-job", Kind: "software_distribution", Targets: []string{"vm-a", "vm-b"}, Parameters: map[string]string{"TAG": "job-native"}, Script: script})
	if result.Status != "success" || result.TotalActions != 2 {
		t.Fatalf("%+v", result)
	}
	for _, id := range []string{"vm-a", "vm-b"} {
		if !strings.Contains(result.Output, "target="+id+", tag=job-native") {
			t.Fatalf("missing native target output: %s", result.Output)
		}
	}
}
