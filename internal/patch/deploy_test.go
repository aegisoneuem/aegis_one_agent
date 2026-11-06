package patch

import "testing"

func TestClassifyWusaExit(t *testing.T) {
	cases := []struct {
		name                                             string
		exit                                             int
		success, reboot, alreadyInstalled, notApplicable bool
	}{
		{"success", 0, true, false, false, false},
		{"reboot required 3010", 3010, true, true, false, false},
		{"reboot initiated 1641", 1641, true, true, false, false},
		{"already installed", 0x240006, false, false, true, false},
		{"not applicable (unsigned)", 0x80240017, false, false, false, true},
		{"not applicable (int32)", -2145124329, false, false, false, true},
		{"hard failure (wrong format 50 etc)", 50, false, false, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var r DeployResult
			classifyWusaExit(c.exit, &r)
			if r.Success != c.success || r.RebootRequired != c.reboot ||
				r.AlreadyInstalled != c.alreadyInstalled || r.NotApplicable != c.notApplicable {
				t.Errorf("exit %d → %+v, want success=%v reboot=%v already=%v notApplicable=%v",
					c.exit, r, c.success, c.reboot, c.alreadyInstalled, c.notApplicable)
			}
			// A non-success outcome must carry a reason.
			if !c.success && r.Reason == "" {
				t.Errorf("exit %d: non-success outcome has empty reason", c.exit)
			}
		})
	}
}
