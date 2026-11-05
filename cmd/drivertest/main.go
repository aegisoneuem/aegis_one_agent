// Command drivertest dumps the live Win32_PnPSignedDriver inventory the agent
// would report for OEM driver patching. Dev/diagnostic only.
package main

import (
	"fmt"
	"os"

	"github.com/aegisoneuem/aegis_one_agent/internal/patch"
)

func main() {
	drivers, err := patch.CollectDrivers()
	if err != nil {
		fmt.Fprintf(os.Stderr, "CollectDrivers failed: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Reported drivers: %d\n", len(drivers))
	fmt.Printf("%-40s %-14s %-12s %-16s %-6s %s\n", "DEVICE", "VERSION", "DATE", "PROVIDER", "SIGN", "CLASS")
	for _, d := range drivers {
		name := d.DeviceName
		if len(name) > 40 {
			name = name[:39] + "…"
		}
		sign := "no"
		if d.IsSigned {
			sign = "yes"
		}
		fmt.Printf("%-40s %-14s %-12s %-16s %-6s %s\n",
			name, d.DriverVersion, d.DriverDate, d.ProviderName, sign, d.DeviceClass)
	}
}
