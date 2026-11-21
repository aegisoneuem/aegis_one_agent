//go:build linux

package softdist

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
)

// EnumerateInstalledApps lists installed packages via the distro package manager:
// dpkg (Debian/Ubuntu) or rpm (RHEL/Rocky/Alma/SUSE/Fedora). Maps each to InstalledApp
// (DisplayName=package, DisplayVersion=version, Publisher=maintainer/vendor,
// InstallDate where cheap). Native CLI, no PowerShell. Returns an empty list (not an
// error) when neither manager is present so DeviceStateReport still goes out.
func EnumerateInstalledApps() ([]InstalledApp, error) {
	if _, err := exec.LookPath("dpkg-query"); err == nil {
		return enumerateDpkg()
	}
	if _, err := exec.LookPath("rpm"); err == nil {
		return enumerateRpm()
	}
	return nil, nil
}

func enumerateDpkg() ([]InstalledApp, error) {
	// Tab-separated; Status filters out removed-but-config packages.
	out, err := exec.Command("dpkg-query", "-W",
		"-f", `${Package}\t${Version}\t${Maintainer}\t${db:Status-Status}\n`).Output()
	if err != nil {
		return nil, err
	}
	var apps []InstalledApp
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 4 || strings.TrimSpace(f[3]) != "installed" {
			continue
		}
		apps = append(apps, InstalledApp{
			DisplayName:    strings.TrimSpace(f[0]),
			DisplayVersion: strings.TrimSpace(f[1]),
			Publisher:      strings.TrimSpace(f[2]),
		})
	}
	return apps, nil
}

func enumerateRpm() ([]InstalledApp, error) {
	out, err := exec.Command("rpm", "-qa",
		"--qf", `%{NAME}\t%{VERSION}-%{RELEASE}\t%{VENDOR}\t%{INSTALLTIME:date}\n`).Output()
	if err != nil {
		return nil, err
	}
	var apps []InstalledApp
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	for sc.Scan() {
		f := strings.Split(sc.Text(), "\t")
		if len(f) < 2 || f[0] == "" {
			continue
		}
		app := InstalledApp{DisplayName: strings.TrimSpace(f[0]), DisplayVersion: strings.TrimSpace(f[1])}
		if len(f) >= 3 {
			app.Publisher = strings.TrimSpace(f[2])
		}
		if len(f) >= 4 {
			app.InstallDate = strings.TrimSpace(f[3])
		}
		apps = append(apps, app)
	}
	return apps, nil
}
