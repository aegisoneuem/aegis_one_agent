package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSVSHBackgroundEventsUseDedicatedLog(t *testing.T) {
	for _, mode := range []string{"AEGIS_SVSH", "AEGIS_SVSH_SCRIPT"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AEGIS_SVSH", "")
			t.Setenv("AEGIS_SVSH_SCRIPT", "")
			t.Setenv(mode, "1")
			path := filepath.Join(t.TempDir(), "logs", "server.log")
			t.Setenv("AEGIS_MOCK_LOG_FILE", path)
			oldOutput, oldLog := mockOutput, log.Writer()
			defer func() { mockOutput = oldOutput; log.SetOutput(oldLog) }()
			var terminal bytes.Buffer
			mockOutput = &terminal
			f, err := configureMockLogging(true)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			mockPrintf("Received heartbeat %s\n", "device")
			mockPrintln("Agent connected")
			log.Print("background warning")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if terminal.Len() != 0 {
				t.Fatal("background event leaked to terminal")
			}
			for _, want := range []string{"heartbeat", "Agent connected", "background warning"} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("missing log %q: %s", want, data)
				}
			}
		})
	}
}

func TestNormalMockModeTeesToConsoleAndFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "server.log")
	t.Setenv("AEGIS_MOCK_LOG_FILE", path)
	oldOutput, oldLog := mockOutput, log.Writer()
	defer func() { mockOutput = oldOutput; log.SetOutput(oldLog) }()
	var terminal bytes.Buffer
	mockOutput = &terminal

	f, err := configureMockLogging(false)
	if err != nil || f == nil {
		t.Fatalf("normal mode must open a log file: f=%v err=%v", f, err)
	}
	defer f.Close()

	mockPrintln("normal server event")
	// Tee: the console sink AND the file both receive the event.
	if !strings.Contains(terminal.String(), "normal server event") {
		t.Fatal("console output missing (tee)")
	}
	data, rerr := os.ReadFile(path)
	if rerr != nil || !strings.Contains(string(data), "normal server event") {
		t.Fatalf("file output missing (tee): %q err=%v", data, rerr)
	}
}
