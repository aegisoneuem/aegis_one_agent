package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNSHBackgroundEventsUseDedicatedLog(t *testing.T) {
	for _, mode := range []string{"AEGIS_NSH", "AEGIS_NSH_SCRIPT"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("AEGIS_NSH", "")
			t.Setenv("AEGIS_NSH_SCRIPT", "")
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

func TestNormalMockModeRetainsConsoleOutput(t *testing.T) {
	t.Setenv("AEGIS_NSH", "1")
	t.Setenv("AEGIS_NSH_SCRIPT", "old-script.nsh")
	old := mockOutput
	defer func() { mockOutput = old }()
	var terminal bytes.Buffer
	mockOutput = &terminal
	f, err := configureMockLogging(false)
	if err != nil || f != nil {
		t.Fatalf("%v %v", f, err)
	}
	mockPrintln("normal server event")
	if !strings.Contains(terminal.String(), "normal server event") {
		t.Fatal("console output missing")
	}
}
