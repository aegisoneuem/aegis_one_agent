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
	logFile, err := logging.Init(cfg.LogLevel, cfg.LogFile, logging.Rotation{})
	if err != nil {
		fmt.Println("failed to initialize logging:", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	slog.Info("localscan starting (Step A: local KB/build query, no network)")
	state, err := patch.CollectLocalState()
	if err != nil {
		// Fail loud: an unreadable local state is UNKNOWN, not "clean".
		fmt.Println("LOCAL SCAN FAILED (patch level UNKNOWN, not compliant):", err)
		return
	}

	fmt.Printf("Product:        %s\n", state.ProductName)
	fmt.Printf("DisplayVersion: %s\n", state.DisplayVersion)
	fmt.Printf("Build:          %s\n", state.FullBuild)
	fmt.Printf("Installed KBs:  %d\n\n", len(state.InstalledKBs))
	fmt.Println(strings.Join(state.InstalledKBs, "  "))
}
