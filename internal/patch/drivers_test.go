package patch

import (
	"reflect"
	"testing"
)

func TestSortDrivers_Deterministic(t *testing.T) {
	// Same set in two different input orders must sort identically — this is
	// what prevents WMI enumeration order from faking inventory "drift".
	a := []DriverInfo{
		{HardwareID: "PCI\\B", DeviceName: "Z", DriverVersion: "2"},
		{HardwareID: "PCI\\A", DeviceName: "Y", DriverVersion: "1"},
		{HardwareID: "PCI\\A", DeviceName: "Y", DriverVersion: "3"},
	}
	b := []DriverInfo{
		{HardwareID: "PCI\\A", DeviceName: "Y", DriverVersion: "3"},
		{HardwareID: "PCI\\B", DeviceName: "Z", DriverVersion: "2"},
		{HardwareID: "PCI\\A", DeviceName: "Y", DriverVersion: "1"},
	}
	sortDrivers(a)
	sortDrivers(b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("sortDrivers not deterministic:\n a=%+v\n b=%+v", a, b)
	}
	// First element must be the (A,Y,1) row.
	if a[0].HardwareID != "PCI\\A" || a[0].DriverVersion != "1" {
		t.Errorf("unexpected sort head: %+v", a[0])
	}
}

func TestNormalizeCIMDate(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"20230510000000.000000-000", "2023-05-10"},
		{"20240101120000.000000+000", "2024-01-01"},
		{"19991231235959.999999-480", "1999-12-31"},
		{"20230510", "2023-05-10"}, // bare 8-digit also fine
	}
	for _, c := range cases {
		if got := normalizeCIMDate(c.in); got != c.want {
			t.Errorf("normalizeCIMDate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Break: malformed / empty / all-zero dates must yield "" (never a fake date).
func TestNormalizeCIMDate_Break(t *testing.T) {
	bad := []string{
		"",                            // empty
		"   ",                         // whitespace
		"2023",                        // too short
		"abcd0510000000.0",            // non-digit year
		"2023051",                     // 7 digits
		"00000000000000.000000-000",   // all-zero date
		"0000000",                     // short all-zero
	}
	for _, in := range bad {
		if got := normalizeCIMDate(in); got != "" {
			t.Errorf("normalizeCIMDate(%q) = %q, want \"\"", in, got)
		}
	}
}

func TestKeepDriver(t *testing.T) {
	// Keep: version + hardware ID.
	if !keepDriver(DriverInfo{DriverVersion: "1.2.3", HardwareID: `PCI\VEN_10DE`}) {
		t.Error("driver with version+hardwareID should be kept")
	}
	// Keep: version + provider (no hardware ID).
	if !keepDriver(DriverInfo{DriverVersion: "1.2.3", ProviderName: "NVIDIA"}) {
		t.Error("driver with version+provider should be kept")
	}
}

// Break: rows with no version, or no catalog anchor, are dropped as noise.
func TestKeepDriver_Break(t *testing.T) {
	drop := []DriverInfo{
		{DriverVersion: "", HardwareID: `PCI\X`, ProviderName: "Intel"}, // no version
		{DriverVersion: "   ", HardwareID: `PCI\X`},                     // blank version
		{DriverVersion: "1.0"},                                          // version but no anchor
		{},                                                              // empty
	}
	for i, d := range drop {
		if keepDriver(d) {
			t.Errorf("case %d: driver %+v should be dropped", i, d)
		}
	}
}
