//go:build !windows

package main

import pb "github.com/aegisoneuem/aegis_one_proto/grpc"

// Non-Windows: Security Center is Windows-only; no AV detection here.
func collectAntivirusProducts() []*pb.AntivirusProduct { return nil }
