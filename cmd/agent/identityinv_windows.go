//go:build windows

package main

import (
	"unsafe"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"golang.org/x/sys/windows"
)

// Native identity/session collectors (BigFix retrieved-properties equivalent) —
// WTS + NetGetJoinInformation + GetTickCount64, no PowerShell, no WMI/COM.
var (
	idKernel32         = windows.NewLazySystemDLL("kernel32.dll")
	procGetTickCount64 = idKernel32.NewProc("GetTickCount64")

	idNetapi32                = windows.NewLazySystemDLL("netapi32.dll")
	procNetGetJoinInformation = idNetapi32.NewProc("NetGetJoinInformation")
	procNetApiBufferFree      = idNetapi32.NewProc("NetApiBufferFree")
)

// NETSETUP_JOIN_STATUS value for an AD-domain-joined machine.
const netSetupDomainName = 3

// collectIdentityInfo gathers the interactive user, domain/join state, and
// uptime. Every field is best-effort: a failure leaves it zero/empty rather than
// dropping the whole inventory snapshot.
func collectIdentityInfo(collectedAt int64) *pb.IdentityInfo {
	id := &pb.IdentityInfo{}

	if user, present := detectInteractiveUser(); present {
		id.LoggedOnUser = user
	}

	if r, _, _ := procGetTickCount64.Call(); r != 0 {
		id.UptimeSeconds = int64(r) / 1000
		if id.UptimeSeconds > 0 {
			id.BootTime = collectedAt - id.UptimeSeconds
		}
	}

	if domain, joined, ok := netJoinInfo(); ok {
		id.MachineDomain = domain
		id.DomainJoined = joined
	}
	return id
}

// netJoinInfo returns the machine's domain/workgroup name and whether it is
// joined to an AD domain, via NetGetJoinInformation (local server).
func netJoinInfo() (domain string, joined bool, ok bool) {
	var namePtr *uint16
	var status uint32
	r, _, _ := procNetGetJoinInformation.Call(0, uintptr(unsafe.Pointer(&namePtr)), uintptr(unsafe.Pointer(&status)))
	if r != 0 || namePtr == nil { // NERR_Success == 0
		return "", false, false
	}
	defer procNetApiBufferFree.Call(uintptr(unsafe.Pointer(namePtr)))
	return windows.UTF16PtrToString(namePtr), status == netSetupDomainName, true
}
