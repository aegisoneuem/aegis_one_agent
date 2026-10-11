//go:build windows

package patch

import "golang.org/x/sys/windows/registry"

// RebootPending reports whether Windows has a PATCH-relevant pending reboot, and
// which indicators tripped. It reads ONLY the registry (milliseconds, no COM, no
// PowerShell) so it is cheap enough to run on every fast-lane cycle.
//
// Only the servicing-stack signals are used — CBS (Component Based Servicing) and
// Windows Update — because those are what a patch sets when it needs a reboot.
// The generic Windows "pending reboot" flags (PendingFileRenameOperations,
// pending computer-rename) are deliberately NOT used: they are noisy (set by all
// kinds of non-patch operations and often linger on an idle machine), so they
// would falsely report a patch reboot as pending.
//
// Why it matters: an installed-but-pending patch is NOT fully effective until the
// machine reboots (the OS build only advances post-reboot), so the console must
// show per-device "reboot pending" to explain why a just-installed KB can still
// look not-yet-applied.
func RebootPending() (pending bool, sources []string) {
	// CBS flagged a reboot — the strongest, patch-specific signal.
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) {
		sources = append(sources, "CBS")
	}
	// Windows Update requires a reboot to complete a servicing operation.
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		sources = append(sources, "WindowsUpdate")
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
