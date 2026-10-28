//go:build windows

package softdist

import (
	"fmt"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A jobGroup is a Windows Job Object owning one installer process AND every
// descendant it spawns. When the job is Closed (or TerminateJobObject fires on
// timeout), every process in the job dies — msiexec engines, EXE installer
// chains, batch helpers, orphaned PowerShell children. Without this a
// timed-out installer leaves working processes behind on the endpoint, which
// is exactly what memory flagged as the "descendants can survive timeout"
// gap.
//
// The job uses:
//   - JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE   (last handle close kills everything)
//   - JOB_OBJECT_LIMIT_BREAKAWAY_OK and JOB_OBJECT_LIMIT_SILENT_BREAKAWAY_OK:
//     NEITHER are set, so child processes CANNOT escape (even if their creator
//     passed CREATE_BREAKAWAY_FROM_JOB, Windows refuses because our job does
//     not permit it).
//   - JobObjectExtendedLimitInformation query class.
//
// We attach the installer process AFTER CreateProcess with CREATE_SUSPENDED
// is NOT used here — we instead rely on Windows' guarantee that when a
// process is assigned to a job before it creates children, every descendant
// inherits the job. In practice: Start the process paused, assign, resume.
// Go's exec.Cmd makes "start paused" awkward; we take the practical
// alternative — attach immediately after Start and accept the microsecond
// race window. If a child spawns BEFORE we assign, that child is still fine
// in all observed installers (msiexec, Firefox installer stub, 7z MSI engine)
// because they do not spawn descendants in their first instructions. If a
// future installer races past this, we document it; the current gap of
// NO job object at all is far worse than this tiny race.
//
// On non-Windows builds this type is replaced by a no-op in the sibling
// jobobject_other.go so the softdist package stays cross-compiling.

type jobGroup struct {
	handle windows.Handle
	closed bool
}

func newJobGroup(name string) (*jobGroup, error) {
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("CreateJobObject: %w", err)
	}
	// Configure KILL_ON_JOB_CLOSE so an agent crash / forced shutdown still
	// takes the installer tree down with it (no orphan survival).
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(h,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("SetInformationJobObject: %w", err)
	}
	_ = name // reserved — if we ever name the job for debugging, parameter is here
	return &jobGroup{handle: h}, nil
}

// Attach places an already-started process into the job. Every child the
// process spawns inherits the job automatically. Must be called after
// cmd.Start().
func (j *jobGroup) Attach(cmd *exec.Cmd) error {
	if j == nil || j.handle == 0 || cmd == nil || cmd.Process == nil {
		return fmt.Errorf("jobGroup not initialized or process not started")
	}
	procHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE,
		false,
		uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", cmd.Process.Pid, err)
	}
	defer windows.CloseHandle(procHandle)
	if err := windows.AssignProcessToJobObject(j.handle, procHandle); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

// Terminate kills the installer and all descendants right now. Called from a
// timeout watchdog or an abort path. Safe to call multiple times.
func (j *jobGroup) Terminate(exitCode uint32) error {
	if j == nil || j.handle == 0 || j.closed {
		return nil
	}
	return windows.TerminateJobObject(j.handle, exitCode)
}

// Close releases the job handle. Because the job carries KILL_ON_JOB_CLOSE,
// any still-running descendants die here even if the installer process is
// already gone. Idempotent.
func (j *jobGroup) Close() error {
	if j == nil || j.handle == 0 || j.closed {
		return nil
	}
	j.closed = true
	return windows.CloseHandle(j.handle)
}

// activeDescendants returns the number of processes currently alive inside the
// job (installer + spawned children). Used for timeout root-cause isolation:
// "timed out with 3 descendants alive" tells the operator the main process was
// NOT hung on its own — children (msiexec engine, config helpers) were still
// executing. Best-effort; 0 and errors are both reported as 0.
func (j *jobGroup) activeDescendants() int {
	if j == nil || j.handle == 0 {
		return 0
	}
	// JOBOBJECT_BASIC_PROCESS_ID_LIST: a small fixed header plus a trailing
	// variable-length array of PIDs. Query with a buffer sized for up to 64
	// PIDs — more than any realistic installer tree.
	type processIDList struct {
		NumberOfAssignedProcesses uint32
		NumberOfProcessIdsInList  uint32
		ProcessIdList             [64]uintptr
	}
	var list processIDList
	var size uint32
	err := windows.QueryInformationJobObject(j.handle,
		windows.JobObjectBasicProcessIdList,
		uintptr(unsafe.Pointer(&list)),
		uint32(unsafe.Sizeof(list)),
		&size)
	if err != nil {
		return 0
	}
	return int(list.NumberOfProcessIdsInList)
}
