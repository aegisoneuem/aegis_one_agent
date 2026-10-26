//go:build windows

package softdist

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var adjustProfilePrivileges = windows.NewLazySystemDLL("advapi32.dll").NewProc("AdjustTokenPrivileges")

// Enable profile privileges only on a temporary service-self thread token.
// The process token and unrelated goroutines never gain ambient privileges.
// The target user's token is never impersonated on an agent thread.
func withProfilePrivileges(action func() error) error {
	result := make(chan error, 1)
	go func() {
		defer func() {
			if recover() != nil {
				result <- fmt.Errorf("native profile operation failed unexpectedly")
			}
		}()
		result <- profileOperation(action)
	}()
	return <-result
}
func profileOperation(action func() error) (err error) {
	runtime.LockOSThread()
	if err = windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		runtime.UnlockOSThread()
		return err
	}
	defer func() {
		if revertErr := windows.RevertToSelf(); revertErr != nil {
			// Keep this thread locked; Go discards a locked thread when the goroutine
			// exits. Never return an impersonating thread to the runtime pool.
			err = fmt.Errorf("restore service identity after profile operation: %w", revertErr)
			return
		}
		runtime.UnlockOSThread()
	}()
	var token windows.Token
	if err = windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, true, &token); err != nil {
		return err
	}
	defer token.Close()
	for _, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		ptr, _ := windows.UTF16PtrFromString(name)
		state := windows.Tokenprivileges{PrivilegeCount: 1}
		if err = windows.LookupPrivilegeValue(nil, ptr, &state.Privileges[0].Luid); err != nil {
			return err
		}
		state.Privileges[0].Attributes = windows.SE_PRIVILEGE_ENABLED
		ok, _, callErr := adjustProfilePrivileges.Call(uintptr(token), 0, uintptr(unsafe.Pointer(&state)), 0, 0, 0)
		if ok == 0 || callErr == windows.ERROR_NOT_ALL_ASSIGNED {
			return fmt.Errorf("enable profile privilege: %w", callErr)
		}
	}
	return action()
}
