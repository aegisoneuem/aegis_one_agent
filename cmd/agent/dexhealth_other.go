//go:build !windows

package main

import pb "github.com/aegisoneuem/aegis_one_proto/grpc"

// collectDexHealth is a stub off-Windows; the Linux/macOS reliability sources
// (journald / log show, systemd-coredump, pmset) land with the cross-platform
// phase.
func collectDexHealth() *pb.DexHealth { return &pb.DexHealth{} }
