//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native battery wear/health reader. GetSystemPowerStatus (battery_windows.go) gives
// the live charge %, but NOT wear — for that we must talk to the battery device via
// SetupAPI (enumerate the battery interface) + DeviceIoControl(IOCTL_BATTERY_*). No
// WMI, no PowerShell. health% = FullChargedCapacity / DesignedCapacity * 100.
//
// Returns ok=false on desktops/servers/VMs with no battery, or when the firmware does
// not report capacities — a normal, non-error outcome.

var (
	batSetupapi                        = windows.NewLazySystemDLL("setupapi.dll")
	procSetupDiGetClassDevs            = batSetupapi.NewProc("SetupDiGetClassDevsW")
	procSetupDiEnumDeviceInterfaces    = batSetupapi.NewProc("SetupDiEnumDeviceInterfaces")
	procSetupDiGetDeviceInterfaceDetail = batSetupapi.NewProc("SetupDiGetDeviceInterfaceDetailW")
	procSetupDiDestroyDeviceInfoList   = batSetupapi.NewProc("SetupDiDestroyDeviceInfoList")
)

const (
	digcfPresent         = 0x02
	digcfDeviceInterface = 0x10
	// CTL_CODE(FILE_DEVICE_BATTERY=0x29, func, METHOD_BUFFERED, FILE_READ_ACCESS).
	ioctlBatteryQueryTag         = 0x294040
	ioctlBatteryQueryInformation = 0x294044
	batteryInformationLevel      = 0 // BatteryInformation
)

// guidDeviceBattery = GUID_DEVICE_BATTERY.
var guidDeviceBattery = windows.GUID{
	Data1: 0x72631E54, Data2: 0x78A4, Data3: 0x11D0,
	Data4: [8]byte{0xBC, 0xF7, 0x00, 0xAA, 0x00, 0xB7, 0xB3, 0x2A},
}

type spDeviceInterfaceData struct {
	cbSize             uint32
	interfaceClassGuid windows.GUID
	flags              uint32
	reserved           uintptr
}

type batteryQueryInformation struct {
	batteryTag       uint32
	informationLevel int32
	atRate           int32
}

type batteryInformation struct {
	capabilities        uint32
	technology          uint8
	reserved            [3]uint8
	chemistry           [4]uint8
	designedCapacity    uint32
	fullChargedCapacity uint32
	defaultAlert1       uint32
	defaultAlert2       uint32
	criticalBias        uint32
	cycleCount          uint32
}

// readBatteryWear returns (healthPct, cycleCount, ok). ok=false when there is no
// battery or the firmware reports no capacity.
func readBatteryWear() (float64, int, bool) {
	hdev, _, _ := procSetupDiGetClassDevs.Call(
		uintptr(unsafe.Pointer(&guidDeviceBattery)), 0, 0, digcfPresent|digcfDeviceInterface)
	if hdev == 0 || hdev == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return 0, 0, false
	}
	defer procSetupDiDestroyDeviceInfoList.Call(hdev)

	var did spDeviceInterfaceData
	did.cbSize = uint32(unsafe.Sizeof(did))
	if r, _, _ := procSetupDiEnumDeviceInterfaces.Call(hdev, 0,
		uintptr(unsafe.Pointer(&guidDeviceBattery)), 0, uintptr(unsafe.Pointer(&did))); r == 0 {
		return 0, 0, false // no battery interface present
	}

	var reqSize uint32
	procSetupDiGetDeviceInterfaceDetail.Call(hdev, uintptr(unsafe.Pointer(&did)),
		0, 0, uintptr(unsafe.Pointer(&reqSize)), 0)
	if reqSize == 0 {
		return 0, 0, false
	}
	buf := make([]byte, reqSize)
	// SP_DEVICE_INTERFACE_DETAIL_DATA_W { DWORD cbSize; WCHAR DevicePath[]; }.
	// cbSize = 8 on x64; the path starts at offset 4.
	*(*uint32)(unsafe.Pointer(&buf[0])) = 8
	if r, _, _ := procSetupDiGetDeviceInterfaceDetail.Call(hdev, uintptr(unsafe.Pointer(&did)),
		uintptr(unsafe.Pointer(&buf[0])), uintptr(reqSize), 0, 0); r == 0 {
		return 0, 0, false
	}
	pathPtr := (*uint16)(unsafe.Pointer(&buf[4]))

	h, err := windows.CreateFile(pathPtr, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return 0, 0, false
	}
	defer windows.CloseHandle(h)

	var tag, wait, ret uint32
	if err := windows.DeviceIoControl(h, ioctlBatteryQueryTag,
		(*byte)(unsafe.Pointer(&wait)), 4, (*byte)(unsafe.Pointer(&tag)), 4, &ret, nil); err != nil || tag == 0 {
		return 0, 0, false
	}
	bqi := batteryQueryInformation{batteryTag: tag, informationLevel: batteryInformationLevel}
	var bi batteryInformation
	if err := windows.DeviceIoControl(h, ioctlBatteryQueryInformation,
		(*byte)(unsafe.Pointer(&bqi)), uint32(unsafe.Sizeof(bqi)),
		(*byte)(unsafe.Pointer(&bi)), uint32(unsafe.Sizeof(bi)), &ret, nil); err != nil {
		return 0, 0, false
	}
	if bi.designedCapacity == 0 {
		return 0, 0, false
	}
	health := float64(bi.fullChargedCapacity) / float64(bi.designedCapacity) * 100
	if health > 100 {
		health = 100 // a fresh battery can read slightly over designed
	}
	return health, int(bi.cycleCount), true
}
