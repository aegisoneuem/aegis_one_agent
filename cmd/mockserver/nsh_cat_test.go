package main

import "testing"

func TestParseCatEndpoint(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPath string
	}{
		{`C:\tmp\x.txt`, "", `C:\tmp\x.txt`},
		{`\\myvm\C\tmp\x.txt`, "myvm", `C:\tmp\x.txt`},
		{`\\myvm\c\tmp\x.txt`, "myvm", `C:\tmp\x.txt`}, // drive uppercased
		{`\\laptop-01\D\logs\deploy.log`, "laptop-01", `D:\logs\deploy.log`},
		{`\\host`, "", `\\host`}, // malformed → fallthrough
	}
	for _, c := range cases {
		got := parseCatEndpoint(c.in)
		if got.host != c.wantHost || got.path != c.wantPath {
			t.Errorf("parseCatEndpoint(%q) = {%q,%q}, want {%q,%q}", c.in, got.host, got.path, c.wantHost, c.wantPath)
		}
	}
}

func TestParseCatRedirect(t *testing.T) {
	type want struct {
		src, dst   catEndpoint
		appendMode bool
	}
	cases := []struct {
		in       string
		want     want
		wantErr  bool
	}{
		{
			in: `C:\a > C:\b`,
			want: want{
				src: catEndpoint{path: `C:\a`}, dst: catEndpoint{path: `C:\b`}, appendMode: false,
			},
		},
		{
			in: `\\host1\C\a.txt >> \\host2\C\b.txt`,
			want: want{
				src: catEndpoint{host: "host1", path: `C:\a.txt`},
				dst: catEndpoint{host: "host2", path: `C:\b.txt`},
				appendMode: true,
			},
		},
		{
			in: `\\host1\c\log.txt > D:\save.txt`,
			want: want{
				src: catEndpoint{host: "host1", path: `C:\log.txt`},
				dst: catEndpoint{path: `D:\save.txt`},
			},
		},
		{in: `justOne`, wantErr: true},
		{in: ``, wantErr: true},
		{in: `> onlyRHS`, wantErr: true},
		{in: `lhs >`, wantErr: true},
	}
	for _, c := range cases {
		src, dst, appendMode, err := parseCatRedirect(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("parseCatRedirect(%q) err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if c.wantErr {
			continue
		}
		if src != c.want.src {
			t.Errorf("parseCatRedirect(%q) src = %+v, want %+v", c.in, src, c.want.src)
		}
		if dst != c.want.dst {
			t.Errorf("parseCatRedirect(%q) dst = %+v, want %+v", c.in, dst, c.want.dst)
		}
		if appendMode != c.want.appendMode {
			t.Errorf("parseCatRedirect(%q) appendMode = %v, want %v", c.in, appendMode, c.want.appendMode)
		}
	}
}

func TestCatPathDescr(t *testing.T) {
	cases := []struct {
		e    catEndpoint
		want string
	}{
		{catEndpoint{path: `C:\tmp\x.txt`}, `C:\tmp\x.txt`},
		{catEndpoint{host: "myvm", path: `C:\tmp\x.txt`}, `\\myvm\C\tmp\x.txt`},
		{catEndpoint{host: "host1", path: `D:\logs\a.log`}, `\\host1\D\logs\a.log`},
	}
	for _, c := range cases {
		got := catPathDescr(c.e)
		if got != c.want {
			t.Errorf("catPathDescr(%+v) = %q, want %q", c.e, got, c.want)
		}
	}
}

func TestCatIndexToken_WhitespaceBounded(t *testing.T) {
	// `>>` should not match when adjacent to non-whitespace (e.g. inside a path).
	cases := []struct {
		s, tok string
		want   int
	}{
		{`a >> b`, `>>`, 2},
		{`a > b`, `>`, 2},
		{`a>>b`, `>>`, -1},   // no whitespace — not a token
		{`a>b`, `>`, -1},     // no whitespace — not a token
		{`>> b`, `>>`, 0},    // ok at the start
		{`a >>`, `>>`, 2},    // ok at the end
	}
	for _, c := range cases {
		got := catIndexToken(c.s, c.tok)
		if got != c.want {
			t.Errorf("catIndexToken(%q,%q) = %d, want %d", c.s, c.tok, got, c.want)
		}
	}
}
