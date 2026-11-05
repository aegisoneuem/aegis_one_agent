package main

import (
	"testing"
)

// B.1 unit tests — happy path for NSH redirect pre-parser.

func TestParseNSHRedirect_HappyPath(t *testing.T) {
	cases := []struct {
		in             string
		wantRedirect   bool
		wantStripped   string
		wantDstHost    string
		wantDstPath    string
		wantAppend     bool
	}{
		{`hosts > hosts.txt`, true, "hosts", "", "hosts.txt", false},
		{`inventory apps > fleet.csv`, true, "inventory apps", "", "fleet.csv", false},
		{`run dir C:\ > C:\tmp\scan.log`, true, `run dir C:\`, "", `C:\tmp\scan.log`, false},
		{`help >> session-ref.txt`, true, "help", "", "session-ref.txt", true},
		{`run ipconfig > \\WIN-TUES4V46T7I\C\tmp\ip.txt`, true, "run ipconfig", "WIN-TUES4V46T7I", `C:\tmp\ip.txt`, false},
		{`run echo hi >> \\WIN-VM\c\logs\log.txt`, true, "run echo hi", "WIN-VM", `C:\logs\log.txt`, true},
	}
	for _, c := range cases {
		got, err := parseNSHRedirect(c.in)
		if err != nil {
			t.Errorf("parseNSHRedirect(%q) error: %v", c.in, err)
			continue
		}
		if got.Redirect != c.wantRedirect {
			t.Errorf("%q: Redirect = %v, want %v", c.in, got.Redirect, c.wantRedirect)
		}
		if got.StrippedLine != c.wantStripped {
			t.Errorf("%q: StrippedLine = %q, want %q", c.in, got.StrippedLine, c.wantStripped)
		}
		if got.Destination.host != c.wantDstHost {
			t.Errorf("%q: dst.host = %q, want %q", c.in, got.Destination.host, c.wantDstHost)
		}
		if got.Destination.path != c.wantDstPath {
			t.Errorf("%q: dst.path = %q, want %q", c.in, got.Destination.path, c.wantDstPath)
		}
		if got.Append != c.wantAppend {
			t.Errorf("%q: Append = %v, want %v", c.in, got.Append, c.wantAppend)
		}
	}
}

// No redirect → StrippedLine == original, Redirect == false, no error.
func TestParseNSHRedirect_NoRedirect(t *testing.T) {
	cases := []string{
		`hosts`,
		`run dir C:\`,
		`help`,
		`inventory apps`,
		``,
		`   `,
	}
	for _, in := range cases {
		got, err := parseNSHRedirect(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
		}
		if got.Redirect {
			t.Errorf("%q: Redirect = true, want false", in)
		}
	}
}

// `cat` verb is EXEMPT — it has its own redirect semantics (source →
// dest copy). The pre-parser must leave it alone.
func TestParseNSHRedirect_CatVerbExempt(t *testing.T) {
	cases := []string{
		`cat C:\src.txt > C:\dst.txt`,
		`cat \\h1\C\a > \\h2\C\b`,
		`cat big.bin >> C:\collected.bin`,
		`CAT src > dst`,  // case-insensitive
	}
	for _, in := range cases {
		got, err := parseNSHRedirect(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
		}
		if got.Redirect {
			t.Errorf("%q: cat verb redirect was NOT exempted (would conflict with cat's own parser)", in)
		}
		if got.StrippedLine != in {
			t.Errorf("%q: cat line was mutated: got %q", in, got.StrippedLine)
		}
	}
}

// BREAK 1: `>` or `>>` without destination path → usage error, not
// silent drop.
func TestParseNSHRedirect_Break_EmptyDestination(t *testing.T) {
	cases := []string{
		`run hostname >`,
		`run hostname >>`,
		`hosts >   `,
	}
	for _, in := range cases {
		_, err := parseNSHRedirect(in)
		if err == nil {
			t.Errorf("%q: expected usage error on empty destination", in)
		}
	}
}

// BREAK 2: redirect without a verb — `> file.txt` or `>> file.txt`.
// Must error so caller surfaces a clear line.
func TestParseNSHRedirect_Break_EmptyVerb(t *testing.T) {
	cases := []string{
		`> dst.txt`,
		`>> dst.txt`,
	}
	for _, in := range cases {
		_, err := parseNSHRedirect(in)
		if err == nil {
			t.Errorf("%q: expected usage error on empty verb side", in)
		}
	}
}

// BREAK 3: multiple redirect tokens — `a > b > c`. Last wins so
// `a > b` is the stripped line and `c` is the destination.
func TestParseNSHRedirect_Break_MultipleTokensLastWins(t *testing.T) {
	got, err := parseNSHRedirect(`echo hi > file1 > file2`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Redirect {
		t.Fatal("expected redirect")
	}
	if got.StrippedLine != `echo hi > file1` {
		t.Errorf("StrippedLine = %q, want %q", got.StrippedLine, `echo hi > file1`)
	}
	if got.Destination.path != "file2" {
		t.Errorf("dst.path = %q, want %q", got.Destination.path, "file2")
	}
}

// BREAK 4: `>>` must be preferred over inner `>` in `a > b >> c`.
func TestParseNSHRedirect_Break_AppendBeatsSingleArrow(t *testing.T) {
	got, _ := parseNSHRedirect(`run echo hi > middle >> final.txt`)
	if !got.Redirect {
		t.Fatal("expected redirect")
	}
	if !got.Append {
		t.Error("Append = false, want true (>>)")
	}
	if got.StrippedLine != `run echo hi > middle` {
		t.Errorf("StrippedLine = %q, want %q", got.StrippedLine, `run echo hi > middle`)
	}
}

// BREAK 5: mid-string `>` inside a command argument must NOT be
// parsed as a redirect — the token must be whitespace-bounded.
func TestParseNSHRedirect_Break_MidStringAngleIgnored(t *testing.T) {
	cases := []string{
		`run powershell "Get-Item 'file>name.txt'"`,
		`run echo 1>&2`,  // cmd.exe stderr redirect NOT whitespace-bounded
		`run echo a>b`,
	}
	for _, in := range cases {
		got, err := parseNSHRedirect(in)
		if err != nil {
			t.Errorf("%q: unexpected error %v", in, err)
		}
		if got.Redirect {
			t.Errorf("%q: mid-string > triggered false redirect (StrippedLine=%q, dst=%q)", in, got.StrippedLine, got.Destination.path)
		}
	}
}

// BREAK 6: empty input returns no redirect, no error.
func TestParseNSHRedirect_Break_EmptyInput(t *testing.T) {
	got, err := parseNSHRedirect(``)
	if err != nil {
		t.Errorf("empty input error: %v", err)
	}
	if got.Redirect {
		t.Error("empty input triggered redirect")
	}
}

// ---- cappedBuffer tests ----

// Normal write below cap: all bytes stored, no overflow.
func TestCappedBuffer_BelowCap(t *testing.T) {
	b := newCappedBuffer(100)
	n, err := b.Write([]byte("hello"))
	if err != nil || n != 5 {
		t.Errorf("Write short: n=%d err=%v", n, err)
	}
	if b.Overflowed() {
		t.Error("overflow flag set under cap")
	}
	if string(b.Bytes()) != "hello" {
		t.Errorf("buf = %q", b.Bytes())
	}
}

// Over-cap write truncates at the cap AND flags overflow.
func TestCappedBuffer_OverCapTruncates(t *testing.T) {
	b := newCappedBuffer(5)
	n, _ := b.Write([]byte("hello world"))
	if n != 11 {
		t.Errorf("Write should report consumed=11 (caller doesn't retry), got %d", n)
	}
	if !b.Overflowed() {
		t.Error("overflow flag NOT set after cap exceeded")
	}
	if string(b.Bytes()) != "hello" {
		t.Errorf("truncated buf = %q, want %q", b.Bytes(), "hello")
	}
}

// Subsequent writes after overflow are dropped (no panic, no growth).
func TestCappedBuffer_WritesAfterOverflowDropped(t *testing.T) {
	b := newCappedBuffer(5)
	b.Write([]byte("hello world"))
	before := len(b.Bytes())
	b.Write([]byte("more data"))
	after := len(b.Bytes())
	if before != after {
		t.Errorf("post-overflow write grew buf: %d -> %d", before, after)
	}
}

// BREAK: 0-byte write is a safe no-op.
func TestCappedBuffer_Break_EmptyWrite(t *testing.T) {
	b := newCappedBuffer(10)
	n, err := b.Write(nil)
	if err != nil || n != 0 {
		t.Errorf("empty write: n=%d err=%v", n, err)
	}
	if b.Overflowed() {
		t.Error("empty write should not overflow")
	}
}
