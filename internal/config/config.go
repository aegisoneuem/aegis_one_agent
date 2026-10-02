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
	LogMaxAgeDays     int // retention: keep this many days of daily log files
	DeviceID          string
	AgentVersion      string
	Hostname          string
	OS                string
	HeartbeatInterval time.Duration
	HeartbeatJitter   time.Duration
	KeepaliveTime     time.Duration // gRPC HTTP/2 keepalive ping interval (dead-conn detection)
	KeepaliveTimeout  time.Duration // how long to wait for a keepalive ping ack
	ReconnectBackoff  time.Duration
	CommandTimeout    time.Duration
	AllowedCommands   []string
	WsusScanCab          string        // path to the server-provided wsusscn2.cab; empty disables local patch scan
	PatchScanInterval    time.Duration // how often the agent runs the local offline patch scan
	SecureDir            string        // agent secure dir: DPAPI key + encrypted buffer + patch cache live here
	DownloadDir          string        // where the server delivers runtime/adhoc patch files (distinct from the staging cache)
	TLSCAFile            string        // CA bundle to verify the server (mTLS)
	TLSCertFile          string        // agent client certificate (mTLS)
	TLSKeyFile           string        // agent client private key (mTLS)
	TLSServerName        string        // expected server certificate SAN
	CacheMaxBytes        int64         // patch cache size cap (bytes); pre-cache/staging budget
	CacheTTL             time.Duration // delete unused (non-staged) cached files older than this
	CacheJanitorInterval time.Duration // how often the cache janitor runs TTL/cap enforcement
	InstallTimeout       time.Duration // max time for a single DISM install
	InstallDiskMultiplier int64        // require free disk >= this * patch size before install
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

func int64FromEnv(key string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
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
		LogMaxAgeDays:     int(int64FromEnv("AEGIS_LOG_MAX_AGE_DAYS", 10)),
		DeviceID:          getenv("AEGIS_DEVICE_ID", "test-device-001"),
		AgentVersion:      getenv("AEGIS_AGENT_VERSION", "0.0.1"),
		Hostname:          getenv("AEGIS_HOSTNAME", "localhost"),
		OS:                getenv("AEGIS_OS", "windows"),
		HeartbeatInterval: durationFromEnv("AEGIS_HEARTBEAT_INTERVAL", 5*time.Minute),
		HeartbeatJitter:   durationFromEnv("AEGIS_HEARTBEAT_JITTER", 30*time.Second),
		KeepaliveTime:     durationFromEnv("AEGIS_KEEPALIVE_TIME", 60*time.Second),
		KeepaliveTimeout:  durationFromEnv("AEGIS_KEEPALIVE_TIMEOUT", 20*time.Second),
		ReconnectBackoff:  durationFromEnv("AEGIS_RECONNECT_BACKOFF", 10*time.Second),
		CommandTimeout:    durationFromEnv("AEGIS_COMMAND_TIMEOUT", 10*time.Second),
		AllowedCommands:   allowed,
		WsusScanCab:       getenv("AEGIS_WSUS_SCAN_CAB", ""),
		PatchScanInterval: durationFromEnv("AEGIS_PATCH_SCAN_INTERVAL", 6*time.Hour),
		SecureDir:         getenv("AEGIS_SECURE_DIR", `C:\Program Files\AegisOne\Client\SKVC`),
		DownloadDir:          getenv("AEGIS_DOWNLOAD_DIR", `C:\Program Files\AegisOne\Client\SKVC\download`),
		TLSCAFile:            getenv("AEGIS_TLS_CA", `C:\Program Files\AegisOne\Client\SKVC\certs\ca.crt`),
		TLSCertFile:          getenv("AEGIS_TLS_CERT", `C:\Program Files\AegisOne\Client\SKVC\certs\client.crt`),
		TLSKeyFile:           getenv("AEGIS_TLS_KEY", `C:\Program Files\AegisOne\Client\SKVC\certs\client.key`),
		TLSServerName:        getenv("AEGIS_TLS_SERVER_NAME", "localhost"),
		CacheMaxBytes:        int64FromEnv("AEGIS_CACHE_MAX_BYTES", 2<<30), // 2 GiB
		CacheTTL:             durationFromEnv("AEGIS_CACHE_TTL", 24*time.Hour),
		CacheJanitorInterval: durationFromEnv("AEGIS_CACHE_JANITOR_INTERVAL", time.Hour),
		InstallTimeout:       durationFromEnv("AEGIS_INSTALL_TIMEOUT", 30*time.Minute),
		InstallDiskMultiplier: int64FromEnv("AEGIS_INSTALL_DISK_MULTIPLIER", 3),
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "localhost"
	}
	if cfg.OS == "" {
		cfg.OS = "windows"
	}
	return cfg
}
