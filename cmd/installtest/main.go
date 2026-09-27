package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: installtest <KB-number-without-KB-prefix>")
		return
	}
	kb := os.Args[1]

	cfg := config.Load()
	logFile, err := logging.Init(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		fmt.Println("failed to initialize logging:", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	slog.Info("install-test starting", "kb", kb, "log_level", cfg.LogLevel, "log_file", cfg.LogFile)
	fmt.Printf("Installing update %s...\n", kb)

	result, err := patch.InstallWindowsUpdate(kb)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}

	fmt.Printf("Success: %v, RebootRequired: %v, Detail: %s\n",
		result.Success, result.RebootRequired, result.ErrorMessage)
}
