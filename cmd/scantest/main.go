package main

import (
	"fmt"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
)

func main() {
	fmt.Println("Scanning for missing Windows updates...")

	updates, err := patch.ScanWindowsUpdates()
	if err != nil {
		// CRITICAL: never silently report "0 missing patches" on failure.
		// A scan failure must be distinguishable from a genuinely clean device.
		fmt.Println("SCAN FAILED (not the same as 'no missing patches'):", err)
		fmt.Println("This device's patch status is UNKNOWN, not compliant.")
		return
	}

	fmt.Printf("Found %d missing update(s):\n", len(updates))
	for _, u := range updates {
		fmt.Printf("- [%s] %s (KB%s, %.1f MB)\n", u.Severity, u.Title, u.KB, u.SizeMB)
	}
}