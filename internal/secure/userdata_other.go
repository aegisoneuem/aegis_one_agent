//go:build !windows

package secure

import "fmt"

func ProtectUserData([]byte) ([]byte, error) {
	return nil, fmt.Errorf("operator credential fixtures require Windows DPAPI")
}
func UnprotectUserData([]byte) ([]byte, error) {
	return nil, fmt.Errorf("operator credential fixtures require Windows DPAPI")
}
