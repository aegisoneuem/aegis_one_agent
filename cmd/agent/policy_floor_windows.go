//go:build windows

package main

import "golang.org/x/sys/windows/registry"

// The sealed anti-rollback floor lives in the agent's own registry tree
// (HKLM\SOFTWARE\AegisOne\Client, value PolicyFloor) — one location with the rest
// of the agent's machine state, SYSTEM-writable only, and NOT a file sitting next
// to policy.json where it could be deleted in the same sweep.
const (
	floorRegPath  = `SOFTWARE\AegisOne\Client`
	floorRegValue = "PolicyFloor"
)

func platformReadFloorBlob(string) ([]byte, error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, floorRegPath, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil, nil // absent → zero floor (not an error)
	}
	defer k.Close()
	b, _, err := k.GetBinaryValue(floorRegValue)
	if err != nil {
		return nil, nil
	}
	return b, nil
}

func platformWriteFloorBlob(_ string, blob []byte) error {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, floorRegPath, registry.SET_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetBinaryValue(floorRegValue, blob)
}
