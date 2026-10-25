//go:build windows

package softdist

import (
	"fmt"
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

// QueryProductInstalled distinguishes invalid/configuration errors from absence.
func QueryProductInstalled(productCode string) (bool, error) {
	productCode = strings.TrimSpace(productCode)
	if _, err := windows.GUIDFromString(productCode); err != nil {
		return false, fmt.Errorf("invalid MSI ProductCode %q: %w", productCode, err)
	}
	p, err := windows.UTF16PtrFromString(productCode)
	if err != nil {
		return false, err
	}
	r, _, _ := procMsiQueryProductState.Call(uintptr(unsafe.Pointer(p)))
	switch state := int32(r); state {
	case installStateDefault, installStateLocal, installStateSource:
		return true, nil
	case -1, 1, 2: // UNKNOWN, ADVERTISED, ABSENT (different-user install)
		return false, nil
	default:
		return false, fmt.Errorf("MSI product-state query failed (state %d)", state)
	}
}

// ProductInstalled is a compatibility wrapper; action decisions must use the
// error-returning query rather than treating unknown states as absent.
func ProductInstalled(productCode string) bool {
	installed, _ := QueryProductInstalled(productCode)
	return installed
}
