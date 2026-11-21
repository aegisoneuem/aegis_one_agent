//go:build !windows && !linux

package main

import pb "github.com/aegisoneuem/aegis_one_proto/grpc"

// collectIdentityInfo is a stub on macOS/BSD (Windows + Linux have collectors). A macOS
// SCDynamicStore collector lands with the macOS port.
func collectIdentityInfo(int64) *pb.IdentityInfo { return &pb.IdentityInfo{} }
