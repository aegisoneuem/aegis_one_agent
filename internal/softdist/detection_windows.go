//go:build windows

package softdist

import (
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modmsi                   = windows.NewLazySystemDLL("msi.dll")
	procMsiQueryProductState = modmsi.NewProc("MsiQueryProductStateW")
)

// INSTALLSTATE values (msi.h). A product query returns DEFAULT when installed and
// usable; LOCAL/SOURCE are feature-level "present" states; ADVERTISED/ABSENT/
// UNKNOWN mean not installed.
const (
	installStateLocal   = 3
	installStateSource  = 4
	installStateDefault = 5
)

// ProductInstalled reports whether the MSI product with the given ProductCode GUID
// (e.g. "{23170F69-40C1-2702-2409-000001000000}") is installed on this machine. An
// empty or malformed code returns false (cannot confirm → act as if not installed).
func ProductInstalled(productCode string) bool {
	productCode = strings.TrimSpace(productCode)
	if productCode == "" {
		return false
	}
	p, err := windows.UTF16PtrFromString(productCode)
	if err != nil {
		return false
	}
	r, _, _ := procMsiQueryProductState.Call(uintptr(unsafe.Pointer(p)))
	switch int32(r) {
	case installStateDefault, installStateLocal, installStateSource:
		return true
	default:
		return false
	}
}
