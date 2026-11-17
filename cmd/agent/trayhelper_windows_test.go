//go:build windows

package main

import "testing"

func TestCopyUTF16(t *testing.T) {
	// Fits with room: expect the string's UTF-16 units followed by a NUL terminator.
	var dst [16]uint16
	copyUTF16(dst[:], "Aegis")
	want := []uint16{'A', 'e', 'g', 'i', 's', 0}
	for i, w := range want {
		if dst[i] != w {
			t.Fatalf("dst[%d] = %d, want %d", i, dst[i], w)
		}
	}

	// Exactly-full buffer must not panic / overflow; it just truncates.
	var small [3]uint16
	copyUTF16(small[:], "Aegis") // longer than the buffer
	if small[0] != 'A' || small[1] != 'e' || small[2] != 'g' {
		t.Fatalf("truncated copy wrong: %v", small)
	}
}
