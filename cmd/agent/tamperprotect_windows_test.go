//go:build windows

package main

import (
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHardenDirACL_AppliesProtectedDACL(t *testing.T) {
	dir, err := os.MkdirTemp("", "aegistamper")
	if err != nil {
		t.Fatal(err)
	}
	// Restore a permissive, inheritable DACL before removing, or cleanup can fail
	// (the hardened ACL doesn't grant the test user delete).
	defer func() {
		if sd, e := windows.SecurityDescriptorFromString("D:(A;OICI;FA;;;WD)"); e == nil {
			if dacl, _, e2 := sd.DACL(); e2 == nil {
				_ = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
					windows.DACL_SECURITY_INFORMATION|windows.UNPROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
			}
		}
		_ = os.RemoveAll(dir)
	}()

	if err := hardenDirACL(dir); err != nil {
		t.Skipf("could not set ACL in this environment (privileges?): %v", err)
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	got := sd.String()
	t.Logf("applied DACL: %s", got)

	// Protected (inheritance broken).
	if !strings.Contains(got, "D:P") {
		t.Fatalf("DACL is not protected (inheritance not broken): %s", got)
	}
	// SYSTEM, Administrators, Users ACEs present (rights may normalize to hex, so
	// assert the SID trustees rather than the exact right strings).
	for _, sid := range []string{";;;SY)", ";;;BA)", ";;;BU)"} {
		if !strings.Contains(got, sid) {
			t.Fatalf("expected an ACE ending %q in DACL: %s", sid, got)
		}
	}
}
