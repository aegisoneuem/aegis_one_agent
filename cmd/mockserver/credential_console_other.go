//go:build !windows

package main

import "fmt"

func readCredentialLine(string, bool) (string, error) {
	return "", fmt.Errorf("credential entry requires Windows DPAPI and a native console")
}
