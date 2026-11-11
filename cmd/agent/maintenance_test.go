package main

import (
	"context"
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/policy"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

func atHour(hour int) time.Time { return time.Date(2026, 1, 5, hour, 30, 0, 0, time.Local) }

func TestInMaintenanceWindow_Hours(t *testing.T) {
	cases := []struct {
		name       string
		start, end int
		hour       int
		want       bool
	}{
		{"no window = always", 0, 0, 3, true},
		{"normal inside", 2, 5, 3, true},
		{"normal start-inclusive", 2, 5, 2, true},
		{"normal end-exclusive", 2, 5, 5, false},
		{"normal before", 2, 5, 1, false},
		{"midnight inside late", 22, 3, 23, true},
		{"midnight inside early", 22, 3, 1, true},
		{"midnight outside", 22, 3, 5, false},
		{"midnight end-exclusive", 22, 3, 3, false},
	}
	for _, c := range cases {
		m := policy.Maintenance{StartHour: c.start, EndHour: c.end} // Days empty => no day gate
		if got := inMaintenanceWindow(m, atHour(c.hour)); got != c.want {
			t.Errorf("%s: inMaintenanceWindow(start=%d,end=%d,hour=%d)=%v want %v", c.name, c.start, c.end, c.hour, got, c.want)
		}
	}
}

func TestInMaintenanceWindow_Days(t *testing.T) {
	now := atHour(3)
	today := weekdayAbbr[now.Weekday()]
	other := "sun"
	if today == "sun" {
		other = "mon"
	}
	if !inMaintenanceWindow(policy.Maintenance{Days: []string{today}}, now) {
		t.Fatalf("today (%s) must be in window", today)
	}
	if inMaintenanceWindow(policy.Maintenance{Days: []string{other}}, now) {
		t.Fatalf("other day (%s) must be outside window", other)
	}
}

func TestDeferredQueue_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cmd := &pb.Command{CommandId: "install-42", Type: "install_patch", Payload: []byte("x")}

	if err := persistDeferredCommand(dir, cmd); err != nil {
		t.Fatalf("persist: %v", err)
	}
	got := loadDeferredCommands(dir)
	if len(got) != 1 || got[0].CommandId != "install-42" || got[0].Type != "install_patch" {
		t.Fatalf("load mismatch: %+v", got)
	}
	removeDeferredCommand(dir, "install-42")
	if len(loadDeferredCommands(dir)) != 0 {
		t.Fatalf("remove failed")
	}
}

// A gated command outside the window is deferred (persisted, not run); draining
// in an open window runs it and clears the queue.
func TestDispatch_MaintenanceDeferThenDrain(t *testing.T) {
	dir := t.TempDir()
	ran := 0
	d := &dispatcher{
		cfg:    config.Config{SecureDir: dir, DeviceID: "dev"},
		outbox: make(chan *pb.AgentMessage, 64),
		handlers: map[string]taskHandler{
			"reboot": func(context.Context, pb.AgentControlPlaneClient, *pb.Command) { ran++ },
		},
	}
	// Deferred commands are re-verified on drain; stub that out for the test.
	prevVerify := verifyDeferredCommand
	verifyDeferredCommand = func(*pb.Command) bool { return true }
	t.Cleanup(func() { verifyDeferredCommand = prevVerify })

	h := time.Now().Hour()
	closed := policy.Policy{Version: 1, PolicyID: "p", IssuedAt: 1, Maintenance: policy.Maintenance{StartHour: (h + 2) % 24, EndHour: (h + 3) % 24}}
	setActivePolicyForTest(t, &closed)

	cmd := &pb.Command{CommandId: "reboot-1", Type: "reboot", Payload: []byte("{}")}
	d.dispatch(context.Background(), nil, cmd)

	if ran != 0 {
		t.Fatalf("command must NOT run outside the window")
	}
	if len(loadDeferredCommands(dir)) != 1 {
		t.Fatalf("command must be persisted to the deferred queue")
	}

	// Open the window and drain.
	open := closed
	open.Maintenance = policy.Maintenance{StartHour: h, EndHour: (h + 1) % 24}
	setActivePolicyForTest(t, &open)

	d.drainDeferred(context.Background(), nil)
	if ran != 1 {
		t.Fatalf("deferred command must run when the window opens: ran=%d", ran)
	}
	if len(loadDeferredCommands(dir)) != 0 {
		t.Fatalf("deferred queue must be empty after drain")
	}
}
