//go:build windows

package main

import (
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
)

// handleReboot schedules a system reboot via shutdown.exe (native, no PowerShell)
// after a grace period, optionally warning logged-in users. Only ever runs from
// an Ed25519-signed server command. Returns (status, message) for the result.
func handleReboot(spec *pb.RebootSpec) (status, message string) {
	delay := int(spec.DelaySeconds)
	if delay < 0 {
		delay = 0
	}
	args := []string{"/r", "/t", strconv.Itoa(delay)}
	if msg := strings.TrimSpace(spec.Message); msg != "" {
		args = append(args, "/c", msg)
	}
	slog.Warn("reboot scheduled by server command", "delay_seconds", delay, "message", spec.Message)

	out, err := exec.Command("shutdown.exe", args...).CombinedOutput()
	if err != nil {
		slog.Error("reboot: shutdown failed", "error", err.Error(), "output", strings.TrimSpace(string(out)))
		return "failed", fmt.Sprintf("shutdown failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return "success", fmt.Sprintf("reboot scheduled in %ds", delay)
}

// handleCancelReboot cancels a pending reboot (shutdown /a).
func handleCancelReboot() (status, message string) {
	slog.Info("reboot cancel requested by server command")
	out, err := exec.Command("shutdown.exe", "/a").CombinedOutput()
	if err != nil {
		return "failed", fmt.Sprintf("cancel failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return "success", "pending reboot cancelled"
}
