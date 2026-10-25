//go:build windows

package softdist

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// splitWindowsArgs uses a dummy executable because CommandLineToArgvW handles
// argv[0] differently from later arguments (notably PROPERTY="quoted value").
func splitWindowsArgs(text string) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	args, err := commandLineToArgv(`aegis.exe ` + text)
	if err != nil {
		return nil, err
	}
	return args[1:], nil
}

// An uninstall argument list must not introduce a second MSI action (for example
// /i other.msi after /x {GUID}). UI, reboot, logging and PROPERTY=value options
// remain available to the signed package author.
func validateMSIUninstallArgs(text string) error {
	args, err := splitWindowsArgs(text)
	if err != nil {
		return err
	}
	for _, arg := range args {
		arg = strings.ToLower(arg)
		if len(arg) < 2 || (arg[0] != '/' && arg[0] != '-') {
			continue
		}
		arg = arg[1:]
		if arg == "forcerestart" {
			continue
		}
		for _, verb := range []string{"i", "x", "f", "a", "j", "package", "uninstall", "update", "repair", "advertise"} {
			if strings.HasPrefix(arg, verb) {
				return fmt.Errorf("silent_args must not override the MSI uninstall action: %q", arg)
			}
		}
	}
	return nil
}

func registryUninstallInvocation(p Params, app *InstalledApp, logPath string) (string, []string, error) {
	if app.WindowsInstaller {
		if err := validateMSIUninstallArgs(Substitute(p.SilentArgs, p.Parameters)); err != nil {
			return "", nil, err
		}
		if _, err := windows.GUIDFromString(app.Key); err != nil {
			return "", nil, fmt.Errorf("WindowsInstaller entry has invalid ProductCode %q", app.Key)
		}
		bin, args := msiUninstallByCode(p, app.Key, logPath)
		return bin, args, nil
	}
	line := strings.TrimSpace(app.QuietUninstallString)
	quiet := line != ""
	if !quiet {
		line = strings.TrimSpace(app.UninstallString)
	}
	if line == "" {
		return "", nil, fmt.Errorf("no uninstall command registered for %q", app.DisplayName)
	}
	line, err := registry.ExpandString(line)
	if err != nil {
		return "", nil, fmt.Errorf("expand uninstall command: %w", err)
	}
	argv, err := commandLineToArgv(line)
	if err != nil || len(argv) == 0 || argv[0] == "" {
		return "", nil, fmt.Errorf("invalid registered uninstall command")
	}
	bin, args := argv[0], argv[1:]
	if !quiet {
		extra, err := splitWindowsArgs(Substitute(p.SilentArgs, p.Parameters))
		if err != nil {
			return "", nil, fmt.Errorf("parse uninstall arguments: %w", err)
		}
		args = append(args, extra...)
	}
	// MSI registrations commonly use /I (maintenance UI). Never execute that
	// verb during an uninstall: pin a valid ProductCode and construct /x instead.
	if strings.EqualFold(filepath.Base(bin), "msiexec.exe") || strings.EqualFold(bin, "msiexec") {
		if err := validateMSIUninstallArgs(Substitute(p.SilentArgs, p.Parameters)); err != nil {
			return "", nil, err
		}
		var code string
		for i := 0; i < len(args); i++ {
			a := args[i]
			if len(a) < 2 || (a[0] != '/' && a[0] != '-') {
				continue
			}
			if strings.EqualFold(a[:2], "/i") || strings.EqualFold(a[:2], "/x") || strings.EqualFold(a[:2], "-i") || strings.EqualFold(a[:2], "-x") {
				if code != "" {
					return "", nil, fmt.Errorf("multiple MSI product actions in registered uninstall command")
				}
				code = a[2:]
				if code == "" && i+1 < len(args) {
					i++
					code = args[i]
				}
				if _, err := windows.GUIDFromString(code); err != nil {
					return "", nil, fmt.Errorf("registry MSI uninstall requires a valid ProductCode, not a package path")
				}
			}
		}
		if code == "" {
			return "", nil, fmt.Errorf("registered MSI command has no /I or /X ProductCode")
		}
		bin, args := msiUninstallByCode(p, code, logPath)
		return bin, args, nil
	}
	// Never guess where an unquoted path with spaces ends, or search the service
	// PATH/current directory for a vendor executable.
	if !filepath.IsAbs(bin) {
		switch strings.ToLower(bin) {
		case "cmd.exe", "rundll32.exe", "cscript.exe", "wscript.exe":
			dir, err := windows.GetSystemDirectory()
			if err != nil {
				return "", nil, err
			}
			bin = filepath.Join(dir, bin)
		default:
			return "", nil, fmt.Errorf("registered uninstaller must use an absolute executable path (quote paths with spaces)")
		}
	}
	if ext := strings.ToLower(filepath.Ext(bin)); ext != ".exe" && ext != ".com" {
		return "", nil, fmt.Errorf("registered uninstaller must name an executable; quote paths with spaces")
	}
	if !quiet && strings.TrimSpace(p.SilentArgs) == "" {
		return "", nil, fmt.Errorf("no QuietUninstallString for %q; supply vendor-specific silent_args", app.DisplayName)
	}
	return bin, args, nil
}
