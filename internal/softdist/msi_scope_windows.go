//go:build windows

package softdist

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getProductInfoExProc = modmsi.NewProc("MsiGetProductInfoExW")

func QueryProductInScope(code, sid string) (bool, string, error) {
	if _, err := windows.GUIDFromString(code); err != nil {
		return false, "", fmt.Errorf("invalid MSI ProductCode")
	}
	contexts := []uint32{4}
	var sidPtr *uint16
	if sid != "" {
		if _, err := windows.StringToSid(sid); err != nil {
			return false, "", fmt.Errorf("invalid execution SID")
		}
		sidPtr, _ = windows.UTF16PtrFromString(sid)
		contexts = []uint32{1, 2}
	}
	product, _ := windows.UTF16PtrFromString(code)
	var installed bool
	var version string
	for _, context := range contexts {
		state, result := msiProperty(product, sidPtr, context, "State")
		if result == 1605 {
			continue
		}
		if result != 0 {
			return false, "", fmt.Errorf("MSI scoped state query failed (Windows error %d)", result)
		}
		if state != "5" {
			if state == "1" {
				continue
			}
			return false, "", fmt.Errorf("MSI scoped state was not recognized")
		}
		if installed {
			return false, "", fmt.Errorf("MSI product has multiple user installation contexts")
		}
		installed = true
		value, result := msiProperty(product, sidPtr, context, "VersionString")
		if result != 0 && result != 1608 {
			return false, "", fmt.Errorf("MSI scoped version query failed (Windows error %d)", result)
		}
		version = value
	}
	return installed, version, nil
}

func msiProperty(code, sid *uint16, context uint32, name string) (string, uint32) {
	property, _ := windows.UTF16PtrFromString(name)
	var count uint32
	result, _, _ := getProductInfoExProc.Call(uintptr(unsafe.Pointer(code)), uintptr(unsafe.Pointer(sid)), uintptr(context), uintptr(unsafe.Pointer(property)), 0, uintptr(unsafe.Pointer(&count)))
	if result != 0 {
		return "", uint32(result)
	}
	if count > 32768 {
		return "", 13
	} // refuse corrupt/unbounded metadata
	buf := make([]uint16, count+1)
	result, _, _ = getProductInfoExProc.Call(uintptr(unsafe.Pointer(code)), uintptr(unsafe.Pointer(sid)), uintptr(context), uintptr(unsafe.Pointer(property)), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&count)))
	if result != 0 {
		return "", uint32(result)
	}
	return windows.UTF16ToString(buf), 0
}
