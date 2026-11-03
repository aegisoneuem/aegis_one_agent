package nsh

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
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

type Engine struct {
	backend          Backend
	options          Options
	ctx              context.Context
	mu               sync.Mutex
	actions          []ActionResult
	totalActions     int
	failedActions    int
	hadFailure       bool
	resultBytes      int
	resultsTruncated bool
	stopScript       bool
	scriptDepth      int
	lastError        error
	vars             map[string]string // let-defined + $exit/$stdout/$host/...
	aliases          map[string]string // alias name -> expansion
	onError          string            // "continue" | "stop" (default "stop" for scripts)
	format           string            // "table" | "json" | "csv" (fanout rendering)
	sessionLog       io.WriteCloser    // non-nil when set session-log <path> ran
	blocks           []*blockFrame     // active for/foreach/if frames
	breakOut         int               // >0 = unwind N loops (for `break`)
	contLoop         bool              // true = jump to next iteration (`continue`)
	// HEREDOC state (top-level only; inside a for/if block the opener is
	// captured as body text and replayed through ExecLine per iteration).
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

func New(backend Backend, options Options) *Engine {
	if options.Output == nil {
		options.Output = io.Discard
	}
	if options.MaxActions <= 0 {
		options.MaxActions = 10000
	}
	if options.MaxSourceDepth <= 0 {
		options.MaxSourceDepth = 16
	}
	if options.MaxResultBytes <= 0 {
		options.MaxResultBytes = 1 << 20
	}
	if options.MaxSourceBytes <= 0 {
		options.MaxSourceBytes = DefaultMaxSourceBytes
	}
	if options.MaxVariableBytes <= 0 {
		options.MaxVariableBytes = 1 << 20
	}
	e := &Engine{backend: backend, options: options, ctx: context.Background(), vars: map[string]string{}, aliases: map[string]string{}, onError: "stop", format: "table"}
	for k, v := range options.Parameters {
		e.SetVariable(k, v)
	}
	return e
}

func (e *Engine) ExecLine(raw string) bool {
	if e.cancelled() {
		return false
	}
	if e.scriptDepth == 0 {
		e.mu.Lock()
		e.stopScript = false
		e.mu.Unlock()
	}
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
func (e *Engine) closeHeredoc() {
	content := e.Expand(strings.Join(e.heredocLines, "\n"))
	command, shell := BuildShellCommand(e.heredocShell, content)
	e.inHeredoc = false
	e.heredocShell, e.heredocDelim = "", ""
	e.heredocLines = nil
	e.dispatch(Command{Verb: "run", Text: command, Shell: shell, Fanout: len(e.backend.SelectedTargets()) > 1})
}

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
func (f *blockFrame) appendBody(line string, e *Engine) {
	if f.kind == "if" && f.inElse {
		f.elseBody = append(f.elseBody, line)
		return
	}
	f.body = append(f.body, line)
}

// closeBlock pops the top block and executes it.
func (e *Engine) closeBlock() bool {
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
func (e *Engine) openFor(line string) bool {
	parts := strings.Fields(line)
	if len(parts) < 4 || strings.ToLower(parts[2]) != "in" {
		e.fail(fmt.Errorf("usage: for VAR in <values ...>"))
		return true
	}
	v := parts[1]
	vals := parts[3:]
	// Expand ${vars} on the values list.
	for i := range vals {
		vals[i] = e.Expand(vals[i])
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
func (e *Engine) openForeach(line string) bool {
	parts := strings.Fields(line)
	if len(parts) < 4 || strings.ToLower(parts[2]) != "in" {
		e.fail(fmt.Errorf("usage: foreach host in *|<h1>,<h2>|${var}"))
		return true
	}
	selector := strings.Join(parts[3:], " ")
	selector = e.Expand(selector)
	var hosts []string
	switch strings.TrimSpace(selector) {
	case "*":
		targets, err := e.backend.WildcardTargets(e.ctx)
		if err != nil {
			e.fail(err)
			return true
		}
		for _, target := range targets {
			hosts = append(hosts, target.ID)
		}
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
func (e *Engine) openIf(line string) bool {
	cond := strings.TrimSpace(strings.TrimPrefix(line, "if"))
	if cond == "" {
		e.fail(fmt.Errorf("usage: if <condition>"))
		return true
	}
	val := e.evalCond(cond)
	e.blocks = append(e.blocks, &blockFrame{kind: "if", cond: val})
	return true
}

// ===== Executors (replay buffered bodies) =====

func (e *Engine) runLoop(f *blockFrame) bool {
	saved := append([]string(nil), e.backend.SelectedTargets()...)
	if f.kind == "foreach" {
		defer func() {
			if err := e.backend.SelectTargets(e.ctx, saved); err != nil {
				e.fail(err)
			}
		}()
	}
	for _, value := range f.values {
		if e.cancelled() || e.stopping() {
			break
		}
		e.SetVariable(f.varName, value)
		if f.kind == "foreach" {
			target, err := e.backend.ResolveTarget(e.ctx, value)
			if err != nil {
				e.fail(err)
				if e.stopping() {
					break
				}
				continue
			}
			e.SetVariable("host", target.Name)
			e.SetVariable(f.varName, target.Name)
			if err = e.backend.SelectTargets(e.ctx, []string{target.ID}); err != nil {
				e.fail(err)
				continue
			}
		}
		for _, line := range f.body {
			if !e.ExecLine(line) {
				return false
			}
			if e.stopping() || e.cancelled() {
				return true
			}
			if e.breakOut > 0 {
				e.breakOut--
				return true
			}
			if e.contLoop {
				e.contLoop = false
				break
			}
		}
	}
	return true
}

func (e *Engine) runIf(f *blockFrame) bool {
	body := f.body
	if !f.cond {
		body = f.elseBody
	}
	for _, line := range body {
		if !e.ExecLine(line) {
			return false
		}
		if e.breakOut > 0 || e.contLoop || e.stopping() || e.cancelled() {
			return true
		}
	}
	return true
}

// ===== Verb dispatch (after substitution, with REPL + script verbs) =====

func (e *Engine) dispatchVerb(line string) bool {
	word, rest := splitFirst(line)
	if alias, ok := e.aliases[word]; ok {
		line = alias
		if rest != "" {
			line += " " + rest
		}
	}
	line = e.Expand(line)
	verb, rest := splitFirst(line)
	switch strings.ToLower(verb) {
	case "exit", "quit", "bye":
		return false
	case "set":
		e.verbSet(rest)
	case "script":
		e.verbScript(rest)
	case "let":
		e.verbLet(rest)
	case "log":
		e.println(rest)
		e.writeSessionLog("# log: " + rest)
	case "alias":
		e.verbAlias(rest)
	case "on-error":
		e.verbOnError(rest)
	case "break":
		n := 1
		if rest != "" {
			if value, err := strconv.Atoi(rest); err == nil && value > 0 {
				n = value
			} else {
				e.fail(fmt.Errorf("invalid break count"))
				return true
			}
		}
		e.breakOut = n
	case "continue":
		e.contLoop = true
	case "wait":
		n, err := strconv.Atoi(rest)
		if err != nil || n < 0 {
			e.fail(fmt.Errorf("wait needs nonnegative seconds"))
		} else {
			e.wait(time.Duration(n) * time.Second)
		}
	case "source":
		e.verbSource(rest)
	case "exec":
		e.verbExec(rest)
	case "vars":
		variables := e.Variables()
		keys := make([]string, 0, len(variables))
		for key := range variables {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			e.printf("  %s = %s\n", key, variables[key])
		}
	case "end", "else":
		e.fail(fmt.Errorf("unexpected %s", verb))
	case "help", "hosts", "search", "copy", "cp", "move", "mv", "delete", "del", "rm", "mkdir", "md", "touch", "cd", "pwd", "run", "grab", "pull", "fanout", "shell", "use", "history", "job", "inventory", "cat":
		e.dispatch(Command{Verb: strings.ToLower(verb), Text: rest})
	default:
		e.dispatch(Command{Verb: "run", Text: line, Fanout: len(e.backend.SelectedTargets()) > 1})
	}
	return true
}

func (e *Engine) verbSet(rest string) {
	key, value := splitFirst(rest)
	switch strings.ToLower(key) {
	case "format":
		switch strings.ToLower(value) {
		case "table", "json", "csv":
			e.format = strings.ToLower(value)
			e.printf("format = %s\n", e.format)
		default:
			e.fail(fmt.Errorf("format must be table|json|csv"))
		}
	case "session-log":
		e.setSessionLog(value)
	default:
		e.dispatch(Command{Verb: "set", Text: rest})
	}
}

func (e *Engine) verbLet(rest string) {
	i := strings.IndexByte(rest, '=')
	if i < 0 {
		e.fail(fmt.Errorf("usage: let NAME = value"))
		return
	}
	name := strings.TrimSpace(rest[:i])
	val := strings.TrimSpace(rest[i+1:])
	val = strings.Trim(val, `"'`)
	// Allow substitution on the RHS so `let X = ${env:USERNAME}` works.
	val = e.Expand(val)
	e.SetVariable(name, val)
	e.printf("%s = %s\n", name, val)
}

func (e *Engine) verbAlias(rest string) {
	i := strings.IndexByte(rest, '=')
	if i < 0 {
		e.fail(fmt.Errorf("usage: alias NAME = <expansion>"))
		return
	}
	name := strings.TrimSpace(rest[:i])
	exp := strings.TrimSpace(rest[i+1:])
	e.aliases[name] = exp
	e.printf("alias %s = %s\n", name, exp)
}

func (e *Engine) verbOnError(rest string) {
	rest = strings.ToLower(strings.TrimSpace(rest))
	switch rest {
	case "continue", "stop":
		e.onError = rest
		e.printf("on-error = %s\n", rest)
	default:
		e.fail(fmt.Errorf("on-error must be continue|stop"))
	}
}

func (e *Engine) verbScript(rest string) {
	sub, path := splitFirst(rest)
	if strings.ToLower(sub) != "run" || path == "" {
		e.fail(fmt.Errorf("usage: script run <file>"))
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
func (e *Engine) verbExec(rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		e.fail(fmt.Errorf("usage: exec [shell] <local-file>"))
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
	b, err := e.loadFile(path)
	if err != nil {
		e.fail(fmt.Errorf("exec: read %s: %w", path, err))
		return
	}
	content := e.Expand(string(b)) // console-side ${VAR} substitution

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
			shell = e.backend.Shell()
		}
	}

	cmdText, agentShell := BuildShellCommand(shell, content)
	e.dispatch(Command{Verb: "run", Text: cmdText, Shell: agentShell, Fanout: len(e.backend.SelectedTargets()) > 1})
}

// BuildShellCommand packages a (possibly multi-line) script for the chosen
// interpreter so the agent runs it as ONE self-contained command — no file is
// created on the endpoint. Returns the command text and the AGENT shell to
// use for RunCommandSpec.shell. Shared by `exec <file>` and the `script
// <shell> until <delim>` heredoc.
func BuildShellCommand(shell, content string) (cmdText, agentShell string) {
	s := strings.ToLower(strings.TrimSpace(shell))
	switch s {
	case "powershell", "ps", "pwsh":
		enc := utf16LEBase64(content)
		// The agent already launches this interpreter with -Command. Supply a
		// PowerShell expression, not process startup flags as script text.
		// Promote unhandled PowerShell errors to a failed task even if a later
		// pipeline succeeds. Scripts can still handle expected errors explicitly.
		text := "$ErrorActionPreference='Stop'; try { & ([ScriptBlock]::Create([Text.Encoding]::Unicode.GetString([Convert]::FromBase64String('" + enc + "')))) } catch { [Console]::Error.WriteLine($_.ToString()); exit 1 }"
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

func (e *Engine) verbSource(rest string) {
	path := strings.TrimSpace(rest)
	if path == "" {
		e.fail(fmt.Errorf("usage: source <file>"))
		return
	}
	e.runScriptFile(path, false)
}

// ===== Script-file execution =====

func (e *Engine) runScriptFile(path string, capture bool) {
	if e.scriptDepth >= e.options.MaxSourceDepth {
		e.fail(fmt.Errorf("source depth limit exceeded"))
		return
	}
	data, err := e.loadFile(strings.Trim(path, "\"'"))
	if err != nil {
		e.fail(fmt.Errorf("source %s: %w", path, err))
		return
	}
	saved := e.backend.Capture()
	if capture {
		e.backend.SetCapture(true)
	}
	defer e.backend.SetCapture(saved)
	e.readSource(strings.NewReader(string(data)))
}

func (e *Engine) setSessionLog(path string) {
	if e.sessionLog != nil {
		e.sessionLog.Close()
		e.sessionLog = nil
	}
	if path == "" || strings.EqualFold(path, "off") {
		e.println("session-log = off")
		return
	}
	if e.options.OpenSessionLog == nil {
		e.fail(fmt.Errorf("session-log is not enabled by this host"))
		return
	}
	writer, err := e.options.OpenSessionLog(path)
	if err != nil {
		e.fail(err)
		return
	}
	e.sessionLog = writer
	fmt.Fprintf(writer, "\n=== session resumed %s ===\n", time.Now().UTC().Format(time.RFC3339))
	e.printf("session-log = %s\n", path)
}

func (e *Engine) writeSessionLog(line string) {
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
func (e *Engine) Expand(s string) string {
	variables := e.Variables()
	bytes := len(s)
	overflow := false
	return varRE.ReplaceAllStringFunc(s, func(match string) string {
		if overflow {
			return ""
		}
		m := varRE.FindStringSubmatch(match)
		name := m[1]
		if name == "" {
			name = m[2]
		}
		value := match
		if strings.HasPrefix(name, "env:") {
			value = e.lookupEnv(name[4:])
		} else if v, ok := variables[name]; ok {
			value = v
		}
		bytes += len(value) - len(match)
		if bytes > e.options.MaxSourceBytes {
			overflow = true
			e.fail(fmt.Errorf("expanded command exceeds source byte limit"))
			return ""
		}
		return value
	})
}

// evalCond understands:  $NAME == x | != x,  stdout contains "x",
// stderr contains "x", defined NAME, with optional leading `!` negation.
func (e *Engine) evalCond(c string) bool {
	variables := e.Variables()
	c = strings.TrimSpace(c)
	negate := false
	if strings.HasPrefix(c, "!") {
		negate = true
		c = strings.TrimSpace(c[1:])
	}
	// defined NAME
	if strings.HasPrefix(strings.ToLower(c), "defined ") {
		name := strings.TrimSpace(c[len("defined "):])
		_, ok := variables[name]
		return ok != negate
	}
	// stdout/stderr contains "x"
	if m := regexp.MustCompile(`^(stdout|stderr)\s+contains\s+(.+)$`).FindStringSubmatch(c); m != nil {
		stream := m[1]
		needle := strings.Trim(strings.TrimSpace(m[2]), `"'`)
		haystack := variables[stream]
		return strings.Contains(haystack, needle) != negate
	}
	// $NAME op value
	if m := regexp.MustCompile(`^\$([A-Za-z_][A-Za-z0-9_]*)\s*(==|!=|>=|<=|>|<)\s*(.*)$`).FindStringSubmatch(c); m != nil {
		got := variables[m[1]]
		op := m[2]
		want := strings.Trim(strings.TrimSpace(m[3]), `"'`)
		return cmpStrings(got, op, want) != negate
	}
	e.fail(fmt.Errorf("cannot evaluate condition: %q", c))
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

func splitFirst(s string) (string, string) {
	s = strings.TrimSpace(s)
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}
