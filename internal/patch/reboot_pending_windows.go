//go:build windows

package patch

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// RebootPending reports whether Windows currently has a pending reboot, and which
// indicators tripped. It reads ONLY the registry (milliseconds, no COM, no
// PowerShell) so it is cheap enough to run on every fast-lane cycle.
//
// Why it matters: an installed-but-pending patch is NOT fully effective until the
// machine reboots (the OS build only advances post-reboot), so the console must
// show per-device "reboot pending" to explain why a just-installed KB can still
// look missing/not-yet-applied.
func RebootPending() (pending bool, sources []string) {
	// CBS (servicing stack) flagged a reboot — the strongest signal for patches.
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) {
		sources = append(sources, "CBS")
	}
	// Windows Update requires a reboot to complete.
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		sources = append(sources, "WindowsUpdate")
	}
	// Files queued to be replaced/renamed on next boot (common after servicing).
	if pendingFileRename() {
		sources = append(sources, "PendingFileRename")
	}
	// A computer rename that has not taken effect yet.
	if pendingComputerRename() {
		sources = append(sources, "PendingComputerRename")
	}
	return len(sources) > 0, sources
}

// keyExists reports whether an HKLM subkey is present (its mere existence is the
// pending-reboot signal for the CBS/WindowsUpdate indicators).
func keyExists(path string) bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	_ = k.Close()
	return true
}

func pendingFileRename() bool {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	// Present and non-empty on both PendingFileRenameOperations and ...2.
	for _, name := range []string{"PendingFileRenameOperations", "PendingFileRenameOperations2"} {
		if vals, _, err := k.GetStringsValue(name); err == nil {
			for _, v := range vals {
				if strings.TrimSpace(v) != "" {
					return true
				}
			}
		}
	}
	return false
}

func pendingComputerRename() bool {
	active, err1 := regString(`SYSTEM\CurrentControlSet\Control\ComputerName\ActiveComputerName`, "ComputerName")
	pending, err2 := regString(`SYSTEM\CurrentControlSet\Control\ComputerName\ComputerName`, "ComputerName")
	if err1 != nil || err2 != nil {
		return false
	}
	return !strings.EqualFold(strings.TrimSpace(active), strings.TrimSpace(pending))
}

func regString(path, name string) (string, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
	if err != nil {
		return "", err
	}
	defer k.Close()
	s, _, err := k.GetStringValue(name)
	return s, err
}
