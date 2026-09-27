package config

import (
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ServerAddress     string
	LogLevel          string
	LogFile           string
	DeviceID          string
	AgentVersion      string
	Hostname          string
	OS                string
	HeartbeatInterval time.Duration
	HeartbeatJitter   time.Duration
	ReconnectBackoff  time.Duration
	CommandTimeout    time.Duration
	AllowedCommands   []string
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func Load() Config {
	allowed := strings.Split(getenv("AEGIS_ALLOWED_COMMANDS", "hostname,whoami,echo"), ",")
	for i := range allowed {
		allowed[i] = strings.TrimSpace(strings.ToLower(allowed[i]))
	}

	cfg := Config{
		ServerAddress:     getenv("AEGIS_SERVER_ADDRESS", "localhost:50051"),
		LogLevel:          getenv("AEGIS_LOG_LEVEL", "info"),
		LogFile:           getenv("AEGIS_LOG_FILE", ""),
		DeviceID:          getenv("AEGIS_DEVICE_ID", "test-device-001"),
		AgentVersion:      getenv("AEGIS_AGENT_VERSION", "0.0.1"),
		Hostname:          getenv("AEGIS_HOSTNAME", "localhost"),
		OS:                getenv("AEGIS_OS", "windows"),
		HeartbeatInterval: durationFromEnv("AEGIS_HEARTBEAT_INTERVAL", 30*time.Second),
		HeartbeatJitter:   durationFromEnv("AEGIS_HEARTBEAT_JITTER", 5*time.Second),
		ReconnectBackoff:  durationFromEnv("AEGIS_RECONNECT_BACKOFF", 10*time.Second),
		CommandTimeout:    durationFromEnv("AEGIS_COMMAND_TIMEOUT", 10*time.Second),
		AllowedCommands:   allowed,
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "localhost"
	}
	if cfg.OS == "" {
		cfg.OS = "windows"
	}
	return cfg
}
