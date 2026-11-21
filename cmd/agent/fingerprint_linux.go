//go:build linux

package main

import (
	"os"
	"strings"
)

// Linux hardware identity signals (no PowerShell/WMI equivalents needed):
//   - SMBIOS System UUID from /sys/class/dmi/id/product_uuid (root-readable; the
//     direct analogue of the Windows SMBIOS UUID; absent in some VMs/containers).
//   - /etc/machine-id (stable per-install id; the analogue of the Windows MachineGuid).
// TPM EK on Linux (tpm2/ /dev/tpmrm0) is a later enhancement; best-effort, like Windows.
//
// Same contract as the Windows build: signals are tagged, strongest first, and the
// persisted set is matched with "any overlap = same machine" for clone detection.

func readTrimFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func systemUUIDValue() string {
	return strings.ToUpper(readTrimFile("/sys/class/dmi/id/product_uuid"))
}

func machineIDValue() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if v := readTrimFile(p); v != "" {
			return v
		}
	}
	return ""
}

func hardwareIDs() (tpmEK, systemUUID, machineGUID string) {
	return "", systemUUIDValue(), machineIDValue()
}

func hardwareSignals() []string {
	var s []string
	if u := systemUUIDValue(); u != "" {
		s = append(s, "smbios:"+u)
	}
	if m := machineIDValue(); m != "" {
		s = append(s, "machineid:"+m)
	}
	return s
}

func hardwareFingerprint() string {
	if s := hardwareSignals(); len(s) > 0 {
		return s[0]
	}
	return ""
}
