//go:build windows

package main

import (
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Native DEX network sampler — measures round-trip latency and reachability to the
// default gateway (or a configured target) via the ICMP helper (iphlpapi), which
// does NOT need raw sockets / Administrator (works as the LocalSystem service). No
// ping.exe, no PowerShell.
var (
	iphlpapi            = windows.NewLazySystemDLL("iphlpapi.dll")
	procIcmpCreateFile  = iphlpapi.NewProc("IcmpCreateFile")
	procIcmpSendEcho    = iphlpapi.NewProc("IcmpSendEcho")
	procIcmpCloseHandle = iphlpapi.NewProc("IcmpCloseHandle")
)

const icmpTimeoutMs = 1000

// icmpEchoReply mirrors ICMP_ECHO_REPLY (leading fields only; the trailing
// ip_option_information is unused). Status 0 (IP_SUCCESS) means a reply arrived.
type icmpEchoReply struct {
	Address       uint32
	Status        uint32
	RoundTripTime uint32
	DataSize      uint16
	Reserved      uint16
	Data          uintptr
}

// netSample is one latency/reachability probe. OK=false means there was no target
// to probe (no gateway, bad override) — the metric is then simply absent. When OK,
// Reachable distinguishes a reply (RttMs valid) from a timeout/loss.
type netSample struct {
	RttMs     float64
	Reachable bool
	OK        bool
}

// netSampler holds an ICMP handle and the resolved IPv4 target.
type netSampler struct {
	target   uint32 // IPAddr (octets in memory order)
	TargetIP string
	handle   uintptr
	ready    bool
}

// newNetSampler resolves the probe target (override if non-empty, else the default
// gateway) and opens an ICMP handle. On any failure sample() yields OK=false.
func newNetSampler(override string) *netSampler {
	n := &netSampler{}
	var ip net.IP
	if override != "" {
		ip = net.ParseIP(override)
	} else {
		ip = defaultGatewayIPv4()
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return n
	}
	h, _, _ := procIcmpCreateFile.Call()
	if h == 0 || h == ^uintptr(0) { // INVALID_HANDLE_VALUE
		return n
	}
	n.handle = h
	n.target = uint32(ip4[0]) | uint32(ip4[1])<<8 | uint32(ip4[2])<<16 | uint32(ip4[3])<<24
	n.TargetIP = ip4.String()
	n.ready = true
	return n
}

func (n *netSampler) sample() netSample {
	if !n.ready {
		return netSample{}
	}
	req := [32]byte{} // small echo payload
	var reply [128]byte
	r, _, _ := procIcmpSendEcho.Call(
		n.handle,
		uintptr(n.target),
		uintptr(unsafe.Pointer(&req[0])), uintptr(len(req)),
		0, // no request options
		uintptr(unsafe.Pointer(&reply[0])), uintptr(len(reply)),
		icmpTimeoutMs,
	)
	if r == 0 { // no replies → timeout / unreachable
		return netSample{Reachable: false, OK: true}
	}
	er := (*icmpEchoReply)(unsafe.Pointer(&reply[0]))
	if er.Status != 0 { // IP_SUCCESS == 0
		return netSample{Reachable: false, OK: true}
	}
	return netSample{RttMs: float64(er.RoundTripTime), Reachable: true, OK: true}
}

func (n *netSampler) close() {
	if n.handle != 0 && n.handle != ^uintptr(0) {
		procIcmpCloseHandle.Call(n.handle)
		n.handle = 0
		n.ready = false
	}
}

// defaultGatewayIPv4 returns the IPv4 default gateway of the first up, non-loopback
// adapter that has one, or nil if none is found.
func defaultGatewayIPv4() net.IP {
	size := uint32(15000)
	var buf []byte
	for attempt := 0; attempt < 3; attempt++ {
		buf = make([]byte, size)
		err := windows.GetAdaptersAddresses(
			windows.AF_INET,
			windows.GAA_FLAG_INCLUDE_GATEWAYS|windows.GAA_FLAG_SKIP_UNICAST|windows.GAA_FLAG_SKIP_ANYCAST|windows.GAA_FLAG_SKIP_MULTICAST|windows.GAA_FLAG_SKIP_DNS_SERVER,
			0,
			(*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])),
			&size,
		)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue // size now holds the required length; retry
		}
		if err != nil {
			return nil
		}
		break
	}
	// Pick the gateway of the up, non-loopback adapter with the LOWEST IPv4 route
	// metric — i.e. the primary default route — so a virtual/secondary adapter's
	// (often unreachable) gateway doesn't get chosen over the real NIC.
	var best net.IP
	bestMetric := ^uint32(0)
	for aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0])); aa != nil; aa = aa.Next {
		if aa.OperStatus != windows.IfOperStatusUp || aa.IfType == windows.IF_TYPE_SOFTWARE_LOOPBACK {
			continue
		}
		for g := aa.FirstGatewayAddress; g != nil; g = g.Next {
			ip := g.Address.IP()
			if ip == nil {
				continue
			}
			v4 := ip.To4()
			if v4 == nil {
				continue
			}
			if aa.Ipv4Metric < bestMetric {
				bestMetric = aa.Ipv4Metric
				best = v4
			}
			break // one gateway per adapter is enough for the comparison
		}
	}
	return best
}
