// Command auditcheck reads an Aegis One audit.log (or an archived
// audit-<ts>.log / audit-TAMPERED-<ts>.log) and produces a human-
// readable transcript of every event plus a chain-integrity verdict.
//
// Intended for admins / auditors inspecting the local rolling buffer
// without hand-reading dense JSON lines.
//
// Usage:
//   auditcheck <path>              Verify and pretty-print every event.
//   auditcheck -v <path>           Verify only (compact "CHAIN OK / BROKEN" output).
//   auditcheck -j <path>           JSON passthrough (each event on its own line, no verify).
//
// Exit code 0 on chain intact, 1 on chain break, 2 on I/O error.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/auditlog"
)

func main() {
	verifyOnly := flag.Bool("v", false, "verify only (do not print events)")
	rawJSON := flag.Bool("j", false, "print events as JSON (no formatting)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  %s [-v | -j] <audit.log path>\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := args[0]

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		os.Exit(2)
	}
	lines := splitLines(data)
	if len(lines) == 0 {
		fmt.Println("(empty audit log)")
		return
	}

	// Verify chain first.
	brokenAt, brokenReason, err := auditlog.VerifyChain(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify %s: %v\n", path, err)
		os.Exit(2)
	}

	if !*verifyOnly {
		for i, raw := range lines {
			if *rawJSON {
				fmt.Printf("%d: %s\n", i, raw)
				continue
			}
			var ev auditlog.Event
			if err := json.Unmarshal(raw, &ev); err != nil {
				fmt.Printf("line %d: PARSE ERROR: %v\n", i, err)
				continue
			}
			prettyPrint(i, ev)
		}
		fmt.Println()
	}

	fmt.Printf("CHAIN SUMMARY: %d event(s), ", len(lines))
	if brokenAt == -1 {
		fmt.Println("INTEGRITY OK")
		return
	}
	fmt.Printf("INTEGRITY BROKEN at line %d\n  reason: %s\n", brokenAt, brokenReason)
	os.Exit(1)
}

func prettyPrint(idx int, ev auditlog.Event) {
	ts := time.UnixMilli(ev.TS).UTC().Format("2006-01-02 15:04:05.000 UTC")
	sigIcon := "✗"
	if ev.SignatureOK {
		sigIcon = "✓"
	}
	fmt.Printf("── line %d ─────────────────────────────────────────────\n", idx)
	fmt.Printf("  time    : %s\n", ts)
	fmt.Printf("  event   : %s   decision: %s   signature: %s\n", ev.EventType, ev.Decision, sigIcon)
	if ev.CommandID != "" {
		fmt.Printf("  cmd id  : %s\n", ev.CommandID)
	}
	if ev.CommandType != "" {
		fmt.Printf("  cmd type: %s\n", ev.CommandType)
	}
	if ev.Actor != "" {
		fmt.Printf("  actor   : %s\n", ev.Actor)
	}
	if ev.Source != "" {
		fmt.Printf("  source  : %s\n", ev.Source)
	}
	if ev.IssuedAt != 0 {
		fmt.Printf("  issued  : %s\n", time.Unix(ev.IssuedAt, 0).UTC().Format(time.RFC3339))
	}
	if ev.Reason != "" {
		fmt.Printf("  reason  : %s\n", ev.Reason)
	}
	fmt.Printf("  device  : %s\n", ev.DeviceID)
	fmt.Printf("  prev    : %s\n", shortHash(ev.PrevHash))
	fmt.Printf("  hash    : %s\n", shortHash(ev.LineHash))
}

func shortHash(h string) string {
	if h == "" {
		return "(empty — chain genesis)"
	}
	if len(h) > 16 {
		return h[:16] + "…"
	}
	return h
}

func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i <= len(data); i++ {
		if i == len(data) || data[i] == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	return out
}
