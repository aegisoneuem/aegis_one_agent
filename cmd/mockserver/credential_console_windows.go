//go:build windows

package main

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"strings"
	"unicode/utf16"
)

// Native console only. Refuse redirected input so credentials cannot silently
// come from an insecure plaintext fixture or echoed command pipeline.
func readCredentialLine(prompt string, hidden bool) (string, error) {
	handle := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return "", fmt.Errorf("credential entry requires an interactive Windows console")
	}
	fmt.Print(prompt)
	if hidden {
		if err := windows.SetConsoleMode(handle, (mode|windows.ENABLE_LINE_INPUT)&^windows.ENABLE_ECHO_INPUT); err != nil {
			return "", err
		}
		defer windows.SetConsoleMode(handle, mode)
	}
	buf := make([]uint16, 32768)
	defer clear(buf)
	var n uint32
	if err := windows.ReadConsole(handle, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return "", err
	}
	if hidden {
		fmt.Println()
	}
	if n == uint32(len(buf)) {
		return "", fmt.Errorf("credential entry too long")
	}
	return strings.TrimRight(string(utf16.Decode(buf[:n])), "\r\n"), nil
}
