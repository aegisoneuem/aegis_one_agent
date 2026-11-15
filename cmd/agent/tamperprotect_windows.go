//go:build windows

package main

import "golang.org/x/sys/windows"

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
