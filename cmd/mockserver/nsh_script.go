package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var base64StdEncoding = base64.StdEncoding

// ===== Script engine =====
//
// The NSH REPL and script runner share one engine so interactive input and
// non-interactive .nsh files behave identically. It carries variables, aliases,
// error policy, last-result bindings and the block stack (for/foreach/if). All
// variable substitution happens AT EXECUTE TIME, so a loop body sees the
// current ${VAR} / $host on every iteration.
//
// Agent-side substitution (${hostname}, ${device_id}) is separate — those are
// resolved on the endpoint by the RunCommandSpec.parameters engine and only
// those names unknown to this engine fall through to the agent.

type scriptEngine struct {
	sess       *nshSession
	vars       map[string]string // let-defined + $exit/$stdout/$host/...
	aliases    map[string]string // alias name -> expansion
	onError    string            // "continue" | "stop" (default "stop" for scripts)
	format     string            // "table" | "json" | "csv" (fanout rendering)
	sessionLog *os.File          // non-nil when set session-log <path> ran
	blocks     []*blockFrame     // active for/foreach/if frames
	breakOut   int               // >0 = unwind N loops (for `break`)
	contLoop   bool              // true = jump to next iteration (`continue`)
	// HEREDOC state (top-level only; inside a for/if block the opener is
	// captured as body text and replayed through execLine per iteration).
	inHeredoc    bool
	heredocShell string
	heredocDelim string
	heredocLines []string
}

// blockFrame is one active block (for, foreach, if). The engine buffers body
// lines between the opening keyword and the matching `end`; once `end` is seen
// the body is replayed N times (for/foreach) or once/skipped (if).
type blockFrame struct {
	kind         string   // "for" | "foreach" | "if"
	varName      string   // loop variable
	values       []string // for/foreach: values to iterate
	body         []string // buffered body lines
	depth        int      // nested-block tracker inside the body
	cond         bool     // if: condition evaluated true
	inElse       bool     // if: we're after `else`
	elseBody     []string // if: lines after `else`
	heredocDelim string   // non-empty while we're buffering INSIDE a heredoc
}

func newScriptEngine(sess *nshSession) *scriptEngine {
	return &scriptEngine{
		sess:    sess,
		vars:    map[string]string{},
		aliases: map[string]string{},
		onError: "stop",
		format:  "table",
	}
}

// ===== Line entry: handles blocks, dispatches to verbs =====

// execLine is called once per source line (REPL input or script line). It
// returns false only on `exit`. It understands control-flow keywords and buffers
// bodies for for/foreach/if and heredocs; the normal verbs fall through to the
// REPL.
func (e *scriptEngine) execLine(raw string) bool {
	// Preserve exact text in heredoc / block mode (indentation matters). The
	// trimmed form is only used to parse keywords.
	line := strings.TrimSpace(raw)
	// At top level, blank lines and `#` comments are skipped. Inside a heredoc
	// they are PART OF THE SCRIPT and must be preserved verbatim.
	if !e.inHeredoc && len(e.blocks) == 0 && (line == "" || strings.HasPrefix(line, "#")) {
		return true
	}
	e.writeSessionLog(raw)

	// Top-level heredoc collection: capture every raw line (including blanks
	// and comments) until the delimiter line, then dispatch as one encoded
	// command — the BigFix `createfile until ... <delim>` ergonomic without
	// dropping a temp file on the endpoint.
	if e.inHeredoc {
		if line == e.heredocDelim {
			e.closeHeredoc()
			return true
		}
		e.heredocLines = append(e.heredocLines, raw)
		return true
	}

	// Buffering inside an open block? Capture until matching end — and if the
	// body contains a heredoc, do not count its `end` / opener keywords.
	if len(e.blocks) > 0 {
		top := e.blocks[len(e.blocks)-1]
		if top.heredocDelim != "" {
			if line == top.heredocDelim {
				top.heredocDelim = ""
			}
			top.appendBody(raw, e)
			return true
		}
		if _, _, ok := parseHeredocOpener(line); ok {
			if _, delim, _ := parseHeredocOpener(line); delim != "" {
				top.heredocDelim = delim
			}
			top.appendBody(raw, e)
			return true
		}
		if openerOf(line) != "" {
			top.depth++
			top.appendBody(raw, e)
			return true
		}
		if line == "end" {
			if top.depth > 0 {
				top.depth--
				top.appendBody(raw, e)
				return true
			}
			return e.closeBlock()
		}
		if line == "else" && top.kind == "if" && top.depth == 0 {
			top.inElse = true
			return true
		}
		top.appendBody(raw, e)
		return true
	}

	// Top level — heredoc opener? Enter collection mode.
	if shell, delim, ok := parseHeredocOpener(line); ok {
		e.inHeredoc = true
		e.heredocShell = shell
		e.heredocDelim = delim
		e.heredocLines = nil
		return true
	}

	// Top level — control-flow starters open a block; normal verbs run now.
	switch opener := openerOf(line); opener {
	case "for":
		return e.openFor(line)
	case "foreach":
		return e.openForeach(line)
	case "if":
		return e.openIf(line)
	}

	return e.dispatchVerb(line)
}

// parseHeredocOpener matches `script <shell> until <DELIM>` (BigFix-style
// createfile until — but delivered as an encoded one-shot command, not a
// dropped file). Example: `script powershell until END`.
var heredocRE = regexp.MustCompile(`^script\s+(\S+)\s+until\s+(\S+)\s*$`)

func parseHeredocOpener(line string) (shell, delim string, ok bool) {
	m := heredocRE.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return m[1], m[2], true
}

// closeHeredoc builds the final script content, substitutes ${VAR}, encodes
// per interpreter, and dispatches as a single run_command through verbRun —
// which honors current targets, capture mode, and timeout.
func (e *scriptEngine) closeHeredoc() {
	content := strings.Join(e.heredocLines, "\n")
	content = e.expand(content) // ${VAR} / ${env:X} applied to WHOLE script
	cmdText, agentShell := buildShellCommand(e.heredocShell, content)

	saved := e.sess.shell
	e.sess.shell = agentShell
	e.sess.verbRun(cmdText, len(e.sess.targets) > 1)
	e.sess.shell = saved

	e.inHeredoc = false
	e.heredocShell = ""
	e.heredocDelim = ""
	e.heredocLines = nil
}

// blockFrame extension: a heredoc opened INSIDE a block body (foreach / if)
// captures its lines as part of the body — depth tracking is suspended until
// the delimiter closes the heredoc.

func openerOf(line string) string {
	w, _ := splitFirst(line)
	switch strings.ToLower(w) {
	case "for", "foreach", "if":
		return strings.ToLower(w)
	}
	return ""
}

// appendBody stashes a body line (also keeps the opening line of a nested
// block so replay sees the whole structure).
func (f *blockFrame) appendBody(line string, e *scriptEngine) {
	if f.kind == "if" && f.inElse {
		f.elseBody = append(f.elseBody, line)
		return
	}
	f.body = append(f.body, line)
}

// closeBlock pops the top block and executes it.
func (e *scriptEngine) closeBlock() bool {
	top := e.blocks[len(e.blocks)-1]
	e.blocks = e.blocks[:len(e.blocks)-1]
	switch top.kind {
	case "for", "foreach":
		return e.runLoop(top)
	case "if":
		return e.runIf(top)
	}
	return true
}

// ===== Control flow openers =====

// `for VAR in a b c` — plain for-loop over space-separated values. Values may
// use ${VAR} / ${env:NAME} substitution against the enclosing scope.
func (e *scriptEngine) openFor(line string) bool {
	parts := strings.Fields(line)
	if len(parts) < 4 || strings.ToLower(parts[2]) != "in" {
		fmt.Println("nsh: usage: for VAR in <values ...>")
		return true
	}
	v := parts[1]
	vals := parts[3:]
	// Expand ${vars} on the values list.
	for i := range vals {
		vals[i] = e.expand(vals[i])
	}
	// Support `for VAR in a,b,c`
	if len(vals) == 1 && strings.Contains(vals[0], ",") {
		vals = splitCSV(vals[0])
	}
	e.blocks = append(e.blocks, &blockFrame{kind: "for", varName: v, values: vals})
	return true
}

// `foreach host in *|list` — host iteration (binds $host, implicit `cd \\$host`
// per iteration). Convenience over writing cd + fanout by hand.
func (e *scriptEngine) openForeach(line string) bool {
	parts := strings.Fields(line)
	if len(parts) < 4 || strings.ToLower(parts[2]) != "in" {
		fmt.Println("nsh: usage: foreach host in *|<h1>,<h2>|${var}")
		return true
	}
	selector := strings.Join(parts[3:], " ")
	selector = e.expand(selector)
	var hosts []string
	switch strings.TrimSpace(selector) {
	case "*":
		e.sess.srv.liveMu.Lock()
		for id := range e.sess.srv.liveAgents {
			hosts = append(hosts, e.sess.srv.hostnameOf(id))
		}
		e.sess.srv.liveMu.Unlock()
	default:
		hosts = splitCSV(selector)
	}
	sort.Strings(hosts)
	e.blocks = append(e.blocks, &blockFrame{kind: "foreach", varName: parts[1], values: hosts})
	return true
}

// `if <cond>` — condition evaluated now; body runs if true, else-body if false.
// Conditions supported: `$name == value`, `$name != value`, `stdout contains "..."`,
// `stderr contains "..."`, `defined NAME`, with optional leading `!` negation.
func (e *scriptEngine) openIf(line string) bool {
	cond := strings.TrimSpace(strings.TrimPrefix(line, "if"))
	if cond == "" {
		fmt.Println("nsh: usage: if <condition>")
		return true
	}
	val := e.evalCond(cond)
	e.blocks = append(e.blocks, &blockFrame{kind: "if", cond: val})
	return true
}

// ===== Executors (replay buffered bodies) =====

func (e *scriptEngine) runLoop(f *blockFrame) bool {
	for _, v := range f.values {
		e.vars[f.varName] = v
		if f.kind == "foreach" {
			e.vars["host"] = v
			id := e.sess.srv.resolveTarget(v)
			if id == "" {
				fmt.Printf("nsh: foreach — no such online host: %s (skipping)\n", v)
				continue
			}
			e.sess.targets = []string{id}
		}
		for _, line := range f.body {
			if !e.execLine(line) {
				return false
			}
			if e.breakOut > 0 {
				e.breakOut--
				goto loopDone
			}
			if e.contLoop {
				e.contLoop = false
				break
			}
		}
	}
loopDone:
	return true
}

func (e *scriptEngine) runIf(f *blockFrame) bool {
	body := f.body
	if !f.cond {
		body = f.elseBody
	}
	for _, line := range body {
		if !e.execLine(line) {
			return false
		}
		if e.breakOut > 0 || e.contLoop {
			return true
		}
	}
	return true
}

// ===== Verb dispatch (after substitution, with REPL + script verbs) =====

func (e *scriptEngine) dispatchVerb(line string) bool {
	// Alias expansion.
	w, rest := splitFirst(line)
	if a, ok := e.aliases[w]; ok {
		if rest == "" {
			line = a
		} else {
			line = a + " " + rest
		}
	}
	// Substitute ${VAR} / ${env:NAME} / $exit etc. BEFORE routing.
	line = e.expand(line)

	v, rest := splitFirst(line)
	switch strings.ToLower(v) {
	case "exit", "quit", "bye":
		return false
	case "help":
		e.sess.verbHelp(rest)
	case "hosts":
		e.sess.verbHosts(rest)
	case "cd":
		e.sess.verbCD(rest)
	case "run":
		e.sess.verbRun(rest, false)
	case "grab":
		e.sess.verbGrab(rest)
	case "pull":
		e.sess.verbPull(rest)
	case "fanout":
		e.sess.verbRun(rest, true)
	case "shell":
		e.sess.verbShell()
	case "use":
		e.sess.verbUse(rest)
	case "set":
		e.verbSet(rest)
	case "history":
		for i, h := range e.sess.history {
			fmt.Printf("  %3d  %s\n", i+1, h)
		}
	case "script":
		e.verbScript(rest)
	case "let":
		e.verbLet(rest)
	case "log":
		fmt.Println(rest)
		e.writeSessionLog("# log: " + rest)
	case "alias":
		e.verbAlias(rest)
	case "on-error":
		e.verbOnError(rest)
	case "break":
		n := 1
		if rest != "" {
			if m, err := strconv.Atoi(rest); err == nil {
				n = m
			}
		}
		e.breakOut = n
	case "continue":
		e.contLoop = true
	case "wait":
		if n, err := strconv.Atoi(rest); err == nil {
			time.Sleep(time.Duration(n) * time.Second)
		}
	case "source":
		e.verbSource(rest)
	case "exec":
		e.verbExec(rest)
	case "vars":
		for k, v := range e.vars {
			fmt.Printf("  %s = %s\n", k, v)
		}
	default:
		// Bare command → run_command on current target(s).
		e.sess.verbRun(line, len(e.sess.targets) > 1)
	}
	return true
}

// ===== Verbs the engine owns (vs sess.verbXxx) =====

func (e *scriptEngine) verbSet(rest string) {
	k, v := splitFirst(rest)
	v = strings.TrimSpace(v)
	switch strings.ToLower(k) {
	case "timeout":
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			e.sess.timeout = int32(n)
			fmt.Printf("timeout = %ds\n", n)
		}
	case "max-output":
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 {
			e.sess.maxOutput = n
			fmt.Printf("max-output = %d bytes\n", n)
		}
	case "capture":
		e.sess.capture = v == "on" || v == "true" || v == "1"
		fmt.Printf("capture = %v\n", e.sess.capture)
	case "output-dir":
		if v != "" {
			e.sess.outputDir = v
			fmt.Printf("output-dir = %s\n", v)
		}
	case "format":
		switch strings.ToLower(v) {
		case "table", "json", "csv":
			e.format = strings.ToLower(v)
			fmt.Printf("format = %s\n", e.format)
		default:
			fmt.Println("nsh: set format table|json|csv")
		}
	case "session-log":
		e.setSessionLog(v)
	default:
		fmt.Println("nsh: set <timeout|max-output|capture|output-dir|format|session-log> <value>")
	}
}

func (e *scriptEngine) verbLet(rest string) {
	i := strings.IndexByte(rest, '=')
	if i < 0 {
		fmt.Println("nsh: usage: let NAME = value")
		return
	}
	name := strings.TrimSpace(rest[:i])
	val := strings.TrimSpace(rest[i+1:])
	val = strings.Trim(val, `"'`)
	// Allow substitution on the RHS so `let X = ${env:USERNAME}` works.
	val = e.expand(val)
	e.vars[name] = val
	fmt.Printf("%s = %s\n", name, val)
}

func (e *scriptEngine) verbAlias(rest string) {
	i := strings.IndexByte(rest, '=')
	if i < 0 {
		fmt.Println("nsh: usage: alias NAME = <expansion>")
		return
	}
	name := strings.TrimSpace(rest[:i])
	exp := strings.TrimSpace(rest[i+1:])
	e.aliases[name] = exp
	fmt.Printf("alias %s = %s\n", name, exp)
}

func (e *scriptEngine) verbOnError(rest string) {
	rest = strings.ToLower(strings.TrimSpace(rest))
	switch rest {
	case "continue", "stop":
		e.onError = rest
		fmt.Printf("on-error = %s\n", rest)
	default:
		fmt.Println("nsh: on-error continue|stop")
	}
}

func (e *scriptEngine) verbScript(rest string) {
	sub, path := splitFirst(rest)
	if strings.ToLower(sub) != "run" || path == "" {
		fmt.Println("nsh: usage: script run <file>")
		return
	}
	e.runScriptFile(strings.TrimSpace(path), true)
}

// `exec [shell] <local-file>` — read a script file from the CONSOLE side,
// apply ${VAR} / ${env:NAME} substitution to its WHOLE CONTENT, then send it
// to the current target(s) as a single run_command. BigFix "action script with
// embedded relevance" equivalent: admin writes a .ps1 / .py / .bat with NSH
// vars inside, console resolves and ships the final script text.
//
// Shells supported today: cmd / powershell / pwsh (Windows). bash/sh/zsh will
// work the same way when the agent targets POSIX. For Python the shell is
// `cmd` and the script content is wrapped as `python -c "<content>"`. If no
// shell is given, infers from extension: .ps1→powershell, .bat/.cmd→cmd,
// .py→python-via-cmd, else current `use`.
func (e *scriptEngine) verbExec(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		fmt.Println("nsh: usage: exec [shell] <local-file>")
		return
	}
	// Decide which word is the shell vs file.
	var shell, path string
	parts := strings.Fields(rest)
	if len(parts) == 1 {
		path = parts[0]
	} else {
		// Treat first token as shell if it matches a known interpreter.
		switch strings.ToLower(parts[0]) {
		case "cmd", "powershell", "ps", "pwsh", "python", "py", "bash", "sh", "zsh":
			shell = strings.ToLower(parts[0])
			path = strings.Join(parts[1:], " ")
		default:
			path = rest
		}
	}
	path = strings.Trim(path, `"'`)
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("nsh: exec: read %s: %v\n", path, err)
		return
	}
	content := e.expand(string(b)) // console-side ${VAR} substitution

	// Infer shell from extension if admin did not name one.
	if shell == "" {
		switch strings.ToLower(extOf(path)) {
		case ".ps1":
			shell = "powershell"
		case ".bat", ".cmd":
			shell = "cmd"
		case ".py":
			shell = "python"
		case ".sh":
			shell = "bash"
		default:
			shell = e.sess.shell
		}
	}

	cmdText, agentShell := buildShellCommand(shell, content)
	savedShell := e.sess.shell
	e.sess.shell = agentShell
	e.sess.verbRun(cmdText, len(e.sess.targets) > 1)
	e.sess.shell = savedShell
}

// buildShellCommand packages a (possibly multi-line) script for the chosen
// interpreter so the agent runs it as ONE self-contained command — no file is
// created on the endpoint. Returns the command text and the AGENT shell to
// use for RunCommandSpec.shell. Shared by `exec <file>` and the `script
// <shell> until <delim>` heredoc.
func buildShellCommand(shell, content string) (cmdText, agentShell string) {
	s := strings.ToLower(strings.TrimSpace(shell))
	switch s {
	case "powershell", "ps", "pwsh":
		enc := utf16LEBase64(content)
		// The agent already launches this interpreter with -Command. Supply a
		// PowerShell expression, not process startup flags as script text.
		text := "& ([ScriptBlock]::Create([Text.Encoding]::Unicode.GetString([Convert]::FromBase64String('" + enc + "'))))"
		if s == "pwsh" {
			return text, "pwsh"
		}
		return text, "powershell"
	case "cmd":
		// cmd.exe through /C accepts a single command line; join a multi-line
		// script with ` & `, drop :: / rem comments. Rich .bat (labels, GOTO)
		// should ship via softdist as a file.
		return inlineBat(content), "cmd"
	case "python", "py":
		enc := base64Str(content)
		// Run via cmd so AEGIS_PARAM_<NAME> env vars pass through unchanged.
		return fmt.Sprintf(`python.exe -c "import base64; exec(base64.b64decode('%s').decode())"`, enc), "cmd"
	case "bash", "sh", "zsh":
		enc := base64Str(content)
		return fmt.Sprintf(`-c "echo %s | base64 -d | %s"`, enc, s), s
	default:
		return content, "cmd"
	}
}

func (e *scriptEngine) verbSource(rest string) {
	path := strings.TrimSpace(rest)
	if path == "" {
		fmt.Println("nsh: usage: source <file>")
		return
	}
	e.runScriptFile(path, false)
}

// ===== Script-file execution =====

func (e *scriptEngine) runScriptFile(path string, capture bool) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Printf("nsh: open %s: %v\n", path, err)
		return
	}
	defer f.Close()
	savedCap := e.sess.capture
	if capture {
		e.sess.capture = true
	}
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadString('\n')
		if line != "" {
			if !e.execLine(strings.TrimRight(line, "\r\n")) {
				e.sess.capture = savedCap
				return
			}
		}
		if err == io.EOF || err != nil {
			break
		}
	}
	e.sess.capture = savedCap
}

// ===== Session log =====

func (e *scriptEngine) setSessionLog(path string) {
	if e.sessionLog != nil {
		e.sessionLog.Close()
		e.sessionLog = nil
	}
	if path == "" || strings.EqualFold(path, "off") {
		fmt.Println("session-log = off")
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Printf("nsh: open session-log %s: %v\n", path, err)
		return
	}
	fmt.Fprintf(f, "\n=== session resumed %s ===\n", time.Now().UTC().Format(time.RFC3339))
	e.sessionLog = f
	fmt.Printf("session-log = %s\n", path)
}

func (e *scriptEngine) writeSessionLog(line string) {
	if e.sessionLog == nil {
		return
	}
	fmt.Fprintln(e.sessionLog, line)
}

// ===== Substitution + condition eval =====

var varRE = regexp.MustCompile(`\$\{([^}]+)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// expand resolves ${name} and $name against engine variables, special
// last-result bindings, and ${env:NAME} for mock-side env. Agent-side names
// (hostname, device_id) that the engine doesn't know are passed through
// unchanged so the agent's own substitution still handles them.
func (e *scriptEngine) expand(s string) string {
	return varRE.ReplaceAllStringFunc(s, func(match string) string {
		m := varRE.FindStringSubmatch(match)
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if strings.HasPrefix(name, "env:") {
			return os.Getenv(name[4:])
		}
		if v, ok := e.vars[name]; ok {
			return v
		}
		// Unknown → leave as-is (agent-side names fall through here).
		return match
	})
}

// evalCond understands:  $NAME == x | != x,  stdout contains "x",
// stderr contains "x", defined NAME, with optional leading `!` negation.
func (e *scriptEngine) evalCond(c string) bool {
	c = strings.TrimSpace(c)
	negate := false
	if strings.HasPrefix(c, "!") {
		negate = true
		c = strings.TrimSpace(c[1:])
	}
	// defined NAME
	if strings.HasPrefix(strings.ToLower(c), "defined ") {
		name := strings.TrimSpace(c[len("defined "):])
		_, ok := e.vars[name]
		return ok != negate
	}
	// stdout/stderr contains "x"
	if m := regexp.MustCompile(`^(stdout|stderr)\s+contains\s+(.+)$`).FindStringSubmatch(c); m != nil {
		stream := m[1]
		needle := strings.Trim(strings.TrimSpace(m[2]), `"'`)
		haystack := e.vars[stream]
		return strings.Contains(haystack, needle) != negate
	}
	// $NAME op value
	if m := regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)\s*(==|!=|>=|<=|>|<)\s*(.*)$`).FindStringSubmatch(c); m != nil {
		got := e.vars[m[1]]
		op := m[2]
		want := strings.Trim(strings.TrimSpace(m[3]), `"'`)
		return cmpStrings(got, op, want) != negate
	}
	fmt.Printf("nsh: cannot evaluate condition: %q\n", c)
	return false
}

func cmpStrings(got, op, want string) bool {
	gi, gErr := strconv.ParseInt(got, 10, 64)
	wi, wErr := strconv.ParseInt(want, 10, 64)
	if gErr == nil && wErr == nil {
		switch op {
		case "==":
			return gi == wi
		case "!=":
			return gi != wi
		case ">":
			return gi > wi
		case "<":
			return gi < wi
		case ">=":
			return gi >= wi
		case "<=":
			return gi <= wi
		}
	}
	switch op {
	case "==":
		return got == want
	case "!=":
		return got != want
	}
	return false
}

// recordLastResult is called after each run_command returns, so subsequent
// `if $exit == 0` conditions and ${stdout} substitutions see fresh values.
func (e *scriptEngine) recordLastResult(host, stdout, stderr, reason string, exit int, durationMs int64) {
	e.vars["host"] = host
	e.vars["exit"] = strconv.Itoa(exit)
	e.vars["stdout"] = stdout
	e.vars["stderr"] = stderr
	e.vars["reason"] = reason
	e.vars["duration"] = strconv.FormatInt(durationMs, 10)
}

// shouldStop returns true when a non-zero exit should abort the script.
func (e *scriptEngine) shouldStop(exit int) bool {
	return exit != 0 && e.onError == "stop" && len(e.blocks) == 0 // never abort INSIDE a user block
}

// ===== small utils =====

// extOf returns the lowercased extension (".ps1", ".bat", ...) of a path.
func extOf(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return ""
	}
	return path[i:]
}

// utf16LEBase64 encodes a string as UTF-16LE then base64 — the format
// powershell.exe -EncodedCommand expects. Lets a multi-line script with any
// quoting / special chars pass through a single command-line arg safely.
func utf16LEBase64(s string) string {
	runes := []rune(s)
	b := make([]byte, 0, len(runes)*2)
	for _, r := range runes {
		if r <= 0xFFFF {
			b = append(b, byte(r), byte(r>>8))
		} else {
			r -= 0x10000
			hi := uint16(0xD800 + (r >> 10))
			lo := uint16(0xDC00 + (r & 0x3FF))
			b = append(b, byte(hi), byte(hi>>8), byte(lo), byte(lo>>8))
		}
	}
	return base64StdEncode(b)
}

// base64Str base64-encodes a string.
func base64Str(s string) string { return base64StdEncode([]byte(s)) }

// base64StdEncode is a tiny wrapper that pulls in encoding/base64 for one call
// without widening the import blocks in a dozen places.
func base64StdEncode(b []byte) string {
	return base64StdEncoding.EncodeToString(b)
}

// inlineBat flattens a multi-line .bat into a cmd /C-friendly one-liner by
// joining lines with ` & `. Comments (lines beginning with `::` or `rem `) are
// dropped. Admins who need rich .bat should ship it via softdist instead.
func inlineBat(content string) string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "::") || strings.HasPrefix(strings.ToLower(t), "rem ") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, " & ")
}

func splitCSV(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
