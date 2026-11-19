package main

import (
	"fmt"
	"strings"
)

// SVSH REPL-wide output redirection (`> dst` / `>> dst`) — extends UC-22
// cat semantics to ANY verb that produces stdout. The pre-parser runs
// BEFORE verb dispatch: it strips the trailing redirect from the input
// line and tells the caller where to write the captured output.
//
// Design (kept tight so verb args that happen to contain `>` are not
// misread):
//   - `>` or `>>` MUST be a WHITESPACE-BOUNDED token (space on both
//     sides). Mid-string `>` inside cmd arg survives untouched.
//   - Destination is the ENTIRE remainder after the token, trimmed. The
//     destination may be local (`C:\tmp\x.txt`) or UNC
//     (`\\host\C\tmp\x.txt`); the writer layer handles both.
//   - The `cat` verb is EXEMPT — it has its own `>` / `>>` parser with
//     its own semantics (source → dest file copy). Double-parsing would
//     corrupt cat usage.
//   - If parse finds a redirect but destination is empty, we return an
//     error so the caller can surface a usage line rather than silently
//     dropping output into the void.

// svshRedirectResult describes what the pre-parser found. When Redirect
// is true, StrippedLine is the verb+args with the redirect removed and
// Destination + Append describe where output should go. When Redirect
// is false, StrippedLine == original line.
type svshRedirectResult struct {
	Redirect     bool
	StrippedLine string
	Destination  catEndpoint // reused from UC-22 cat parser
	Append       bool
}

// parseSVSHRedirect runs the pre-parser on one full SVSH input line. It
// returns the result + any usage error (empty destination or bad path).
// Caller passes the FIRST verb word so we can exempt `cat`.
func parseSVSHRedirect(line string) (svshRedirectResult, error) {
	line = strings.TrimRight(line, " \t")
	if line == "" {
		return svshRedirectResult{StrippedLine: line}, nil
	}

	// First word = verb. If it's `cat`, skip parsing — cat owns its own
	// redirect semantics.
	verb := firstWord(line)
	if strings.EqualFold(verb, "cat") {
		return svshRedirectResult{StrippedLine: line}, nil
	}

	// Locate the LAST whitespace-bounded `>>` or `>` — the trailing
	// redirect. Both are scanned; whichever occurs later wins so
	// shell-style `a > b >> c` reads as append to c (NOT the inner `>`).
	iGT := lastIndexToken(line, ">")
	iAPP := lastIndexToken(line, ">>")
	var tokIdx int
	var tokLen int
	var appendMode bool
	switch {
	case iAPP >= 0 && iAPP >= iGT:
		tokIdx, tokLen, appendMode = iAPP, 2, true
	case iGT >= 0:
		tokIdx, tokLen, appendMode = iGT, 1, false
	default:
		return svshRedirectResult{StrippedLine: line}, nil
	}

	left := strings.TrimRight(line[:tokIdx], " \t")
	right := strings.TrimSpace(line[tokIdx+tokLen:])
	if left == "" {
		return svshRedirectResult{StrippedLine: line}, fmt.Errorf("SVSH redirect: verb side is empty (did you mean `cat <src> > <dst>`?)")
	}
	if right == "" {
		sep := ">"
		if appendMode {
			sep = ">>"
		}
		return svshRedirectResult{StrippedLine: line}, fmt.Errorf("SVSH redirect: `%s` has no destination path", sep)
	}

	return svshRedirectResult{
		Redirect:     true,
		StrippedLine: left,
		Destination:  parseCatEndpoint(right),
		Append:       appendMode,
	}, nil
}

// lastIndexToken returns the index of the LAST whitespace-bounded tok
// in s, or -1 when absent. Companion to catIndexToken (first-match)
// which cat uses; the SVSH pre-parser needs last-match so shell-style
// `a > b > c` reads as redirect to c.
func lastIndexToken(s, tok string) int {
	best := -1
	for i := 0; i+len(tok) <= len(s); i++ {
		if s[i:i+len(tok)] != tok {
			continue
		}
		// Exclude the `>` that is really the first char of a `>>`
		// occurrence at the same position — the caller handles `>>`
		// separately.
		if tok == ">" && i+1 < len(s) && s[i+1] == '>' {
			continue
		}
		// And exclude a `>` immediately AFTER a `>` so we don't match
		// the second char of a `>>`.
		if tok == ">" && i > 0 && s[i-1] == '>' {
			continue
		}
		before := i == 0 || s[i-1] == ' ' || s[i-1] == '\t'
		after := i+len(tok) == len(s) || s[i+len(tok)] == ' ' || s[i+len(tok)] == '\t'
		if before && after {
			best = i
		}
	}
	return best
}

// firstWord returns the first whitespace-separated token in s, or empty
// when s is blank. Case preserved so the caller can decide whether to
// lowercase.
func firstWord(s string) string {
	s = strings.TrimLeft(s, " \t")
	i := strings.IndexAny(s, " \t")
	if i < 0 {
		return s
	}
	return s[:i]
}

// svshRedirectMaxBytes is the soft cap on captured output per verb
// invocation. Mirrors the cat soft cap so an operator pointing a verb at
// a UNC destination gets the same ceiling. The agent's write_bytes path
// enforces its own AEGIS_FILE_WRITE_MAX_BYTES cap on UNC writes.
const svshRedirectMaxBytes = 100 * 1024 * 1024

// cappedBuffer is a bytes buffer with a hard ceiling. When full, further
// writes return the bytes consumed but drop the overflow so a runaway
// verb cannot exhaust memory. Truncation is reported via Overflowed().
type cappedBuffer struct {
	buf      []byte
	max      int
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.overflow {
		return len(p), nil
	}
	room := c.max - len(c.buf)
	if room <= 0 {
		c.overflow = true
		return len(p), nil
	}
	if len(p) > room {
		c.buf = append(c.buf, p[:room]...)
		c.overflow = true
		return len(p), nil
	}
	c.buf = append(c.buf, p...)
	return len(p), nil
}

func (c *cappedBuffer) Bytes() []byte     { return c.buf }
func (c *cappedBuffer) Overflowed() bool  { return c.overflow }
func (c *cappedBuffer) Size() int         { return len(c.buf) }
func newCappedBuffer(max int) *cappedBuffer { return &cappedBuffer{buf: make([]byte, 0, 4096), max: max} }

// execInputLine is the REPL's input entry point. Both the interactive
// loop (readline) and the script readLoop route through here BEFORE
// handing the line to the scripting engine. The pre-parser strips a
// trailing `> dst` / `>> dst` so verb output is captured into a size-
// capped buffer and flushed to the destination. On no redirect (or on
// a parse error) the original line goes to engine.execLine unchanged.
//
// This replaces the previous direct engine.execLine calls — the old
// path missed the pre-parser, which is why redirect fell through to
// the verbs' own argument handling (e.g. `inventory apps > apps.txt`
// treated the redirect as a filter string).
func (s *svshSession) execInputLine(line string) bool {
	// dispatch selects the real runtime (engine when present) or the
	// per-verb exec() fallback (used by test harnesses that don't stand
	// up a full scripting engine). Pre-parser runs BEFORE either.
	dispatch := func(l string) bool {
		if s.engine != nil {
			return s.engine.execLine(l)
		}
		return s.exec(l)
	}
	red, err := parseSVSHRedirect(line)
	if err != nil {
		s.printf("svsh: %v\n", err)
		return true
	}
	if !red.Redirect {
		return dispatch(line)
	}
	keepGoing := true
	if werr := s.runWithRedirect(red.Destination, red.Append, func() {
		keepGoing = dispatch(red.StrippedLine)
	}); werr != nil {
		s.printf("svsh: redirect write failed: %v\n", werr)
	}
	return keepGoing
}

// runWithRedirect swaps the session's output writer for a size-capped
// buffer, runs the dispatch callback (the verb), then flushes the
// buffer to the destination. stderr stream is NOT redirected — errors
// stay on the console so the operator still sees failures.
//
// Append mode and destination (local vs UNC) are handled by catWriteDest
// which is already battle-tested through UC-22. Caller holds the
// stripped line and dispatches it; this helper is agnostic.
func (s *svshSession) runWithRedirect(dst catEndpoint, appendMode bool, dispatch func()) error {
	capBuf := newCappedBuffer(svshRedirectMaxBytes)
	savedOutput := s.output
	s.output = capBuf
	dispatch()
	s.output = savedOutput

	if capBuf.Overflowed() {
		s.printf("svsh: redirect output exceeded %d bytes — captured bytes truncated at the cap before writing\n", svshRedirectMaxBytes)
	}
	if capBuf.Size() == 0 {
		// Nothing to write. Treat as a successful no-op and still touch
		// the file so the operator sees the empty result.
		return s.catWriteDest(dst, []byte{}, appendMode)
	}
	return s.catWriteDest(dst, capBuf.Bytes(), appendMode)
}
