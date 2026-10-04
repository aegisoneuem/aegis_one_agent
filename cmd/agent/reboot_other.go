//go:build !windows

package main

import pb "github.com/aegisoneuem/aegis_one_proto/grpc"

// Reboot handling is Windows-only for now (uses shutdown.exe).
func handleReboot(spec *pb.RebootSpec) (string, string) {
	return "failed", "reboot not supported on this OS yet"
}

func handleCancelReboot() (string, string) {
	return "failed", "reboot not supported on this OS yet"
}
