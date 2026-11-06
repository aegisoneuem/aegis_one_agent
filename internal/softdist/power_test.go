package softdist

import "testing"

func TestACPowerBlocked(t *testing.T) {
	cases := []struct {
		name            string
		require         bool
		onAC, known     bool
		wantBlocked     bool
	}{
		{"not required -> never blocks", false, false, true, false},
		{"required, on AC -> allow", true, true, true, false},
		{"required, on battery -> BLOCK", true, false, true, true},
		{"required, power unknown (desktop/VM) -> allow", true, false, false, false},
		{"not required, on battery -> allow", false, false, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			blocked, reason := acPowerBlocked(c.require, c.onAC, c.known)
			if blocked != c.wantBlocked {
				t.Errorf("acPowerBlocked(%v,%v,%v) blocked=%v, want %v", c.require, c.onAC, c.known, blocked, c.wantBlocked)
			}
			if blocked && reason == "" {
				t.Error("blocked but empty reason")
			}
			if !blocked && reason != "" {
				t.Errorf("not blocked but reason set: %q", reason)
			}
		})
	}
}
