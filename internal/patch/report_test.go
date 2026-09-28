package patch

import (
	"errors"
	"testing"
	"time"
)

// memBuffer is an in-memory buffer.Buffer for router tests.
type memBuffer struct {
	items []memItem
}

type memItem struct {
	kind    string
	payload []byte
}

func (m *memBuffer) Enqueue(kind string, payload []byte) error {
	m.items = append(m.items, memItem{kind, append([]byte(nil), payload...)})
	return nil
}

func (m *memBuffer) Drain(handle func(string, []byte) error) (int, error) {
	drained := 0
	for i, it := range m.items {
		if err := handle(it.kind, it.payload); err != nil {
			m.items = m.items[i:]
			return drained, nil
		}
		drained++
	}
	m.items = nil
	return drained, nil
}

func (m *memBuffer) Len() (int, error) { return len(m.items), nil }

func sampleState() *LocalState {
	return &LocalState{
		ProductName:    "Windows 11 Pro",
		DisplayVersion: "23H2",
		FullBuild:      "22631.7517",
		Architecture:   "amd64",
		InstalledKBs:   []string{"KB1", "KB2"},
		CollectedAt:    time.Now(),
	}
}

func TestReportOfflineQueues(t *testing.T) {
	buf := &memBuffer{}
	sent := 0
	err := Report(sampleState(), false, func(StateReport) error { sent++; return nil }, buf)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("offline must not send, sent=%d", sent)
	}
	if n, _ := buf.Len(); n != 1 {
		t.Fatalf("offline must queue 1, got %d", n)
	}
}

func TestReportOnlineDrainsBacklogThenSends(t *testing.T) {
	buf := &memBuffer{}
	// Simulate an earlier offline period leaving one report queued.
	if err := Report(sampleState(), false, nil, buf); err != nil {
		t.Fatal(err)
	}

	var sent []StateReport
	err := Report(sampleState(), true, func(r StateReport) error {
		sent = append(sent, r)
		return nil
	}, buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("want 2 sends (backlog + current), got %d", len(sent))
	}
	if n, _ := buf.Len(); n != 0 {
		t.Fatalf("buffer must be empty after online flush, got %d", n)
	}
}

func TestReportOnlineSendFailureBuffers(t *testing.T) {
	buf := &memBuffer{}
	err := Report(sampleState(), true, func(StateReport) error {
		return errors.New("network down")
	}, buf)
	if err != nil {
		t.Fatalf("send failure should be handled by buffering, got err=%v", err)
	}
	if n, _ := buf.Len(); n != 1 {
		t.Fatalf("failed send must buffer, got %d", n)
	}
}
