package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Set once at startup, before any RPC or terminal goroutine starts. Server
// events never share NSH's stdout/stderr; operator command output still does.
var mockOutput io.Writer = os.Stdout

func mockPrintf(format string, args ...any) { fmt.Fprintf(mockOutput, format, args...) }
func mockPrintln(args ...any)               { fmt.Fprintln(mockOutput, args...) }

// Startup/serve failures must remain visible even when routine events go to file.
func mockFatalf(format string, args ...any) {
	log.Printf(format, args...)
	if file, ok := mockOutput.(*os.File); ok && file != os.Stdout {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
	os.Exit(1)
}

func configureMockLogging() (*os.File, error) {
	if !truthy(os.Getenv("AEGIS_NSH")) && strings.TrimSpace(os.Getenv("AEGIS_NSH_SCRIPT")) == "" {
		return nil, nil
	}
	path := getenv("AEGIS_MOCK_LOG_FILE", filepath.Join("logs", "mockserver.log"))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	mockOutput = f
	log.SetOutput(f)
	return f, nil
}
