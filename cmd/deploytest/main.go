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
		fmt.Println("Usage: deploytest <path-to-.msu-or-.cab> [expected-sha256]")
		return
	}
	path := os.Args[1]
	expectedSHA256 := ""
	if len(os.Args) > 2 {
		expectedSHA256 = os.Args[2]
	}

	cfg := config.Load()
	logFile, err := logging.Init(cfg.LogLevel, cfg.LogFile, logging.Rotation{})
	if err != nil {
		fmt.Println("failed to initialize logging:", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	slog.Info("deploytest starting", "path", path)
	fmt.Printf("Installing local patch: %s\n", path)

	res, err := patch.InstallLocalPatch(path, expectedSHA256)
	if err != nil {
		fmt.Println("Error:", err)
		return
	}
	fmt.Printf("Success: %v, RebootRequired: %v, ExitCode: %d\nDetail: %s\n",
		res.Success, res.RebootRequired, res.ExitCode, res.Detail)
}
