package main

import (
	"os"
	"runtime/debug"
	"time"
)

// tuneMemory keeps the agent's idle resident memory (RSS) low. The agent is a
// Go program, so it has a larger baseline than a C++ agent (the Go runtime +
// gRPC/TLS stacks), but most of the 40 MB a tester sees is memory the runtime
// FREED after a transient spike (inventory enumeration, a content fetch) yet has
// not returned to the OS. Two levers fix that:
//   - a soft memory limit, so the GC works harder before the heap grows; and
//   - a periodic FreeOSMemory, which returns freed pages to the OS so Task
//     Manager reflects real use.
//
// This matters because the agent is a long-lived background service on customer
// machines sharing CPU/RAM/disk with patching, softdist, RC and (soon) DEX — a
// smaller steady-state footprint is worth a little GC work. GOMEMLIMIT set in the
// environment always wins.
func tuneMemory() {
	if _, set := os.LookupEnv("GOMEMLIMIT"); !set {
		debug.SetMemoryLimit(96 << 20) // 96 MiB soft cap; only bites under real pressure
	}
	go func() {
		defer recoverLog("mem-trim")
		t := time.NewTicker(2 * time.Minute)
		defer t.Stop()
		for range t.C {
			debug.FreeOSMemory() // full GC + return freed pages to the OS
		}
	}()
}
