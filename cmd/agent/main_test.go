package main

import (
	"testing"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/config"
)

func TestBuildHeartbeatMessage(t *testing.T) {
	now := time.Unix(1700000000, 0)
	msg := buildHeartbeatMessage("device-123", "1.2.3", "windows", "laptop-01", now)

	if msg == nil {
		t.Fatal("expected heartbeat message, got nil")
	}

	if hb := msg.GetHeartbeat(); hb == nil {
		t.Fatal("expected heartbeat payload")
	} else {
		if hb.DeviceId != "device-123" {
			t.Fatalf("unexpected device id: %s", hb.DeviceId)
		}
		if hb.AgentVersion != "1.2.3" {
			t.Fatalf("unexpected agent version: %s", hb.AgentVersion)
		}
		if hb.Hostname != "laptop-01" {
			t.Fatalf("unexpected hostname: %s", hb.Hostname)
		}
		if hb.Timestamp != now.Unix() {
			t.Fatalf("unexpected timestamp: %d", hb.Timestamp)
		}
		if hb.Os != "windows" {
			t.Fatalf("unexpected OS: %s", hb.Os)
		}
	}
}

func TestBuildCommandResultMessage(t *testing.T) {
	msg := buildCommandResultMessage("cmd-123", "success", "done")
	if msg == nil {
		t.Fatal("expected command result message, got nil")
	}

	result := msg.GetCommandResult()
	if result == nil {
		t.Fatal("expected command result payload")
	}
	if result.CommandId != "cmd-123" {
		t.Fatalf("unexpected command id: %s", result.CommandId)
	}
	if result.Status != "success" {
		t.Fatalf("unexpected status: %s", result.Status)
	}
	if result.Message != "done" {
		t.Fatalf("unexpected message: %s", result.Message)
	}
}

func TestLoadDefaultConfig(t *testing.T) {
	cfg := config.Load()

	if cfg.ServerAddress == "" {
		t.Fatal("server address should not be empty")
	}
	if cfg.HeartbeatInterval == 0 {
		t.Fatal("heartbeat interval should not be zero")
	}
	if cfg.HeartbeatJitter == 0 {
		t.Fatal("heartbeat jitter should not be zero")
	}
	if cfg.ReconnectBackoff == 0 {
		t.Fatal("reconnect backoff should not be zero")
	}
}
