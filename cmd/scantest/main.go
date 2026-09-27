package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
)

func main() {
	cfg := config.Load()
	logFile, err := logging.Init(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		fmt.Println("failed to initialize logging:", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	slog.Info("scan-test starting", "log_level", cfg.LogLevel, "log_file", cfg.LogFile)
	fmt.Println("Scanning for missing Windows updates...")

	updates, err := patch.ScanWindowsUpdates()
	if err != nil {
		// CRITICAL: never silently report "0 missing patches" on failure.
		// A scan failure must be distinguishable from a genuinely clean device.
		fmt.Println("SCAN FAILED (not the same as 'no missing patches'):", err)
		fmt.Println("This device's patch status is UNKNOWN, not compliant.")
		return
	}

	fmt.Printf("Found %d applicable update(s):\n\n", len(updates))
	byClass := map[string]int{}
	for _, u := range updates {
		byClass[u.Classification]++

		kb := "-"
		if len(u.KBArticleIDs) > 0 {
			kb = "KB" + strings.Join(u.KBArticleIDs, ", KB")
		}
		sev := u.MsrcSeverity
		if sev == "" {
			sev = "-"
		}

		fmt.Printf("- [%s] %s\n", u.Classification, u.Title)
		fmt.Printf("    kb=%s  type=%s  severity=%s  reboot=%v  size=%.1fMB\n",
			kb, u.Type, sev, u.RebootRequired, u.SizeMB)
		fmt.Printf("    updateID=%s rev=%d\n", u.UpdateID, u.RevisionNumber)
		if len(u.Products) > 0 {
			fmt.Printf("    products=%s\n", strings.Join(u.Products, ", "))
		}
		if len(u.CveIDs) > 0 {
			fmt.Printf("    cves=%s\n", strings.Join(u.CveIDs, ", "))
		}
		if len(u.SecurityBulletinIDs) > 0 {
			fmt.Printf("    bulletins=%s\n", strings.Join(u.SecurityBulletinIDs, ", "))
		}
		if len(u.SupersededUpdateIDs) > 0 {
			fmt.Printf("    supersedes=%d update(s)\n", len(u.SupersededUpdateIDs))
		}
	}

	fmt.Println("\nSummary by classification:")
	for class, n := range byClass {
		label := class
		if label == "" {
			label = "(uncategorized)"
		}
		fmt.Printf("  %-24s %d\n", label, n)
	}
}
