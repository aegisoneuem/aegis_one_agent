// Command auditcheck reads an Aegis One audit.log (or an archived
// audit-<ts>.log) and pretty-prints every event. Intended for field L1
// engineers inspecting the local JSONL trace on an endpoint without
// server portal access. The server-side audit log is the authoritative
// compliance record; this tool is a convenience over the local trace.
//
// Usage:
//   auditcheck <path>              Pretty-print every event.
//   auditcheck -j <path>           JSON passthrough (each event on its own line).
//
// Exit code 0 on success, 2 on I/O error.
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
	rawJSON := flag.Bool("j", false, "print events as JSON (no formatting)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  %s [-j] <audit.log path>\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		os.Exit(2)
	}
	path := args[0]

	events, err := auditlog.Read(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read %s: %v\n", path, err)
		os.Exit(2)
	}
	if len(events) == 0 {
		fmt.Println("(empty audit log)")
		return
	}
	for i, ev := range events {
		if *rawJSON {
			raw, _ := eventToJSON(ev)
			fmt.Printf("%d: %s\n", i, raw)
			continue
		}
		prettyPrint(i, ev)
	}
	fmt.Printf("\n%d event(s).\n", len(events))
}

func eventToJSON(ev auditlog.Event) (string, error) {
	data, err := json.Marshal(ev)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func prettyPrint(idx int, ev auditlog.Event) {
	ts := time.UnixMilli(ev.TS).UTC().Format("2006-01-02 15:04:05.000 UTC")
	sigIcon := "no"
	if ev.SignatureOK {
		sigIcon = "ok"
	}
	fmt.Printf("-- line %d --------------------------------------------\n", idx)
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
	if ev.Details != "" {
		fmt.Printf("  details : %s\n", ev.Details)
	}
	fmt.Printf("  device  : %s\n", ev.DeviceID)
}
