//go:build !windows

package main

import pb "github.com/aegisoneuem/aegis_one_proto/grpc"

// collectIdentityInfo is a stub off-Windows; the Linux/macOS collectors (utmp /
// SCDynamicStore, realm/sssd domain state, sysinfo uptime) land with the
// cross-platform phase.
func collectIdentityInfo(int64) *pb.IdentityInfo { return &pb.IdentityInfo{} }
