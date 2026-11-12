package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
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

// configureMockLogging sets up the server log file. In NSH mode the file is the
// sole sink (NSH owns the terminal). In normal server mode it TEES: everything
// still prints to the console AND is saved to a dated file under logs/ (or
// AEGIS_MOCK_LOG_FILE), so a tester can review what happened after the fact — and
// so a crash/reboot of the box does not lose the scrollback.
func configureMockLogging(nshMode bool) (*os.File, error) {
	path := getenv("AEGIS_MOCK_LOG_FILE", filepath.Join("logs", fmt.Sprintf("mockserver-%s.log", time.Now().Format("2006-01-02"))))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if nshMode {
		mockOutput = f
		log.SetOutput(f)
	} else {
		mockOutput = io.MultiWriter(os.Stdout, f)
		log.SetOutput(io.MultiWriter(os.Stderr, f))
		fmt.Fprintf(os.Stdout, "server log also saved to %s\n", path)
	}
	return f, nil
}
