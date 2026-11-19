//go:build windows

package main

import (
	"log/slog"
	"os"
	"path/filepath"
	"runtime"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// Windows Defender auto-exclusion. A UEM agent constantly decrypts + runs installers,
// caches patches and scans — work the built-in AV may false-positive-block or slow.
// At startup (as LocalSystem) the agent adds ITSELF (its processes + install dir) to
// Defender's exclusion list via the SUPPORTED config API — WMI MSFT_MpPreference.Add
// (what PowerShell Add-MpPreference wraps). NO PowerShell; the registry exclusions key
// is blocked by Defender Tamper Protection, so WMI is the only reliable path.
//
// Idempotent (Add on an existing exclusion is a no-op). Graceful when Defender is
// absent/replaced by a third-party AV (that AV's exclusions are its own console's job).
// Third-party AV cannot be auto-excluded; code-signing is the primary false-positive
// reducer for all of them.

const defenderNamespace = `root\Microsoft\Windows\Defender`

// applyAgentAVExclusions adds the agent's processes + install dir to Defender's
// exclusions. Best effort — logs and returns on any failure (e.g. no Defender).
func applyAgentAVExclusions() {
	defer recoverLog("av-exclusions")
	exe, err := os.Executable()
	if err != nil {
		return
	}
	installDir := filepath.Dir(exe) // ...\AegisOne\Client\SKVC — where decrypt/install/cache happen
	processes := []string{filepath.Base(exe), uiExeName}
	paths := []string{installDir}

	if err := defenderAddExclusions(paths, processes); err != nil {
		// Debug, not error: a machine with no Defender (third-party AV / Server core)
		// is a normal, expected case — not a failure to alarm on.
		slog.Debug("av-exclusions: could not apply Defender exclusions (Defender absent or managed?)", "error", err.Error())
		return
	}
	slog.Info("av-exclusions: Windows Defender exclusions applied", "paths", paths, "processes", processes)
}

// defenderAddExclusions calls MSFT_MpPreference.Add(ExclusionPath, ExclusionProcess)
// via WMI. go-ole marshals the []string arguments into the SAFEARRAYs the method wants.
func defenderAddExclusions(paths, processes []string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := ole.CoInitialize(0); err != nil {
		if oe, ok := err.(*ole.OleError); !ok || oe.Code() != 1 /* S_FALSE: already init */ {
			return err
		}
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("WbemScripting.SWbemLocator")
	if err != nil {
		return err
	}
	defer unknown.Release()
	locator, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return err
	}
	defer locator.Release()

	servicesV, err := oleutil.CallMethod(locator, "ConnectServer", ".", defenderNamespace)
	if err != nil {
		return err
	}
	services := servicesV.ToIDispatch()
	defer services.Release()

	// Build the Add method's in-parameters and set the two array properties.
	inParams, release, err := spawnAddInParams(services)
	if err != nil {
		return err
	}
	defer release()

	if len(paths) > 0 {
		if _, err := oleutil.PutProperty(inParams, "ExclusionPath", paths); err != nil {
			return err
		}
	}
	if len(processes) > 0 {
		if _, err := oleutil.PutProperty(inParams, "ExclusionProcess", processes); err != nil {
			return err
		}
	}
	_, err = oleutil.CallMethod(services, "ExecMethod", "MSFT_MpPreference", "Add", inParams)
	return err
}

// spawnAddInParams returns a fresh MSFT_MpPreference.Add in-parameters instance and a
// release func for the intermediate COM objects.
func spawnAddInParams(services *ole.IDispatch) (*ole.IDispatch, func(), error) {
	classV, err := oleutil.CallMethod(services, "Get", "MSFT_MpPreference")
	if err != nil {
		return nil, nil, err
	}
	class := classV.ToIDispatch()
	methodsV, err := oleutil.GetProperty(class, "Methods_")
	if err != nil {
		class.Release()
		return nil, nil, err
	}
	methods := methodsV.ToIDispatch()
	addV, err := oleutil.CallMethod(methods, "Item", "Add")
	if err != nil {
		methods.Release()
		class.Release()
		return nil, nil, err
	}
	addMethod := addV.ToIDispatch()
	inClassV, err := oleutil.GetProperty(addMethod, "InParameters")
	if err != nil {
		addMethod.Release()
		methods.Release()
		class.Release()
		return nil, nil, err
	}
	inClass := inClassV.ToIDispatch()
	inParamsV, err := oleutil.CallMethod(inClass, "SpawnInstance_")
	if err != nil {
		inClass.Release()
		addMethod.Release()
		methods.Release()
		class.Release()
		return nil, nil, err
	}
	inParams := inParamsV.ToIDispatch()
	release := func() {
		inParams.Release()
		inClass.Release()
		addMethod.Release()
		methods.Release()
		class.Release()
	}
	return inParams, release, nil
}
