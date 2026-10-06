package config

import (
	"bytes"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"time"
)

// fileCfg holds settings loaded from the config file (keys are the same env-var
// names). Precedence: environment variable > config file > built-in default.
// Loaded once at the start of Load().
var fileCfg map[string]string

// FilePath is where the agent reads its config file (AEGIS_CONFIG_FILE, else the
// default next to the install dir). A Windows service cannot see a user's cmd
// environment, so the file is how it gets non-default settings.
func FilePath() string {
	if v := strings.TrimSpace(os.Getenv("AEGIS_CONFIG_FILE")); v != "" {
		return v
	}
	// Everything the agent owns lives under SKVC, so the config file does too.
	return `C:\Program Files\AegisOne\Client\SKVC\config.json`
}

var (
	fileLoadedPath string
	fileLoaded     bool
	fileLoadedKeys int
	fileLoadErr    error
)

// LoadedFrom reports the config file path, whether it was loaded, how many keys
// it set, and any error (e.g. a file that exists but failed to parse) — so
// startup can log config visibility clearly.
func LoadedFrom() (path string, loaded bool, keys int, err error) {
	return fileLoadedPath, fileLoaded, fileLoadedKeys, fileLoadErr
}

func loadFileConfig(path string) map[string]string {
	fileLoadedPath = path
	m := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fileLoadErr = err // exists but unreadable
		}
		return m // no file → env/defaults only
	}
	// Strip a UTF-8 BOM — PowerShell's Set-Content -Encoding utf8 adds one, and
	// Go's JSON parser rejects it.
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if jerr := json.Unmarshal(data, &m); jerr != nil {
		fileLoadErr = jerr // file present but invalid JSON
		return map[string]string{}
	}
	fileLoaded = true
	fileLoadedKeys = len(m)
	return m
}

// resolve returns the value for key from the environment (highest priority) then
// the config file; "" means neither set it (caller uses its default).
func resolve(key string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	if v, ok := fileCfg[key]; ok {
		return strings.TrimSpace(v)
	}
	return ""
}

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
	WsusScanCab          string        // path to the server-provided wsusscn2.cab; empty disables the Layer 4 reconciliation scan
	DeviceStateInterval  time.Duration // Layer 1: how often to check local installed-KB/build state (sent on change)
	ReconScanInterval    time.Duration // Layer 4: how often to run the heavier offline cab reconciliation scan (weekly)
	SecureDir            string        // agent secure dir: DPAPI key + encrypted buffer + patch cache live here
	DownloadDir          string        // where the server delivers runtime/adhoc patch files (distinct from the staging cache)
	TLSCAFile            string        // CA bundle to verify the server (mTLS)
	TLSCertFile          string        // agent client certificate (mTLS)
	TLSKeyFile           string        // agent client private key (mTLS)
	TLSServerName        string        // expected server certificate SAN
	CmdVerifyKey         string        // base64 Ed25519 public key for verifying signed commands
	CmdVerifyKeyFile     string        // path to a file holding the base64 Ed25519 public key (alt to CmdVerifyKey)
	CacheMaxBytes        int64         // patch cache size cap (bytes); pre-cache/staging budget
	CacheTTL             time.Duration // delete unused (non-staged) cached files older than this
	CacheJanitorInterval time.Duration // how often the cache janitor runs TTL/cap enforcement
	InstallTimeout       time.Duration // max time for a single DISM install
	InstallDiskMultiplier int64        // require free disk >= this * patch size before install
	InstallMaxAttempts   int           // total DISM attempts on a transient failure
	InstallRetryBackoff  time.Duration // wait between transient-failure retries
}

// WriteTemplate writes a config.json template (keys = env-var names) with the
// current defaults, for an admin to edit. Durations/sizes are in seconds/bytes.
func WriteTemplate(path string) error {
	tmpl := map[string]string{
		"AEGIS_SERVER_ADDRESS":        "127.0.0.1:50051",
		"AEGIS_LOG_LEVEL":             "info",
		"AEGIS_LOG_MAX_AGE_DAYS":      "10",
		"AEGIS_DEVICE_ID":             "",
		"AEGIS_WSUS_SCAN_CAB":         `C:\Program Files\AegisOne\Client\wsusscn2.cab`,
		"AEGIS_DEVICE_STATE_INTERVAL": "300",
		"AEGIS_RECON_SCAN_INTERVAL":   "604800",
		"AEGIS_SECURE_DIR":            `C:\Program Files\AegisOne\Client\SKVC`,
		"AEGIS_DOWNLOAD_DIR":          `C:\Program Files\AegisOne\Client\SKVC\download`,
		"AEGIS_TLS_CA":                `C:\Program Files\AegisOne\Client\SKVC\certs\ca.crt`,
		"AEGIS_TLS_CERT":              `C:\Program Files\AegisOne\Client\SKVC\certs\client.crt`,
		"AEGIS_TLS_KEY":               `C:\Program Files\AegisOne\Client\SKVC\certs\client.key`,
		"AEGIS_TLS_SERVER_NAME":       "aegis-server",
		"AEGIS_HEARTBEAT_INTERVAL":    "300",
		"AEGIS_KEEPALIVE_TIME":        "60",
		"AEGIS_CACHE_MAX_BYTES":       "2147483648",
		"AEGIS_CACHE_TTL":             "86400",
		"AEGIS_INSTALL_TIMEOUT":       "1800",
	}
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return err
	}
	if dir := pathDir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
	}
	return os.WriteFile(path, data, 0o644)
}

func pathDir(p string) string {
	i := strings.LastIndexAny(p, `\/`)
	if i < 0 {
		return ""
	}
	return p[:i]
}

func durationFromEnv(key string, fallback time.Duration) time.Duration {
	value := resolve(key)
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
	if value := resolve(key); value != "" {
		return value
	}
	return fallback
}

func int64FromEnv(key string, fallback int64) int64 {
	value := resolve(key)
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
	fileCfg = loadFileConfig(FilePath()) // env > file > default

	allowed := strings.Split(getenv("AEGIS_ALLOWED_COMMANDS", "hostname,whoami,echo"), ",")
	for i := range allowed {
		allowed[i] = strings.TrimSpace(strings.ToLower(allowed[i]))
	}

	cfg := Config{
		ServerAddress:     getenv("AEGIS_SERVER_ADDRESS", "localhost:50051"),
		LogLevel:          getenv("AEGIS_LOG_LEVEL", "info"),
		LogFile:           getenv("AEGIS_LOG_FILE", ""),
		LogMaxAgeDays:     int(int64FromEnv("AEGIS_LOG_MAX_AGE_DAYS", 10)),
		DeviceID:          getenv("AEGIS_DEVICE_ID", ""), // empty → agent generates + persists a stable UUID
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
		WsusScanCab:         getenv("AEGIS_WSUS_SCAN_CAB", ""),
		DeviceStateInterval: durationFromEnv("AEGIS_DEVICE_STATE_INTERVAL", 5*time.Minute),
		ReconScanInterval:   durationFromEnv("AEGIS_RECON_SCAN_INTERVAL", 7*24*time.Hour),
		SecureDir:         getenv("AEGIS_SECURE_DIR", `C:\Program Files\AegisOne\Client\SKVC`),
		DownloadDir:          getenv("AEGIS_DOWNLOAD_DIR", `C:\Program Files\AegisOne\Client\SKVC\download`),
		TLSCAFile:            getenv("AEGIS_TLS_CA", `C:\Program Files\AegisOne\Client\SKVC\certs\ca.crt`),
		TLSCertFile:          getenv("AEGIS_TLS_CERT", `C:\Program Files\AegisOne\Client\SKVC\certs\client.crt`),
		TLSKeyFile:           getenv("AEGIS_TLS_KEY", `C:\Program Files\AegisOne\Client\SKVC\certs\client.key`),
		TLSServerName:        getenv("AEGIS_TLS_SERVER_NAME", "localhost"),
		CmdVerifyKey:         getenv("AEGIS_CMD_VERIFY_KEY", ""),
		CmdVerifyKeyFile:     getenv("AEGIS_CMD_VERIFY_KEY_FILE", ""),
		CacheMaxBytes:        int64FromEnv("AEGIS_CACHE_MAX_BYTES", 2<<30), // 2 GiB
		CacheTTL:             durationFromEnv("AEGIS_CACHE_TTL", 24*time.Hour),
		CacheJanitorInterval: durationFromEnv("AEGIS_CACHE_JANITOR_INTERVAL", time.Hour),
		InstallTimeout:       durationFromEnv("AEGIS_INSTALL_TIMEOUT", 30*time.Minute),
		InstallDiskMultiplier: int64FromEnv("AEGIS_INSTALL_DISK_MULTIPLIER", 3),
		InstallMaxAttempts:   int(int64FromEnv("AEGIS_INSTALL_MAX_ATTEMPTS", 2)),
		InstallRetryBackoff:  durationFromEnv("AEGIS_INSTALL_RETRY_BACKOFF", 30*time.Second),
	}
	if cfg.Hostname == "" {
		cfg.Hostname = "localhost"
	}
	if cfg.OS == "" {
		cfg.OS = "windows"
	}
	return cfg
}
