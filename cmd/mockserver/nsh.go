package main

import (
	"bufio"
	"context"
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

	"github.com/peterh/liner"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/pkg/nsh"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/protobuf/proto"
)

// ===== Live-agent registry (used by REPL to target by host / id) =====

func (s *server) registerLiveAgent(deviceID string, stream pb.AgentControlPlane_ConnectServer) *liveAgent {
	s.liveMu.Lock()
	agent := &liveAgent{deviceID: deviceID, stream: stream, since: time.Now()}
	s.liveAgents[deviceID] = agent
	s.liveMu.Unlock()
	return agent
}

func (a *liveAgent) send(msg *pb.ServerMessage) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	return a.stream.Send(msg)
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
// nshActor returns the attribution string identifying who is dispatching
// a command from THIS NSH session. Format: "nsh:<os-user>@<host>" so an
// agent log line pinpoints exactly which operator (and which workstation
// the mock REPL was running on) initiated a command.
func (s *nshSession) nshActor() string {
	if s.cachedActor != "" {
		return s.cachedActor
	}
	user := os.Getenv("USERNAME")
	if user == "" {
		user = os.Getenv("USER")
	}
	if user == "" {
		user = "unknown"
	}
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "mockserver"
	}
	s.cachedActor = "nsh:" + user + "@" + host
	return s.cachedActor
}

type nshSession struct {
	srv                 *server
	// cachedActor stores the computed "nsh:<user>@<host>" attribution so
	// every signed dispatch does not re-stat the OS env. Set lazily on
	// first nshActor() call.
	cachedActor         string
	targets             []string          // device_ids resolved from `cd \\host`
	workingDirs         map[string]string // endpoint CWD per device (unset = SKVC\tmp)
	fileWorkers         int
	fileMaxBytes        int64
	fileMaxFiles        int
	shell               string // cmd | powershell | pwsh (nothing on this OS == default)
	timeout             int32  // seconds
	maxOutput           int64  // bytes
	capture             bool   // auto-save every command's stdout as a server-side file
	outputDir           string // default upload path prefix for grab / capture
	history             []string
	out                 *bufio.Writer
	engine              *scriptEngine
	ctx                 context.Context
	output, errorOutput io.Writer
	parameters          map[string]string
	emit                func(nsh.ActionResult)
	allowedTargets      map[string]bool
	jobMode             bool
}

// runNSH starts the interactive REPL on stdin (or non-interactive from a
// script file explicitly selected by the startup mode). Runs alongside the
// gRPC server — not a separate process (console will embed the same engine).
func runNSH(srv *server, scriptPath string) {
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
	defer sess.engine.core.Close()
	// Non-interactive script mode (same engine, no prompt).
	if scriptPath != "" {
		runEnvironmentNSHJob(srv, scriptPath)
		return
	}
	sess.printBanner()
	sess.readLoop(os.Stdin, true)
}

func (s *nshSession) printBanner() {
	s.println("")
	s.println("Aegis One NSH v1 — console operator REPL")
	s.println("  type `help` for verbs, `hosts` to list devices, `exit` to quit")
	s.println("")
}

func (s *nshSession) prompt() string {
	if s.engine != nil && s.engine.core.State().InHeredoc {
		return "... [" + s.engine.core.State().Delimiter + "]> "
	}
	// Inside an open for/foreach/if block → continuation prompt.
	if s.engine != nil && s.engine.core.State().BlockDepth > 0 {
		dots := strings.Repeat("..", s.engine.core.State().BlockDepth)
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
	// Interactive mode uses peterh/liner for Tab completion + line editing.
	// Tab on a prefix completes the verb if there is exactly one match, or
	// prints the candidate list if there are several (plain prefix echo on
	// zero matches). Non-interactive (script) mode still uses bufio since
	// there is no terminal to configure.
	if interactive && src == os.Stdin {
		s.interactiveLoop()
		return
	}
	br := bufio.NewReader(src)
	for {
		if interactive {
			s.printf("%s", s.prompt())
		}
		line, err := br.ReadString('\n')
		if err != nil && line == "" {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if !interactive {
			s.printf("# %s\n", line)
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

// nshVerbNames lists every top-level verb the REPL dispatcher recognizes, used
// for Tab completion. Keep it alphabetical so the candidate list a user sees
// after `inv<Tab>` is predictable.
var nshVerbNames = []string{
	"cat", "cd", "copy", "cp", "delete", "del", "exec", "exit", "fanout",
	"grab", "help", "history", "hosts", "inventory", "mkdir", "md", "move",
	"mv", "pull", "pwd", "quit", "rm", "run", "script", "search", "set",
	"shell", "source", "touch", "use",
}

// interactiveLoop drives the Tab-completing readline on the mockserver's stdin.
// The completer receives the full line prefix and returns every verb whose name
// starts with the first token the user typed. Multiple matches are shown as a
// candidate list under the prompt (liner prints them automatically). History
// is kept per-process (not persisted) — a REPL session's history dies with
// the process, matching the mock's dev-tool scope.
func (s *nshSession) interactiveLoop() {
	line := liner.NewLiner()
	defer line.Close()
	line.SetCtrlCAborts(true)
	line.SetTabCompletionStyle(liner.TabPrints)
	line.SetCompleter(func(prefix string) []string {
		// Decide what the user is completing: a verb (first token) or a
		// path argument to a verb that takes paths. For path args we
		// dispatch a list_directory to the current cd target (if any) and
		// return matching entries; the result is cached briefly so rapid
		// Tabs do not spam the agent. Verb completion stays as-is.
		head := prefix
		rest := ""
		if i := strings.IndexAny(prefix, " \t"); i >= 0 {
			head = prefix[:i]
			rest = prefix[i+1:]
		}
		if head == "" || rest == "" {
			// Verb completion branch.
			if head == "" {
				return nshVerbNames
			}
			lower := strings.ToLower(head)
			var out []string
			for _, v := range nshVerbNames {
				if strings.HasPrefix(v, lower) {
					out = append(out, v+prefix[len(head):])
				}
			}
			return out
		}
		if !verbTakesPath(head) {
			return nil
		}
		return s.completePathArg(prefix, head, rest)
	})
	for _, h := range s.history {
		line.AppendHistory(h)
	}
	for {
		input, err := line.Prompt(s.prompt())
		if err == liner.ErrPromptAborted || err == io.EOF {
			return
		}
		if err != nil {
			s.printf("nsh: input error: %v\n", err)
			return
		}
		input = strings.TrimRight(input, "\r\n")
		if strings.TrimSpace(input) != "" {
			line.AppendHistory(input)
			s.history = append(s.history, input)
		}
		if cont := s.engine.execLine(input); !cont {
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
	case "search":
		s.verbHosts(rest)
	case "copy", "cp":
		s.verbFile("copy", rest)
	case "move", "mv":
		s.verbFile("move", rest)
	case "delete", "del", "rm":
		s.verbFile("delete", rest)
	case "mkdir", "md":
		s.verbFile("mkdir", rest)
	case "touch":
		s.verbFile("touch", rest)
	case "cd":
		s.verbCD(rest)
	case "pwd":
		s.changeWorkingDir("")
	case "run":
		s.verbRun(rest, false)
	case "grab":
		s.verbGrab(rest)
	case "pull":
		s.verbPull(rest)
	case "cat":
		s.verbCat(rest)
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
			s.printf("  %3d  %s\n", i+1, h)
		}
	case "script":
		s.verbScript(rest)
	case "inventory":
		s.verbInventory(rest)
	default:
		// Bare command text → `run` on current target(s). Keeps the REPL feel.
		s.verbRun(line, len(s.targets) > 1)
	}
	return true
}

// ===== Verbs =====

func (s *nshSession) verbHelp(_ string) {
	s.print(`
TARGETING
  hosts [filter]            known devices + ONLINE/OFFLINE (20 rows by default)
  search <text>             search hostname/device_id/computer_id
  hosts --status offline --page 2 --limit 20   filter/paginate (max 100 rows)
  inventory apps [filter]   third-party installed-apps from latest DeviceStateReport
  inventory refresh [--timeout N]
                            dev/admin tool: force the current target(s) to send a fresh
                            DeviceStateReport now (parallel dispatch, shared deadline;
                            default 5s; capped at 100 targets — use server-side batch for fleets)
  cat <src> > <dst>         copy src bytes to dst (overwrite); either path can be
  cat <src> >> <dst>        local or UNC-style \\<host>\<drive>\<path>; ≤ 500 KB
                            (append mode for >>). Phase-1 single-shot writer.
  cd \\<host>               set single target (hostname / device_id / computer_id)
  cd \\<h1>,<h2>            set multiple targets
  cd *                      target all online
  cd C:\temp | cd ..        change selected endpoint's working directory
  cd | pwd                  show endpoint working directory
  cd \\                     clear targets (return to nsh prompt)

EXECUTION
  job run <file.nsh> [--param NAME=value] [--kind script_job|software_distribution]
      non-interactive execution with isolated state on selected targets
  copy|cp [-r] [--overwrite] <src> <dest>  native local/cross-host copy
  move|mv [-r] [--overwrite] <src> <dest>  copy-verify-remove (one destination)
  mkdir|md <path> | touch <path> | delete|del|rm [-r] <path>
     Paths: selected-host C:\path or \\host[,host]\c\path; \\* targets online hosts.
     Cross-host paths are NSH device paths, not Windows SMB shares.
     set file-workers 4 | file-max-bytes 524288000 | file-max-files 10000
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
	s.printHosts(filter)
}

// verbInventory answers "inventory apps [filter]" by listing the installed-apps
// snapshot from the most recent DeviceStateReport for the current target. The
// optional filter is a case-insensitive substring match against DisplayName or
// Publisher. With no cd target, the mock summarises counts per connected
// device so the operator can see fleet-wide inventory presence at a glance.
//
// This is a DEV / TESTING convenience. The production catalog match (which app
// is outdated, what version is latest, which fixlet-equivalent to dispatch) is
// server-team work.
func (s *nshSession) verbInventory(rest string) {
	parts := strings.Fields(strings.TrimSpace(rest))
	if len(parts) == 0 {
		s.printf("nsh: inventory apps [filter] | inventory refresh — see help for details\n")
		return
	}
	if strings.EqualFold(parts[0], "refresh") {
		s.refreshInventory(parts[1:])
		return
	}
	if !strings.EqualFold(parts[0], "apps") {
		s.printf("nsh: inventory apps [filter] | inventory refresh — unknown sub-verb %q\n", parts[0])
		return
	}
	filter := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(rest, parts[0])))

	if len(s.targets) == 0 {
		// Fleet summary.
		s.srv.inventoryMu.Lock()
		defer s.srv.inventoryMu.Unlock()
		if len(s.srv.lastInventory) == 0 {
			s.printf("inventory: no DeviceStateReport received yet from any device\n")
			return
		}
		s.printf("%-20s  %-16s  %s\n", "HOST", "INVENTORY_SIZE", "LAST_REPORT_AT")
		for devID, rep := range s.srv.lastInventory {
			host := s.srv.hostnameOf(devID)
			s.printf("%-20s  %-16d  %d\n", host, len(rep.InstalledApps), rep.CollectedAt)
		}
		return
	}

	for _, devID := range s.targets {
		host := s.srv.hostnameOf(devID)
		s.srv.inventoryMu.Lock()
		rep, ok := s.srv.lastInventory[devID]
		s.srv.inventoryMu.Unlock()
		if !ok {
			s.printf("%s: no DeviceStateReport received yet (wait for AEGIS_DEVICE_STATE_INTERVAL or trigger a change)\n", host)
			continue
		}
		apps := rep.InstalledApps
		if filter != "" {
			var kept []*pb.InstalledApp
			for _, a := range apps {
				if strings.Contains(strings.ToLower(a.DisplayName), filter) ||
					strings.Contains(strings.ToLower(a.Publisher), filter) {
					kept = append(kept, a)
				}
			}
			apps = kept
		}
		s.printf("---- %s: %d installed app(s)%s ----\n", host, len(apps), filterTag(filter))
		s.printf("%-45s  %-20s  %-30s  %s\n", "DISPLAY_NAME", "VERSION", "PUBLISHER", "HIVE")
		for _, a := range apps {
			s.printf("%-45s  %-20s  %-30s  %s\n",
				truncate(a.DisplayName, 45),
				truncate(a.DisplayVersion, 20),
				truncate(a.Publisher, 30),
				a.Hive)
		}
	}
}

// refreshInventory sends a signed "report_device_state" command in PARALLEL
// to every current target and waits for a FRESH DeviceStateReport
// (CollectedAt newer than the one the mock already has, if any) to land in the
// per-device cache. This closes the "inventory apps shows no data when nothing
// has changed" UX gap: the agent sends on-change by default; this verb forces
// a one-shot send regardless of fingerprint state.
//
// Timeout is SHARED across the whole fleet, not per-target — so a cd with 50
// targets completes in <timeout> seconds total, not 50 × <timeout>. Default
// timeout is 5s; override with "inventory refresh --timeout <seconds>" for
// cross-WAN fleets or slow agents.
func (s *nshSession) refreshInventory(args []string) {
	timeout := 5 * time.Second
	for i := 0; i < len(args); i++ {
		a := args[i]
		if (a == "--timeout" || a == "-t") && i+1 < len(args) {
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				s.printf("nsh: inventory refresh: --timeout expects a positive integer (seconds)\n")
				return
			}
			timeout = time.Duration(n) * time.Second
			i++
			continue
		}
		s.printf("nsh: inventory refresh: unknown argument %q (use --timeout <seconds>)\n", a)
		return
	}
	if len(s.targets) == 0 {
		s.printf("nsh: inventory refresh requires a cd target (or -t <host>)\n")
		return
	}
	// Hard cap: NSH is a dev / admin tool, not a fleet operations surface.
	// Fleet-wide refresh (hundreds-to-thousands of endpoints) belongs in a
	// server-side scheduled operation that batches waves and rate-limits
	// — the agent handler is the same, only the dispatcher differs.
	const maxTargets = 100
	if len(s.targets) > maxTargets {
		s.printf("nsh: inventory refresh refuses more than %d targets (you have %d). "+
			"NSH is a dev/admin tool. Use the server-side scheduled fleet refresh instead "+
			"— the agent handler is identical; only the dispatcher batches waves.\n",
			maxTargets, len(s.targets))
		return
	}

	// Fire signed commands to every live target first (parallel dispatch).
	// Each pending entry remembers the pre-refresh CollectedAt so we can tell
	// a FRESH report from the cached one.
	type pending struct {
		devID    string
		host     string
		beforeAt int64
	}
	var fleet []pending
	for _, devID := range s.targets {
		host := s.srv.hostnameOf(devID)
		s.srv.liveMu.Lock()
		la, live := s.srv.liveAgents[devID]
		s.srv.liveMu.Unlock()
		if !live {
			s.printf("%s: OFFLINE (no live stream) — refresh skipped\n", host)
			continue
		}
		var beforeAt int64
		s.srv.inventoryMu.Lock()
		if rep, cached := s.srv.lastInventory[devID]; cached {
			beforeAt = rep.CollectedAt
		}
		s.srv.inventoryMu.Unlock()
		cmdID := "nsh-refresh-" + nshRandomID()
		msg := &pb.ServerMessage{Payload: &pb.ServerMessage_Command{Command: sign(&pb.Command{
			CommandId: cmdID, Type: "report_device_state",
			Actor: s.nshActor(), Source: "mockserver.nsh",
		})}}
		if err := la.send(msg); err != nil {
			s.printf("%s: send failed: %v\n", host, err)
			continue
		}
		fleet = append(fleet, pending{devID: devID, host: host, beforeAt: beforeAt})
	}
	if len(fleet) == 0 {
		return
	}
	s.printf("nsh: refresh dispatched to %d target(s), waiting up to %s (shared deadline)...\n", len(fleet), timeout)

	// Shared deadline across the whole fleet. Poll every 100ms for fresh
	// reports until every target lands or the deadline passes.
	deadline := time.Now().Add(timeout)
	done := make(map[string]bool, len(fleet))
	for len(done) < len(fleet) && time.Now().Before(deadline) {
		s.srv.inventoryMu.Lock()
		for _, p := range fleet {
			if done[p.devID] {
				continue
			}
			if rep, cached := s.srv.lastInventory[p.devID]; cached && rep.CollectedAt > p.beforeAt {
				s.printf("%s: fresh inventory (%d apps, collected_at=%d)\n", p.host, len(rep.InstalledApps), rep.CollectedAt)
				done[p.devID] = true
			}
		}
		s.srv.inventoryMu.Unlock()
		if len(done) < len(fleet) {
			time.Sleep(100 * time.Millisecond)
		}
	}
	timedOut := 0
	for _, p := range fleet {
		if !done[p.devID] {
			s.printf("%s: no fresh DeviceStateReport within %s\n", p.host, timeout)
			timedOut++
		}
	}
	s.printf("nsh: refresh complete — %d/%d received, %d timed out — run `inventory apps` to view\n",
		len(done), len(fleet), timedOut)
}

func filterTag(f string) string {
	if f == "" {
		return ""
	}
	return " matching '" + f + "'"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func (s *nshSession) verbCD(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		if len(s.targets) == 1 {
			s.changeWorkingDir("")
		}
		return
	}
	if rest == `\\` {
		s.targets = nil
		return
	}
	if rest != "*" && !strings.HasPrefix(rest, `\\`) && len(s.targets) > 0 {
		s.changeWorkingDir(rest)
		return
	}
	if rest == "*" {
		s.srv.liveMu.Lock()
		s.targets = nil
		for id := range s.srv.liveAgents {
			if s.allowedTargets != nil && !s.allowedTargets[id] {
				continue
			}
			s.targets = append(s.targets, id)
		}
		s.srv.liveMu.Unlock()
		s.printf("targets: %d online host(s)\n", len(s.targets))
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
		id, err := s.srv.resolveKnownTarget(p)
		if err != nil {
			s.printf("nsh: %v\n", err)
			return // preserve selection rather than partially selecting a list
		}
		resolved = append(resolved, id)
	}
	s.targets = resolved
	for _, id := range resolved {
		if !s.srv.isOnline(id) {
			s.printf("nsh: %s selected (OFFLINE); commands require this endpoint to reconnect\n", s.srv.hostnameOf(id))
		}
	}
	if len(resolved) == 0 {
		s.println("nsh: no targets selected")
	}
}

// Validate/resolve the directory on the endpoint. Never use the mockserver's
// local filesystem, and retain the old CWD if the endpoint rejects the change.
func (s *nshSession) changeWorkingDir(path string) {
	if len(s.targets) != 1 {
		s.println("nsh: cd/pwd needs exactly one selected endpoint")
		return
	}
	if strings.HasPrefix(strings.ToLower(path), "/d ") {
		path = strings.TrimSpace(path[3:])
	}
	path = strings.Trim(strings.TrimSpace(path), `"`)
	if strings.ContainsAny(path, "\r\n\x00") {
		s.println("nsh: invalid directory path")
		return
	}
	text := "cd"
	switch strings.ToLower(s.shell) {
	case "powershell", "ps", "pwsh":
		text = "(Get-Location).ProviderPath"
		if path != "" {
			text = "$ErrorActionPreference='Stop'; Set-Location -LiteralPath '" + strings.ReplaceAll(path, "'", "''") + "'; (Get-Location).ProviderPath"
		}
	case "", "auto", "cmd":
		if strings.Contains(path, `"`) {
			s.println("nsh: invalid directory path")
			return
		}
		if path != "" {
			text = `cd /d "` + path + `" && cd`
		}
	default:
		s.println("nsh: directory navigation currently supports cmd/powershell/pwsh on Windows")
		return
	}
	id := s.targets[0]
	st, _ := s.sendCommand(id, text, "")
	if st == nil {
		s.recordAction(nsh.ActionResult{TargetID: id, Host: s.srv.hostnameOf(id), Reason: "command not sent", ExitCode: -1})
		return
	}
	s.recordStatus(id, st)
	if st.Phase != pb.ActionPhase_ACTION_PHASE_SUCCESS {
		s.printf("nsh: directory unchanged: %s\n%s", st.Reason, parseStderr(st.Detail))
		return
	}
	cwd := strings.TrimSpace(parseStdout(st.Detail))
	if cwd == "" {
		s.println("nsh: endpoint returned no directory; directory unchanged")
		return
	}
	if s.workingDirs == nil {
		s.workingDirs = make(map[string]string)
	}
	s.workingDirs[id] = cwd
	s.printf("%s: %s\n", s.srv.hostnameOf(id), cwd)
}

func (s *nshSession) verbRun(cmdText string, fan bool) {
	cmdText = strings.TrimSpace(cmdText)
	if cmdText == "" {
		s.println("nsh: nothing to run")
		return
	}
	if len(s.targets) == 0 {
		s.println("nsh: no target — pick one with `cd \\\\<host>`")
		return
	}
	if !fan && len(s.targets) > 1 {
		s.printf("nsh: %d targets selected; use `fanout <cmd>` for multi-target\n", len(s.targets))
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
		s.println("nsh: usage: grab [server-path] <command>")
		return
	}
	if len(s.targets) == 0 {
		s.println("nsh: no target — pick one with `cd \\\\<host>`")
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
		s.println("nsh: usage: pull <remote-path> [server-path]")
		return
	}
	if len(s.targets) == 0 {
		s.println("nsh: no target — pick one with cd \\\\<host>")
		return
	}
	for _, id := range s.targets {
		up := savePath
		if up == "" {
			up = time.Now().Format("20060102-150405.000000000") + "-" + filepath.Base(strings.ReplaceAll(remote, "\\", "/"))
		}
		up = s.namedUploadPath(id, up)
		s.printf("---- %s pull %s\n", s.srv.hostnameOf(id), remote)
		st, _ := s.sendTask(id, "collect_file", &pb.CollectFileSpec{Paths: []string{remote}, MaxBytesEach: 1<<63 - 1, MaxTotalBytes: 1<<63 - 1}, up)
		if st == nil {
			continue
		}
		if st.Detail != "" {
			s.println(st.Detail)
		}
		s.printf("---- %s %s: %s\n", s.srv.hostnameOf(id), phaseWord(st.Phase), st.Reason)
		s.recordStatus(id, st)
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
		s.println("nsh: `shell` needs exactly ONE target — use `cd \\\\<host>` first")
		return
	}
	s.println("nsh: `shell` opens a persistent interactive session — run `mockserver.exe` with AEGIS_SHELL (legacy) or wait for the console UI to wire the terminal.")
	s.println("     (v1 interactive shell exists on the agent as open_shell/ShellSession; the console UI will carry stdin/stdout to a web terminal.)")
}

func (s *nshSession) verbUse(rest string) {
	rest = strings.ToLower(strings.TrimSpace(rest))
	switch rest {
	case "cmd", "powershell", "ps", "pwsh", "bash", "sh", "zsh":
		s.shell = rest
		s.printf("shell = %s\n", rest)
	default:
		s.println("nsh: use <cmd|powershell|pwsh|bash|sh|zsh>")
	}
}

func (s *nshSession) verbSet(rest string) {
	k, v := splitFirst(rest)
	v = strings.TrimSpace(v)
	switch strings.ToLower(k) {
	case "timeout":
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			s.timeout = int32(n)
			s.printf("timeout = %ds\n", n)
		}
	case "max-output":
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			s.maxOutput = n
			s.printf("max-output = %d bytes\n", n)
		}
	case "capture":
		s.capture = v == "on" || v == "true" || v == "1"
		s.printf("capture = %v\n", s.capture)
	case "output-dir":
		if v != "" {
			s.outputDir = v
			s.printf("output-dir = %s\n", v)
		}
	default:
		s.println("nsh: set <timeout|max-output|capture|output-dir> <value>")
	}
}

func (s *nshSession) verbScript(rest string) {
	sub, path := splitFirst(rest)
	if strings.ToLower(sub) != "run" || path == "" {
		s.println("nsh: usage: script run <file>")
		return
	}
	f, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		s.printf("nsh: open %s: %v\n", path, err)
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
	s.printf("%-18s  %-10s  %-60s  %s\n", "HOST", "RESULT", "FIRST LINE", "SAVED")
	for _, r := range rows {
		s.printf("%-18s  %-10s  %-60s  %s\n", r.host, r.status, r.snippet, r.saved)
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
		s.recordAction(nsh.ActionResult{TargetID: id, Host: s.srv.hostnameOf(id), Reason: "no reply", ExitCode: -1})
		return oneResult{status: "no-reply"}
	}
	s.recordStatus(id, st)
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
	s.printf("---- %s $ %s\n", host, cmdText)
	st, cmdID := s.sendCommand(id, cmdText, uploadAs)
	if st == nil {
		s.printf("     (no reply, action_id=%s)\n", cmdID)
		s.recordAction(nsh.ActionResult{TargetID: id, Host: host, ActionID: cmdID, Reason: "no reply", ExitCode: -1})
		return
	}
	stdout := parseStdout(st.Detail)
	stderr := parseStderr(st.Detail)
	s.recordStatus(id, st)
	if stdout != "" {
		s.print(stdout)
		if !strings.HasSuffix(stdout, "\n") {
			s.println()
		}
	}
	if stderr != "" {
		fmt.Fprint(s.writer(true), stderr)
		if !strings.HasSuffix(stderr, "\n") {
			s.println()
		}
	}
	if up := parseUploaded(st.Detail); up != "" {
		s.printf("---- saved to %s\n", up)
	}
	s.printf("---- %s %s  (exit=%d, %dms)\n", host, phaseWord(st.Phase), st.ExitCode, parseDuration(st.Detail))
	if st.Reason != "" {
		s.printf("     reason: %s\n", st.Reason)
	}
}

func (s *nshSession) sendCommand(deviceID, cmdText, uploadAs string) (*pb.ActionStatus, string) {
	// Forward NSH variables to the agent as command parameters: the agent
	// (a) runs ${name} substitution on the command text (idempotent — the REPL
	// already substituted known names), and (b) exports every param to the
	// child process env as AEGIS_PARAM_<NAME> so a pre-staged .ps1/.bat/.py on
	// the endpoint can read them (BigFix "parameters" pattern, softdist-shared).
	var params map[string]string
	values := s.parameters
	if values == nil && s.engine != nil {
		values = s.engine.variables()
	}
	if len(values) > 0 {
		params = make(map[string]string, len(values))
		for k, v := range values {
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
		WorkingDir:     s.workingDirs[deviceID],
	}
	return s.sendTask(deviceID, "run_command", spec, uploadAs)
}

func (s *nshSession) sendTask(deviceID, taskType string, spec proto.Message, uploadAs string) (*pb.ActionStatus, string) {
	payload, err := proto.Marshal(spec)
	if err != nil {
		s.printf("nsh: marshal: %v\n", err)
		return nil, ""
	}
	return s.sendTaskPayload(deviceID, taskType, payload, uploadAs)
}

func (s *nshSession) sendTaskPayload(deviceID, taskType string, payload []byte, uploadAs string) (*pb.ActionStatus, string) {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil, ""
	}
	if s.allowedTargets != nil && !s.allowedTargets[deviceID] {
		s.printf("nsh: %s is outside this job's authorized scope\n", deviceID)
		return nil, ""
	}
	s.srv.liveMu.Lock()
	la, ok := s.srv.liveAgents[deviceID]
	s.srv.liveMu.Unlock()
	if !ok {
		s.printf("nsh: %s is OFFLINE; command not sent (no fleet/heartbeat wait)\n", s.srv.hostnameOf(deviceID))
		return nil, ""
	}
	if uploadAs != "" {
		if _, err := safeNSHUploadPath(uploadAs); err != nil {
			s.printf("nsh: %v\n", err)
			return nil, ""
		}
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
		Actor: s.nshActor(), Source: "mockserver.nsh",
	})}}
	err := la.send(msg)
	if err != nil {
		s.srv.cancelWait(cmdID)
		s.printf("nsh: send to %s failed: %v\n", s.srv.hostnameOf(deviceID), err)
		return nil, cmdID
	}
	timeout := time.Duration(s.timeout+10) * time.Second // small grace over per-command timeout
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var terminal *pb.ActionStatus
	for {
		select {
		case <-ctx.Done():
			s.srv.cancelWait(cmdID)
			s.printf("nsh: job cancelled while waiting for %s\n", cmdID)
			return nil, cmdID
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
			s.printf("nsh: timed out waiting for %s\n", cmdID)
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
	id, err := s.resolveKnownTarget(token)
	if err == nil && s.isOnline(id) {
		return id
	}
	return ""
}

func (s *server) isOnline(id string) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	_, ok := s.liveAgents[id]
	return ok
}

func (s *server) resolveKnownTarget(token string) (string, error) {
	token = strings.TrimSpace(token)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, record := range s.byID {
		if strings.EqualFold(id, token) || (record.ComputerID != "" && record.ComputerID == token) {
			return id, nil
		}
	}
	var match string
	for id, record := range s.byID {
		if strings.EqualFold(record.Hostname, token) || (token != "" && strings.HasPrefix(strings.ToLower(id), strings.ToLower(token))) {
			if match != "" && match != id {
				return "", fmt.Errorf("ambiguous target %q; use device_id or computer_id", token)
			}
			match = id
		}
	}
	if match == "" {
		return "", fmt.Errorf("unknown device: %s", token)
	}
	return match, nil
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
