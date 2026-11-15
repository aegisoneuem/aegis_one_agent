//go:build windows

package main

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native DEX Wi-Fi reader via wlanapi — signal quality (0-100) + SSID of the
// current connection. No netsh, no WMI, no PowerShell. Best-effort: any failure or
// no Wi-Fi adapter yields OK with Present=false.
var (
	wlanapi              = windows.NewLazySystemDLL("wlanapi.dll")
	procWlanOpenHandle   = wlanapi.NewProc("WlanOpenHandle")
	procWlanCloseHandle  = wlanapi.NewProc("WlanCloseHandle")
	procWlanEnumIfaces   = wlanapi.NewProc("WlanEnumInterfaces")
	procWlanQueryIface   = wlanapi.NewProc("WlanQueryInterface")
	procWlanFreeMemory   = wlanapi.NewProc("WlanFreeMemory")
)

const (
	wlanIntfStateConnected       = 1 // wlan_interface_state_connected
	wlanOpcodeCurrentConnection  = 7 // wlan_intf_opcode_current_connection
)

// wlanInterfaceInfo mirrors WLAN_INTERFACE_INFO (GUID + description + state).
type wlanInterfaceInfo struct {
	InterfaceGuid        windows.GUID
	strInterfaceDesc     [256]uint16
	isState              uint32
}

// wlanInterfaceInfoList mirrors WLAN_INTERFACE_INFO_LIST header (items follow).
type wlanInterfaceInfoList struct {
	dwNumberOfItems uint32
	dwIndex         uint32
	// InterfaceInfo[1] wlanInterfaceInfo — variable array starts here
}

type dot11SSID struct {
	uLength uint32
	ucSSID  [32]byte
}

// wlanAssociationAttributes — explicit padding so wlanSignalQuality lands where the
// C struct puts it (uint32 after a [6]byte BSSID needs 4-byte alignment).
type wlanAssociationAttributes struct {
	dot11Ssid         dot11SSID
	dot11BssType      uint32
	dot11Bssid        [6]byte
	_                 [2]byte
	dot11PhyType      uint32
	uDot11PhyIndex    uint32
	wlanSignalQuality uint32 // 0-100
	ulRxRate          uint32
	ulTxRate          uint32
}

// wlanConnectionAttributes — leading fields up to the association attributes (the
// trailing security attributes are unused).
type wlanConnectionAttributes struct {
	isState            uint32
	wlanConnectionMode uint32
	strProfileName     [256]uint16
	assoc              wlanAssociationAttributes
}

// wifiSample is one Wi-Fi reading. Present=false on a desktop/VM with no Wi-Fi.
// Connected=false when a Wi-Fi adapter exists but isn't associated to an AP.
type wifiSample struct {
	Present   bool
	Connected bool
	SignalPct int
	SSID      string
	OK        bool
}

func readWifi() wifiSample {
	var handle uintptr
	var negotiated uint32
	if r, _, _ := procWlanOpenHandle.Call(2, 0, uintptr(unsafe.Pointer(&negotiated)), uintptr(unsafe.Pointer(&handle))); r != 0 {
		return wifiSample{} // wlanapi/WLAN service unavailable
	}
	defer procWlanCloseHandle.Call(handle, 0)

	var listPtr *wlanInterfaceInfoList
	if r, _, _ := procWlanEnumIfaces.Call(handle, 0, uintptr(unsafe.Pointer(&listPtr))); r != 0 || listPtr == nil {
		return wifiSample{OK: true} // call ok but no list → treat as no Wi-Fi
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(listPtr)))

	if listPtr.dwNumberOfItems == 0 {
		return wifiSample{Present: false, OK: true} // no Wi-Fi adapter (desktop/server)
	}
	// First interface. Items begin right after the 8-byte list header.
	first := (*wlanInterfaceInfo)(unsafe.Pointer(uintptr(unsafe.Pointer(listPtr)) + 8))
	out := wifiSample{Present: true, OK: true}
	if first.isState != wlanIntfStateConnected {
		return out // adapter present but not associated
	}

	var dataSize uint32
	var dataPtr *wlanConnectionAttributes
	if r, _, _ := procWlanQueryIface.Call(
		handle,
		uintptr(unsafe.Pointer(&first.InterfaceGuid)),
		wlanOpcodeCurrentConnection,
		0,
		uintptr(unsafe.Pointer(&dataSize)),
		uintptr(unsafe.Pointer(&dataPtr)),
		0,
	); r != 0 || dataPtr == nil {
		return out // connected but couldn't read details
	}
	defer procWlanFreeMemory.Call(uintptr(unsafe.Pointer(dataPtr)))

	out.Connected = true
	out.SignalPct = int(dataPtr.assoc.wlanSignalQuality)
	n := dataPtr.assoc.dot11Ssid.uLength
	if n > 32 {
		n = 32
	}
	out.SSID = string(dataPtr.assoc.dot11Ssid.ucSSID[:n])
	return out
}
