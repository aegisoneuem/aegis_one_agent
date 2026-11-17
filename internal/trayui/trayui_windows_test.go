//go:build windows

package trayui

import "testing"

func TestCopyUTF16(t *testing.T) {
	var dst [16]uint16
	CopyUTF16(dst[:], "Aegis")
	for i, w := range []uint16{'A', 'e', 'g', 'i', 's', 0} {
		if dst[i] != w {
			t.Fatalf("dst[%d] = %d, want %d", i, dst[i], w)
		}
	}
	// Must not overflow a too-small buffer.
	var small [3]uint16
	CopyUTF16(small[:], "Aegis")
	if small[0] != 'A' || small[1] != 'e' || small[2] != 'g' {
		t.Fatalf("truncated copy wrong: %v", small)
	}
}
