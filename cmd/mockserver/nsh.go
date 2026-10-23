package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// ===== Live-agent registry (used by REPL to target by host / id) =====

func (s *server) registerLiveAgent(deviceID string, stream pb.AgentControlPlane_ConnectServer) {
	s.liveMu.Lock()
	s.liveAgents[deviceID] = &liveAgent{deviceID: deviceID, stream: stream, since: time.Now()}
	s.liveMu.Unlock()
}

func (s *server) deregisterLiveAgent(deviceID string) {
	s.liveMu.Lock()
	delete(s.liveAgents, deviceID)
	s.liveMu.Unlock()
}

// ===== ActionStatus correlation (REPL waits on terminal phase for a cmd id) =====

type nshUploadTarget struct {
	deviceID string
	path     string
}

func (s *server) waitFor(commandID string) <-chan *pb.ActionStatus {
	ch := make(chan *pb.ActionStatus, 4)
	s.pendingMu.Lock()
	s.pending[commandID] = ch
	s.pendingMu.Unlock()
	return ch
}

func (s *server) cancelWait(commandID string) {
	s.pendingMu.Lock()
	delete(s.nshUploads, commandID)
	if ch, ok := s.pending[commandID]; ok {
		delete(s.pending, commandID)
		close(ch)
	}
	s.pendingMu.Unlock()
}

// deliverActionStatus pushes a status to any REPL waiter; returns true if
// consumed (so the regular print loop skips it, keeping REPL output clean).
func (s *server) deliverActionStatus(st *pb.ActionStatus) bool {
	s.pendingMu.Lock()
	ch, ok := s.pending[st.ActionId]
	if !ok {
		s.pendingMu.Unlock()
		return false
	}
	// Non-blocking send; if the REPL is slow / gone, drop and clean up.
	select {
	case ch <- st:
	default:
	}
	// On a terminal phase, close + remove so the REPL goroutine wakes up.
	if st.Phase == pb.ActionPhase_ACTION_PHASE_SUCCESS ||
		st.Phase == pb.ActionPhase_ACTION_PHASE_FAILED ||
		st.Phase == pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE {
		delete(s.pending, st.ActionId)
		delete(s.nshUploads, st.ActionId)
		close(ch)
	}
	s.pendingMu.Unlock()
	return true
}

// ===== REPL entrypoint =====

// nshSession is the REPL state (per-process). The console (future web UI) will
// keep this per logged-in operator; the mock has one.
type nshSession struct {
	srv        *server
	targets    []string // device_ids resolved from `cd \\host`
	workingDir string   // endpoint CWD for subsequent run_commands ("" = SKVC\tmp)
	shell      string   // cmd | powershell | pwsh (nothing on this OS == default)
	timeout    int32    // seconds
	maxOutput  int64    // bytes
	capture    bool     // auto-save every command's stdout as a server-side file
	outputDir  string   // default upload path prefix for grab / capture
	history    []string
	out        *bufio.Writer
	engine     *scriptEngine
}

// runNSH starts the interactive REPL on stdin (or non-interactive from a
// script file when AEGIS_NSH_SCRIPT is set). Runs in a goroutine alongside the
// gRPC server — not a separate process (console will embed the same engine).
func runNSH(srv *server) {
	sess := &nshSession{
		srv:       srv,
		shell:     "cmd",
		timeout:   300,
		maxOutput: 1 << 20,
		capture:   false,
		outputDir: "nsh",
		out:       bufio.NewWriter(os.Stdout),
	}
	sess.engine = newScriptEngine(sess)
	sess.printBanner()
	// Non-interactive script mode (same engine, no prompt).
	if scriptPath := strings.TrimSpace(os.Getenv("AEGIS_NSH_SCRIPT")); scriptPath != "" {
		f, err := os.Open(scriptPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "nsh: open script %q: %v\n", scriptPath, err)
			return
		}
		defer f.Close()
		sess.capture = true
		sess.readLoop(f, false)
		return
	}
	sess.readLoop(os.Stdin, true)
}

func (s *nshSession) printBanner() {
	fmt.Println("")
	fmt.Println("Aegis One NSH v1 — console operator REPL")
	fmt.Println("  type `help` for verbs, `hosts` to list devices, `exit` to quit")
	fmt.Println("")
}

func (s *nshSession) prompt() string {
	// Inside an open for/foreach/if block → continuation prompt.
	if s.engine != nil && len(s.engine.blocks) > 0 {
		dots := strings.Repeat("..", len(s.engine.blocks))
		return dots + "> "
	}
	tag := "nsh"
	if len(s.targets) == 1 {
		tag = s.srv.hostnameOf(s.targets[0])
	} else if len(s.targets) > 1 {
		tag = fmt.Sprintf("[%d hosts]", len(s.targets))
	}
	return tag + "> "
}

func (s *nshSession) readLoop(src io.Reader, interactive bool) {
	br := bufio.NewReader(src)
	for {
		if interactive {
			fmt.Fprint(os.Stdout, s.prompt())
		}
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err != nil {
				return
			}
			continue
		}
		if !interactive {
			fmt.Printf("# %s\n", line)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		s.history = append(s.history, line)
		if cont := s.engine.execLine(line); !cont {
			return
		}
		if err != nil {
			return
		}
	}
}

// exec (legacy direct path) is kept for callers that bypass the engine during
// a block replay. Routes to the verb, returns false on `exit`.
func (s *nshSession) exec(line string) bool {
	line = strings.TrimSpace(line)
	verb, rest := splitFirst(line)
	switch strings.ToLower(verb) {
	case "exit", "quit", "bye":
		return false
	case "help":
		s.verbHelp(rest)
	case "hosts":
		s.verbHosts(rest)
	case "cd":
		s.verbCD(rest)
	case "run":
		s.verbRun(rest, false)
	case "grab":
		s.verbGrab(rest)
	case "pull":
		s.verbPull(rest)
	case "fanout":
		s.verbRun(rest, true)
	case "shell":
		s.verbShell()
	case "use":
		s.verbUse(rest)
	case "set":
		s.verbSet(rest)
	case "history":
		for i, h := range s.history {
			fmt.Printf("  %3d  %s\n", i+1, h)
		}
	case "script":
		s.verbScript(rest)
	default:
		// Bare command text → `run` on current target(s). Keeps the REPL feel.
		s.verbRun(line, len(s.targets) > 1)
	}
	return true
}

// ===== Verbs =====

func (s *nshSession) verbHelp(_ string) {
	fmt.Print(`
TARGETING
  hosts [filter]            list online devices (host=sub, id=sub, cid=sub)
  cd \\<host>               set single target (hostname / device_id / computer_id)
  cd \\<h1>,<h2>            set multiple targets
  cd *                      target all online
  cd                        clear target

EXECUTION
  <cmd> | run <cmd>         run on current target(s) (bare line = run)
  grab [path] <cmd>         run + save stdout as server-side file
  pull <remote> [path]      fetch an endpoint file verbatim
  fanout <cmd>              explicit multi-target run, aggregated output
  shell                     persistent interactive session (open_shell plumbing)

CONTROL FLOW (scripts + REPL)
  let NAME = value          define variable (supports ${name}, ${env:X})
  ${VAR} | $VAR             substitute a variable in any arg
  foreach host in *|csv ... end   iterate hosts (binds $host, implicit cd)
  for VAR in a b c ... end        plain loop
  if <cond> ... [else] ... end    cond: $exit == 0, $exit != 0,
                                        stdout contains "x", defined NAME
  break [N]                 exit N enclosing loops (default 1)
  continue                  next iteration
  on-error continue|stop    script error policy (default stop)
  log <message>             print + session-log marker
  alias name = <expansion>  reusable command

SESSION
  use cmd|powershell|pwsh|bash|sh|zsh   interpreter
  set timeout <sec>         per-command timeout (default 300)
  set max-output <bytes>    inline capture cap (default 1 MiB)
  set capture on|off        auto-save every command's stdout
  set output-dir <path>     default server-side save prefix (default "nsh")
  set format table|json|csv fanout output format
  set session-log <path>    append full REPL to a file (off to disable)
  script run <file>         non-interactive batch (capture=on by default)
  source <file>             include another .nsh (vars/aliases)
  exec [shell] <file>       read a local .ps1/.bat/.cmd/.py/.sh, substitute
                            ${vars}, send to current target(s) as one command
                            (BigFix "action script with embedded relevance"
                            equivalent — infers shell from extension)
  script <shell> until <DELIM>   HEREDOC: inline multi-line script until
     <lines>                     the DELIM line. Substitutes ${vars} and
     <DELIM>                     ships as one encoded command (no file is
                                 created on the endpoint). BigFix
                                 createfile-until ergonomic without the file.
                                 Shells: cmd | powershell | pwsh | python |
                                 bash | sh | zsh
  wait <sec>                sleep in script
  vars                      show defined variables
  history                   recent commands
  exit                      quit

LAST-RESULT VARS (after each command, usable in if-conditions / substitution)
  $exit  $stdout  $stderr  $host  $duration  $reason
`)
}

func (s *nshSession) verbHosts(filter string) {
	s.srv.liveMu.Lock()
	ids := make([]string, 0, len(s.srv.liveAgents))
	for id := range s.srv.liveAgents {
		ids = append(ids, id)
	}
	s.srv.liveMu.Unlock()
	sort.Strings(ids)

	fmt.Printf("%-18s  %-24s  %s\n", "HOST", "DEVICE_ID", "COMPUTER_ID")
	for _, id := range ids {
		host := s.srv.hostnameOf(id)
		cid := s.srv.computerIDOf(id)
		if filter != "" && !matchFilter(host, id, cid, filter) {
			continue
		}
		fmt.Printf("%-18s  %-24s  %s\n", host, id, cid)
	}
}

func (s *nshSession) verbCD(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		s.targets = nil
		return
	}
	if rest == "*" {
		s.srv.liveMu.Lock()
		s.targets = nil
		for id := range s.srv.liveAgents {
			s.targets = append(s.targets, id)
		}
		s.srv.liveMu.Unlock()
		fmt.Printf("targets: %d online host(s)\n", len(s.targets))
		return
	}
	rest = strings.TrimPrefix(rest, `\\`)
	parts := strings.Split(rest, ",")
	var resolved []string
	for _, p := range parts {
		p = strings.TrimSpace(strings.TrimPrefix(p, `\\`))
		if p == "" {
			continue
		}
		id := s.srv.resolveTarget(p)
		if id == "" {
			fmt.Printf("nsh: no such online host: %s\n", p)
			continue
		}
		resolved = append(resolved, id)
	}
	s.targets = resolved
	if len(resolved) == 0 {
		fmt.Println("nsh: no targets selected")
	}
}

func (s *nshSession) verbRun(cmdText string, fan bool) {
	cmdText = strings.TrimSpace(cmdText)
	if cmdText == "" {
		fmt.Println("nsh: nothing to run")
		return
	}
	if len(s.targets) == 0 {
		fmt.Println("nsh: no target — pick one with `cd \\\\<host>`")
		return
	}
	if !fan && len(s.targets) > 1 {
		fmt.Printf("nsh: %d targets selected; use `fanout <cmd>` for multi-target\n", len(s.targets))
		return
	}
	// Capture mode: each target gets its OWN per-host path so a fan-out never
	// collides into one file. Single-target still gets a host-folder path so
	// scripts can always find "which host saved what".
	s.dispatchAndRender(cmdText, "", fan)
}

func (s *nshSession) verbGrab(rest string) {
	path, cmdText := splitFirst(rest)
	if cmdText == "" {
		// User passed only a command, no server path — auto-name.
		cmdText = path
		path = ""
	}
	cmdText = strings.TrimSpace(cmdText)
	if cmdText == "" {
		fmt.Println("nsh: usage: grab [server-path] <command>")
		return
	}
	if len(s.targets) == 0 {
		fmt.Println("nsh: no target — pick one with `cd \\\\<host>`")
		return
	}
	for _, id := range s.targets {
		upload := path
		if upload == "" {
			upload = s.autoCapturePath(cmdText, s.srv.hostnameOf(id))
		} else {
			upload = s.namedUploadPath(id, path)
		}
		s.dispatchOne(id, cmdText, upload)
	}
}

func (s *nshSession) verbPull(rest string) {
	remote, savePath := splitPullArgs(rest)
	if remote == "" {
		fmt.Println("nsh: usage: pull <remote-path> [server-path]")
		return
	}
	if len(s.targets) == 0 {
		fmt.Println("nsh: no target — pick one with cd \\\\<host>")
		return
	}
	for _, id := range s.targets {
		up := savePath
		if up == "" {
			up = time.Now().Format("20060102-150405.000000000") + "-" + filepath.Base(strings.ReplaceAll(remote, "\\", "/"))
		}
		up = s.namedUploadPath(id, up)
		fmt.Printf("---- %s pull %s\n", s.srv.hostnameOf(id), remote)
		st, _ := s.sendTask(id, "collect_file", &pb.CollectFileSpec{Paths: []string{remote}, MaxBytesEach: 1<<63 - 1, MaxTotalBytes: 1<<63 - 1}, up)
		if st == nil {
			continue
		}
		if st.Detail != "" {
			fmt.Println(st.Detail)
		}
		fmt.Printf("---- %s %s: %s\n", s.srv.hostnameOf(id), phaseWord(st.Phase), st.Reason)
		if s.engine != nil {
			exit := 0
			if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
				exit = 1
			}
			s.engine.recordLastResult(s.srv.hostnameOf(id), st.Detail, "", st.Reason, exit, 0)
		}
	}
}

// Quoted endpoint paths may contain spaces. Wildcards are deliberately refused
// by pull: one requested destination corresponds to exactly one source file.
func splitPullArgs(rest string) (string, string) {
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, `"`) {
		if end := strings.Index(rest[1:], `"`); end >= 0 {
			p := rest[1 : end+1]
			if strings.ContainsAny(p, "*?[") {
				return "", ""
			}
			return p, strings.Trim(strings.TrimSpace(rest[end+2:]), `"`)
		}
		return "", ""
	}
	p, name := splitFirst(rest)
	if strings.ContainsAny(p, "*?[") {
		return "", ""
	}
	return p, strings.Trim(name, `"`)
}

func (s *nshSession) namedUploadPath(id, name string) string {
	return strings.TrimRight(s.outputDir, "/\\") + "/" + slugify(s.srv.hostnameOf(id)) + "/" + strings.TrimLeft(name, "/\\")
}

func (s *nshSession) verbShell() {
	if len(s.targets) != 1 {
		fmt.Println("nsh: `shell` needs exactly ONE target — use `cd \\\\<host>` first")
		return
	}
	fmt.Println("nsh: `shell` opens a persistent interactive session — run `mockserver.exe` with AEGIS_SHELL (legacy) or wait for the console UI to wire the terminal.")
	fmt.Println("     (v1 interactive shell exists on the agent as open_shell/ShellSession; the console UI will carry stdin/stdout to a web terminal.)")
}

func (s *nshSession) verbUse(rest string) {
	rest = strings.ToLower(strings.TrimSpace(rest))
	switch rest {
	case "cmd", "powershell", "ps", "pwsh", "bash", "sh", "zsh":
		s.shell = rest
		fmt.Printf("shell = %s\n", rest)
	default:
		fmt.Println("nsh: use <cmd|powershell|pwsh|bash|sh|zsh>")
	}
}

func (s *nshSession) verbSet(rest string) {
	k, v := splitFirst(rest)
	v = strings.TrimSpace(v)
	switch strings.ToLower(k) {
	case "timeout":
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.timeout = int32(n)
			fmt.Printf("timeout = %ds\n", n)
		}
	case "max-output":
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			s.maxOutput = n
			fmt.Printf("max-output = %d bytes\n", n)
		}
	case "capture":
		s.capture = v == "on" || v == "true" || v == "1"
		fmt.Printf("capture = %v\n", s.capture)
	case "output-dir":
		if v != "" {
			s.outputDir = v
			fmt.Printf("output-dir = %s\n", v)
		}
	default:
		fmt.Println("nsh: set <timeout|max-output|capture|output-dir> <value>")
	}
}

func (s *nshSession) verbScript(rest string) {
	sub, path := splitFirst(rest)
	if strings.ToLower(sub) != "run" || path == "" {
		fmt.Println("nsh: usage: script run <file>")
		return
	}
	f, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		fmt.Printf("nsh: open %s: %v\n", path, err)
		return
	}
	defer f.Close()
	saved := s.capture
	s.capture = true
	s.readLoop(f, false)
	s.capture = saved
}

// ===== Dispatch =====

// dispatchAndRender fans a command out across targets (or sends to the one
// selected target), waits for each terminal ActionStatus, and prints. Each
// target gets its OWN per-host upload path when capture is on so a multi-host
// run never collides into one file.
func (s *nshSession) dispatchAndRender(cmdText, forcedUpload string, fan bool) {
	pathFor := func(deviceID string) string {
		if forcedUpload != "" {
			return forcedUpload // explicit grab/pull path wins
		}
		if !s.capture {
			return ""
		}
		return s.autoCapturePath(cmdText, s.srv.hostnameOf(deviceID))
	}
	if !fan && len(s.targets) == 1 {
		s.dispatchOne(s.targets[0], cmdText, pathFor(s.targets[0]))
		return
	}
	type row struct {
		host, status, snippet, saved string
	}
	var mu sync.Mutex
	var rows []row
	var wg sync.WaitGroup
	for _, id := range s.targets {
		id := id
		up := pathFor(id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := s.dispatchQuiet(id, cmdText, up)
			mu.Lock()
			rows = append(rows, row{host: s.srv.hostnameOf(id), status: res.status, snippet: res.snippet, saved: res.saved})
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(rows, func(i, j int) bool { return rows[i].host < rows[j].host })
	fmt.Printf("%-18s  %-10s  %-60s  %s\n", "HOST", "RESULT", "FIRST LINE", "SAVED")
	for _, r := range rows {
		fmt.Printf("%-18s  %-10s  %-60s  %s\n", r.host, r.status, r.snippet, r.saved)
	}
}

type oneResult struct {
	status  string
	snippet string
	saved   string
}

func (s *nshSession) dispatchQuiet(id, cmdText, uploadAs string) oneResult {
	st, _ := s.sendCommand(id, cmdText, uploadAs)
	if st == nil {
		return oneResult{status: "no-reply"}
	}
	saved := parseUploaded(st.Detail)
	snippet := firstLine(parseStdout(st.Detail))
	switch st.Phase {
	case pb.ActionPhase_ACTION_PHASE_SUCCESS:
		return oneResult{status: "ok (0)", snippet: snippet, saved: saved}
	case pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE:
		return oneResult{status: "n/a", snippet: st.Reason, saved: saved}
	default:
		return oneResult{status: fmt.Sprintf("fail (%d)", st.ExitCode), snippet: st.Reason, saved: saved}
	}
}

func (s *nshSession) dispatchOne(id, cmdText, uploadAs string) {
	host := s.srv.hostnameOf(id)
	fmt.Printf("---- %s $ %s\n", host, cmdText)
	st, cmdID := s.sendCommand(id, cmdText, uploadAs)
	if st == nil {
		fmt.Printf("     (no reply, action_id=%s)\n", cmdID)
		if s.engine != nil {
			s.engine.recordLastResult(host, "", "", "no reply", -1, 0)
		}
		return
	}
	stdout := parseStdout(st.Detail)
	stderr := parseStderr(st.Detail)
	if s.engine != nil {
		s.engine.recordLastResult(host, stdout, stderr, st.Reason, int(st.ExitCode), parseDuration(st.Detail))
	}
	if stdout != "" {
		fmt.Print(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			fmt.Println()
		}
	}
	if stderr != "" {
		fmt.Fprint(os.Stderr, stderr)
		if !strings.HasSuffix(stderr, "\n") {
			fmt.Println()
		}
	}
	if up := parseUploaded(st.Detail); up != "" {
		fmt.Printf("---- saved to %s\n", up)
	}
	fmt.Printf("---- %s %s  (exit=%d, %dms)\n", host, phaseWord(st.Phase), st.ExitCode, parseDuration(st.Detail))
	if st.Reason != "" {
		fmt.Printf("     reason: %s\n", st.Reason)
	}
}

func (s *nshSession) sendCommand(deviceID, cmdText, uploadAs string) (*pb.ActionStatus, string) {
	// Forward NSH variables to the agent as command parameters: the agent
	// (a) runs ${name} substitution on the command text (idempotent — the REPL
	// already substituted known names), and (b) exports every param to the
	// child process env as AEGIS_PARAM_<NAME> so a pre-staged .ps1/.bat/.py on
	// the endpoint can read them (BigFix "parameters" pattern, softdist-shared).
	var params map[string]string
	if s.engine != nil && len(s.engine.vars) > 0 {
		params = make(map[string]string, len(s.engine.vars))
		for k, v := range s.engine.vars {
			params[k] = v
		}
	}
	spec := &pb.RunCommandSpec{
		Command:        cmdText,
		Shell:          s.shell,
		TimeoutSeconds: s.timeout,
		MaxOutputBytes: s.maxOutput,
		UploadAs:       uploadAs,
		Parameters:     params,
		WorkingDir:     s.workingDir,
	}
	return s.sendTask(deviceID, "run_command", spec, uploadAs)
}

func (s *nshSession) sendTask(deviceID, taskType string, spec proto.Message, uploadAs string) (*pb.ActionStatus, string) {
	s.srv.liveMu.Lock()
	la, ok := s.srv.liveAgents[deviceID]
	s.srv.liveMu.Unlock()
	if !ok {
		fmt.Printf("nsh: %s is not currently connected\n", s.srv.hostnameOf(deviceID))
		return nil, ""
	}
	if uploadAs != "" {
		if _, err := safeNSHUploadPath(uploadAs); err != nil {
			fmt.Printf("nsh: %v\n", err)
			return nil, ""
		}
	}
	payload, err := proto.Marshal(spec)
	if err != nil {
		fmt.Printf("nsh: marshal: %v\n", err)
		return nil, ""
	}
	cmdID := "nsh-" + nshRandomID()
	ch := s.srv.waitFor(cmdID)
	if uploadAs != "" {
		s.srv.pendingMu.Lock()
		if s.srv.nshUploads == nil {
			s.srv.nshUploads = make(map[string]nshUploadTarget)
		}
		s.srv.nshUploads[cmdID] = nshUploadTarget{deviceID: deviceID, path: uploadAs}
		s.srv.pendingMu.Unlock()
	}
	msg := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
		CommandId: cmdID, Type: taskType, Payload: payload,
	})}}
	if err := la.stream.Send(msg); err != nil {
		s.srv.cancelWait(cmdID)
		fmt.Printf("nsh: send to %s failed: %v\n", s.srv.hostnameOf(deviceID), err)
		return nil, cmdID
	}
	timeout := time.Duration(s.timeout+10) * time.Second // small grace over per-command timeout
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var terminal *pb.ActionStatus
	for {
		select {
		case st, open := <-ch:
			if !open {
				return terminal, cmdID
			}
			if st.Phase == pb.ActionPhase_ACTION_PHASE_SUCCESS ||
				st.Phase == pb.ActionPhase_ACTION_PHASE_FAILED ||
				st.Phase == pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE {
				terminal = st
			}
		case <-deadline.C:
			s.srv.cancelWait(cmdID)
			fmt.Printf("nsh: timed out waiting for %s\n", cmdID)
			return terminal, cmdID
		}
	}
}

// ===== Helpers =====

func (s *nshSession) autoCapturePath(cmdText, host string) string {
	ts := time.Now().Format("20060102-150405.000000000")
	slug := slugify(cmdText)
	if host == "" {
		return fmt.Sprintf("%s/%s-%s.out", s.outputDir, ts, slug)
	}
	return fmt.Sprintf("%s/%s/%s-%s.out", s.outputDir, slugify(host), ts, slug)
}

func safeNSHUploadPath(name string) (string, error) {
	name = strings.TrimLeft(strings.ReplaceAll(name, "\\", "/"), "/")
	parts := strings.Split(name, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "<>:\"|?*\x00") || strings.TrimRight(part, " .") != part {
			return "", fmt.Errorf("invalid upload path %q", name)
		}
	}
	return filepath.Join(parts...), nil
}

// Exclusive creation preserves earlier reports even when an explicit grab name
// is reused or several hosts/operators submit concurrently.
func createUploadFile(dir, name string) (*os.File, string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; ; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		path := filepath.Join(dir, candidate)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		return f, path, err
	}
}

var slugRE = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func slugify(s string) string {
	if len(s) > 32 {
		s = s[:32]
	}
	return strings.Trim(slugRE.ReplaceAllString(s, "_"), "_")
}

func splitFirst(s string) (first, rest string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

func matchFilter(host, id, cid, filter string) bool {
	f := strings.ToLower(filter)
	if strings.HasPrefix(f, "host=") {
		return strings.Contains(strings.ToLower(host), f[5:])
	}
	if strings.HasPrefix(f, "id=") {
		return strings.Contains(strings.ToLower(id), f[3:])
	}
	if strings.HasPrefix(f, "cid=") {
		return strings.Contains(cid, f[4:])
	}
	// Default substring match across all three.
	return strings.Contains(strings.ToLower(host+" "+id+" "+cid), f)
}

// Server helpers used by the REPL — small lookups into the device registry.

func (s *server) hostnameOf(deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[deviceID]; r != nil && r.Hostname != "" {
		return r.Hostname
	}
	return deviceID
}

func (s *server) computerIDOf(deviceID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.byID[deviceID]; r != nil {
		return r.ComputerID
	}
	return ""
}

// resolveTarget takes a user-typed token (hostname, device_id prefix, computer_id)
// and returns the matching ONLINE device_id, or "".
func (s *server) resolveTarget(token string) string {
	token = strings.TrimSpace(token)
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	for id := range s.liveAgents {
		if strings.EqualFold(id, token) || strings.HasPrefix(id, token) {
			return id
		}
		host := s.hostnameOf(id)
		if strings.EqualFold(host, token) {
			return id
		}
		if s.computerIDOf(id) == token {
			return id
		}
	}
	return ""
}

func nshRandomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func phaseWord(p pb.ActionPhase) string {
	switch p {
	case pb.ActionPhase_ACTION_PHASE_SUCCESS:
		return "OK"
	case pb.ActionPhase_ACTION_PHASE_FAILED:
		return "FAIL"
	case pb.ActionPhase_ACTION_PHASE_NOT_APPLICABLE:
		return "N/A"
	default:
		return p.String()
	}
}

func parseStdout(detail string) string { return parseJSONField(detail, "stdout") }
func parseStderr(detail string) string { return parseJSONField(detail, "stderr") }
func parseUploaded(detail string) string {
	return parseJSONField(detail, "uploaded_path")
}
func parseDuration(detail string) int64 {
	if v := parseJSONField(detail, "duration_ms"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return 0
}

func parseJSONField(detail, field string) string {
	if detail == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(detail), &m); err != nil {
		return ""
	}
	v, ok := m[field]
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimRight(s[:i], " \r\t")
	}
	return strings.TrimRight(s, " \r\t")
}
