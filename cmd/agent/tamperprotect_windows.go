//go:build windows

package main

import (
	"log/slog"

	"golang.org/x/sys/windows"
)

// Service DACLs (SDDL). Rights: CC=query-config DC=change-config LC=query-status
// SW=enum-deps RP=start WP=stop DT=pause LO=interrogate CR=user-defined
// SD=delete RC=read-control WD=write-dac WO=write-owner.
const (
	// Locked (self-protected): SYSTEM full; Administrators get query + START + read
	// but NOT stop(WP)/change-config(DC=disable)/pause(DT)/delete(SD); Interactive
	// users read-only. Protected (no inherited grants).
	serviceDACLLocked = "D:P(A;;CCLCSWRPWPDTLOCRSDRCWDWO;;;SY)(A;;CCLCSWRPLORC;;;BA)(A;;CCLCSWLORC;;;IU)"
	// Open (restore): SYSTEM + Administrators full; Interactive read. Lets normal
	// management (stop/disable/delete) work again once protection is off.
	serviceDACLOpen = "D:(A;;CCLCSWRPWPDTLOCRSDRCWDWO;;;SY)(A;;CCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)"
)

func applyServiceDACL(sddl string, protected bool) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protected {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		info |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	return windows.SetNamedSecurityInfo(serviceName, windows.SE_SERVICE, info, nil, nil, dacl, nil)
}

// applyServiceDACLForState locks or restores the service DACL to match the self-
// protect state. Only touches the service when running AS the service (not from a
// console/CLI invocation). Self-healing: re-applied at startup + on runtime change.
//
// Honest bound: an admin can take-ownership of the service and reset the DACL; this
// blocks the NORMAL tools (sc config disabled / sc delete / services.msc) + raises
// the bar + is undone only deliberately. SYSTEM keeps full control, so the agent and
// a SYSTEM-context MSI uninstall still work.
func applyServiceDACLForState(selfProtect bool) {
	if !runningAsService {
		return
	}
	var err error
	if selfProtect {
		err = applyServiceDACL(serviceDACLLocked, true)
	} else {
		err = applyServiceDACL(serviceDACLOpen, false)
	}
	if err != nil {
		slog.Warn("tamper-protect: service DACL update failed", "locked", selfProtect, "error", err.Error())
		return
	}
	hardenServiceRegistryKey(selfProtect)
	slog.Debug("tamper-protect: service DACL applied", "locked", selfProtect)
}

// hardenServiceRegistryKey ACLs HKLM\SYSTEM\CurrentControlSet\Services\<svc> so a
// non-SYSTEM admin cannot WRITE it (e.g. regedit Start=4 to disable the service) —
// the service DACL guards the SCM object, not this registry key. SYSTEM keeps full
// control (the SCM + the agent run as SYSTEM); Administrators/Users get read only.
// Honest bound: an admin can take-ownership of the key and reset it — the watchdog
// re-applies this, so the window is small.
func hardenServiceRegistryKey(locked bool) {
	name := `MACHINE\SYSTEM\CurrentControlSet\Services\` + serviceName
	sddl := "D:(A;CI;KA;;;SY)(A;CI;KA;;;BA)(A;CI;KR;;;BU)" // open: SYSTEM+Admins full
	protected := false
	if locked {
		sddl = "D:P(A;CI;KA;;;SY)(A;CI;KR;;;BA)(A;CI;KR;;;BU)" // SYSTEM full; Admins/Users READ only
		protected = true
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return
	}
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
	if protected {
		info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
	} else {
		info |= windows.UNPROTECTED_DACL_SECURITY_INFORMATION
	}
	if err := windows.SetNamedSecurityInfo(name, windows.SE_REGISTRY_KEY, info, nil, nil, dacl, nil); err != nil {
		slog.Debug("tamper-protect: service registry-key ACL update failed", "error", err.Error())
	}
}

// hardenDirACL sets a PROTECTED (inheritance-broken) DACL on dir:
//   - SYSTEM (SY)            : full control
//   - Administrators (BA)    : full control
//   - Users (BU)            : read + execute only (no write/modify/delete)
// OICI = object + container inherit, so files/subdirs created afterward inherit it.
//
// A non-admin user therefore cannot modify or delete any agent file. The ACL is
// applied to the tree root; children with inheritance enabled pick it up.
func hardenDirACL(dir string) error {
	const sddl = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FRFX;;;BU)"
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		dir,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
}
