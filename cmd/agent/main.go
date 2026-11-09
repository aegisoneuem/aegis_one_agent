package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/aegisoneuem/aegis_one_agent/internal/buffer"
	"github.com/aegisoneuem/aegis_one_agent/internal/capabilities"
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	"github.com/aegisoneuem/aegis_one_agent/internal/softdist"
	"github.com/aegisoneuem/aegis_one_proto/commandsign"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

func buildHeartbeatMessage(deviceID, version, osName, hostname string, now time.Time) *pb.AgentMessage {
	return &pb.AgentMessage{
		Payload: &pb.AgentMessage_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				DeviceId:          deviceID,
				AgentVersion:      version,
				Os:                osName,
				Hostname:          hostname,
				Timestamp:         now.Unix(),
				SchemaVersion:     capabilities.SchemaVersion(),
				AgentCapabilities: capabilities.List(),
				DeviceType:        capabilities.DeviceType(),
			},
		},
	}
}

func buildCommandResultMessage(commandID, status, message string) *pb.AgentMessage {
	return &pb.AgentMessage{
		Payload: &pb.AgentMessage_CommandResult{
			CommandResult: &pb.CommandResult{
				CommandId: commandID,
				Status:    status,
				Message:   message,
			},
		},
	}
}

func isAllowedCommand(cfg config.Config, commandType string) bool {
	for _, allowed := range cfg.AllowedCommands {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(commandType)) {
			return true
		}
	}
	return false
}

// devPublicKeyB64 is the BUILT-IN dev/test command-signing public key. A public
// key is not a secret, but every deployment hard-wired to one key is wrong — so
// production must override it via AEGIS_CMD_VERIFY_KEY(_FILE). It is only the
// fallback for local/dev runs, and its use is logged loud at startup.
const devPublicKeyB64 = "sluXzV/LHkLeAR+1ldhlAZ+hBofkJWgCrVAkVicYLPg="

// cmdVerifyKey is the Ed25519 public key used to verify signed server commands.
// It is loaded once at startup (loadCommandVerifyKey) and read-only thereafter.
var cmdVerifyKey ed25519.PublicKey

// cmdVerifyKeySource describes where the verify key came from, and whether it is
// the built-in DEV key (not for production). Read-only after loadCommandVerifyKey;
// surfaced in the security posture.
var (
	cmdVerifyKeySource = "unloaded"
	cmdVerifyKeyIsDev  bool
)

// loadCommandVerifyKey resolves the command-signing public key from config
// (AEGIS_CMD_VERIFY_KEY base64, or AEGIS_CMD_VERIFY_KEY_FILE), falling back to the
// built-in dev key with a loud warning. A configured-but-invalid key is fatal
// (fail-loud: without a valid key the agent cannot trust any command).
func loadCommandVerifyKey(cfg config.Config) error {
	b64 := strings.TrimSpace(cfg.CmdVerifyKey)
	source := "config (AEGIS_CMD_VERIFY_KEY)"
	if b64 == "" && strings.TrimSpace(cfg.CmdVerifyKeyFile) != "" {
		data, err := os.ReadFile(cfg.CmdVerifyKeyFile)
		if err != nil {
			return fmt.Errorf("read command verify key file %q: %w", cfg.CmdVerifyKeyFile, err)
		}
		b64 = strings.TrimSpace(string(data))
		source = "file (" + cfg.CmdVerifyKeyFile + ")"
	}
	usingDev := false
	if b64 == "" {
		b64 = devPublicKeyB64
		source = "built-in dev key"
		usingDev = true
	}
	pub, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return fmt.Errorf("decode command verify key: %w", err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("command verify key must be %d bytes, got %d", ed25519.PublicKeySize, len(pub))
	}
	cmdVerifyKey = ed25519.PublicKey(pub)
	cmdVerifyKeySource = source
	cmdVerifyKeyIsDev = usingDev
	if usingDev {
		slog.Warn("command signatures verified with the BUILT-IN DEV key — set AEGIS_CMD_VERIFY_KEY (or _FILE) for production")
	} else {
		slog.Info("command verify key loaded", "source", source)
	}
	return nil
}

// verifyCommand checks the signature over the WHOLE command envelope — type,
// command_id and payload — not just the payload. Dispatch routes on cmd.Type,
// so binding the type (and id) means a valid signature for one command cannot
// be replayed under a different type/id. Server and agent share the exact
// canonical encoding via the commandsign package, so they cannot drift.
func verifyCommand(cmd *pb.Command) bool {
	if len(cmdVerifyKey) != ed25519.PublicKeySize {
		return false // no key loaded → trust nothing (fail-loud)
	}
	return commandsign.Verify(cmdVerifyKey, cmd.Type, cmd.CommandId, cmd.Payload, cmd.Signature)
}

func executeCommand(cfg config.Config, commandType string, payload string) (string, error) {
	if !isAllowedCommand(cfg, commandType) {
		return "", fmt.Errorf("unsupported command type: %s", commandType)
	}

	var cmd *exec.Cmd
	commandType = strings.ToLower(strings.TrimSpace(commandType))

	switch commandType {
	case "echo":
		value := strings.TrimSpace(payload)
		if value == "" {
			return "", fmt.Errorf("empty echo payload")
		}
		if runtime.GOOS == "windows" {
			cmd = exec.Command("cmd", "/C", "echo", value)
		} else {
			cmd = exec.Command("sh", "-c", "printf '%s\\n' \""+value+"\"")
		}
	case "hostname":
		if runtime.GOOS == "windows" {
			cmd = exec.Command("hostname")
		} else {
			cmd = exec.Command("hostname")
		}
	case "whoami":
		cmd = exec.Command("whoami")
	default:
		return "", fmt.Errorf("unsupported command type: %s", commandType)
	}

	cmdCtx, cancel := context.WithTimeout(context.Background(), cfg.CommandTimeout)
	defer cancel()
	cmd = exec.CommandContext(cmdCtx, cmd.Path, cmd.Args[1:]...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("command timed out after %s", cfg.CommandTimeout)
		}
		return "", err
	}
	return string(out), nil
}

func sendHeartbeat(stream pb.AgentControlPlane_ConnectClient, cfg config.Config) error {
	heartbeat := buildHeartbeatMessage(
		cfg.DeviceID,
		cfg.AgentVersion,
		cfg.OS,
		cfg.Hostname,
		time.Now(),
	)

	if err := stream.Send(heartbeat); err != nil {
		return err
	}

	slog.Debug("heartbeat sent") // frequent → debug so it does not flood info at scale
	return nil
}

func runAgent(rootCtx context.Context, cfg config.Config, buf buffer.Buffer, outbox chan *pb.AgentMessage, installJobs chan installJob, softdist *softdistInstaller, stg *stager, baseline *baselineStore, cabs *cabManifestHandler, keys secure.KeyProvider, scanTrigger chan<- struct{}, deviceStateTrigger chan<- struct{}, fullInventoryReconnectTrigger chan<- struct{}, drainSignal <-chan struct{}) error {
	connFail := 0 // consecutive connection failures, to throttle reconnect logging
	tlsLogged := false
	// Extensible command dispatch: one registry of per-type handlers (no hardcoded
	// switch), so new task types bolt on without touching the receive loop.
	disp := newDispatcher(cfg, outbox, installJobs, softdist, stg, scanTrigger, deviceStateTrigger)
	for {
		if rootCtx.Err() != nil {
			return nil // graceful shutdown requested
		}
		// (Re)load mTLS creds each connect so a renewed cert is picked up on
		// reconnect. mTLS is mandatory; keys protects the private key at rest.
		creds, err := secure.ClientCredentials(cfg.TLSCAFile, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSServerName, keys)
		if err != nil {
			logReconnect(&connFail, cfg.ServerAddress, err)
			sleepOrDone(rootCtx, cfg.ReconnectBackoff)
			continue
		}
		if !tlsLogged {
			slog.Info("mTLS enabled", "server_name", cfg.TLSServerName, "ca", cfg.TLSCAFile, "client_cert", cfg.TLSCertFile)
			tlsLogged = true
		}
		// 150 MB max message size matches the mock server. Needed because
		// a signed file_operation write_bytes command carries inline bytes
		// (JSON base64; up to AEGIS_FILE_WRITE_MAX_BYTES, default 100 MB).
		// Default gRPC 4 MB is too small for that payload.
		const maxMsgBytes = 150 * 1024 * 1024
		conn, err := grpc.NewClient(cfg.ServerAddress,
			grpc.WithTransportCredentials(creds),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                cfg.KeepaliveTime,
				Timeout:             cfg.KeepaliveTimeout,
				PermitWithoutStream: true, // keep the link alive across reconnect gaps
			}),
			grpc.WithDefaultCallOptions(
				grpc.MaxCallRecvMsgSize(maxMsgBytes),
				grpc.MaxCallSendMsgSize(maxMsgBytes),
			),
		)
		if err != nil {
			logReconnect(&connFail, cfg.ServerAddress, err)
			sleepOrDone(rootCtx, cfg.ReconnectBackoff)
			continue
		}
		ctx, cancel := context.WithCancel(rootCtx) // per-connection ctx derived from the root
		client := pb.NewAgentControlPlaneClient(conn)
		stream, err := client.Connect(ctx)
		if err != nil {
			logReconnect(&connFail, cfg.ServerAddress, err)
			cancel()
			conn.Close()
			sleepOrDone(rootCtx, cfg.ReconnectBackoff)
			continue
		}
		if connFail > 0 {
			slog.Info("connected to server", "after_failed_attempts", connFail)
			connFail = 0
		}
		agentHealth.update(func(h *healthState) { h.connected = true })
		// Tier-3 reconnect snapshot: on connect-after-disconnect, fire a
		// full-inventory trigger. runFullInventoryReport enforces the flap
		// floor (ReconnectSnapshotMinInterval) so a flapping NIC is safe.
		signalDrain(fullInventoryReconnectTrigger)

		// Cert renewal: periodically check if the client cert is nearing expiry;
		// if so, renew it over THIS mTLS channel (no token) and reconnect to use it.
		go func() {
			defer recoverLog("cert-renewal")
			ticker := time.NewTicker(cfg.CertCheckInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if renewed, err := renewIfDue(ctx, client, cfg); err != nil {
						slog.Error("cert renewal failed", "error", err.Error())
					} else if renewed {
						cancel() // tear down so the next reconnect loads the new cert
						return
					}
				}
			}
		}()

		// Sender: the ONLY goroutine that writes to the stream (gRPC Send is not
		// safe for concurrent use). It drains the outbox (command results, install
		// status), beats the heartbeat, and flushes buffered patch scan results.
		go func() {
			defer cancel()
			defer recoverLog("sender") // runs before cancel (LIFO): catch panic, then tear down
			interval := cfg.HeartbeatInterval
			if cfg.HeartbeatJitter > 0 {
				interval += time.Duration(rand.Int63n(int64(cfg.HeartbeatJitter)))
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			drainReports(stream, buf) // flush backlog on (re)connect
			for {
				select {
				case <-ctx.Done():
					return
				case msg := <-outbox:
					if err := stream.Send(msg); err != nil {
						slog.Warn("send failed, connection dropping", "error", err.Error())
						return
					}
				case <-drainSignal:
					// A detection goroutine buffered a report — flush promptly
					// instead of waiting for the next heartbeat.
					drainReports(stream, buf)
				case <-ticker.C:
					if err := sendHeartbeat(stream, cfg); err != nil {
						slog.Warn("heartbeat failed, connection dropping", "error", err.Error())
						return
					}
					agentHealth.update(func(h *healthState) { h.lastHeartbeat = time.Now() })
					drainReports(stream, buf)
				}
			}
		}()

		// Receiver: reads commands and dispatches them. It never writes to the
		// stream — replies go through the outbox, and patch installs through the
		// serial install queue.
		go func() {
			defer cancel()
			defer recoverLog("receiver") // catch a panic, then tear down (reconnect)
			for {
				msg, err := stream.Recv()
				if err != nil {
					slog.Warn("server stream closed", "error", err.Error())
					return
				}
				// Server-pushed patch baseline for the offline self-diff.
				if bl := msg.GetPatchBaseline(); bl != nil {
					if err := baseline.save(bl); err != nil {
						slog.Warn("could not cache patch baseline", "error", err.Error())
					} else {
						slog.Info("patch baseline cached", "build", bl.Build, "latest_kbs", len(bl.LatestKbs))
					}
					continue
				}
				// Server-reported cab version — fetch a newer cab if ours is stale.
				if cm := msg.GetCabManifest(); cm != nil {
					cabs.onManifest(ctx, client, cm.Version, cm.Sha256)
					continue
				}
				cmd := msg.GetCommand()
				if cmd == nil {
					continue
				}
				slog.Info("command received", "command_id", cmd.CommandId, "type", cmd.Type,
					"actor", attributionOrUnknown(cmd.Actor),
					"source", attributionOrUnknown(cmd.Source),
					"issued_at", cmd.IssuedAt)
				if !verifyCommand(cmd) {
					slog.Warn("command signature invalid — rejecting", "command_id", cmd.CommandId)
					recordAudit(ctx, outbox, cfg.DeviceID, cmd, false, "rejected", "signature verification failed")
					send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, "rejected", "signature verification failed"))
					continue
				}
				recordAudit(ctx, outbox, cfg.DeviceID, cmd, true, "accepted", "")

				// Route to the per-type handler (registry; no hardcoded switch). Patch
				// install/uninstall queue on the serial worker; stage/reboot/scan run their
				// own path; unknown types fall through to executeCommand.
				disp.dispatch(ctx, client, cmd)
			}
		}()

		<-ctx.Done() // sender or receiver failed (or shutdown); tear down and retry
		agentHealth.update(func(h *healthState) { h.connected = false })
		conn.Close()
		sleepOrDone(rootCtx, cfg.ReconnectBackoff)
	}
}

// sleepOrDone waits for d, or returns early if ctx is cancelled (shutdown).
func sleepOrDone(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// logReconnect throttles noisy reconnect logging: the first failure is a WARN,
// subsequent consecutive failures drop to DEBUG so a long server outage does not
// flood the log (important at 50k-device scale).
func logReconnect(count *int, server string, err error) {
	*count++
	if *count == 1 {
		slog.Warn("cannot reach server, will keep retrying", "server", server, "error", err.Error())
	} else {
		slog.Debug("still cannot reach server", "server", server, "attempts", *count, "error", err.Error())
	}
}

// send queues a message for the sender goroutine, unless the connection is
// already tearing down.
func send(ctx context.Context, outbox chan<- *pb.AgentMessage, msg *pb.AgentMessage) {
	select {
	case outbox <- msg:
	case <-ctx.Done():
	}
}

func main() {
	// A subcommand, if given, must be recognized — otherwise error instead of
	// silently running the agent (a typo like "instal" must not start it).
	if len(os.Args) > 1 {
		arg := os.Args[1]
		if arg == "config" {
			path := config.FilePath()
			if err := config.WriteTemplate(path); err != nil {
				fmt.Fprintf(os.Stderr, "config: %v\n", err)
				os.Exit(1)
			}
			fmt.Println("wrote config template to", path)
			return
		}
		if arg == "writeconfig" {
			runWriteConfig(os.Args[2:]) // used by the MSI to populate config.json
			return
		}
		if runControlCommand(arg) { // install/uninstall/start/stop (Windows)
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\nusage: %s [config|writeconfig|install|uninstall|start|stop]\n(no argument runs the agent)\n",
			arg, filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	// No args: run as a Windows service when launched by the SCM; otherwise run
	// in the console (dev). Platform-split in service_windows.go / service_other.go.
	runAgentEntry()
}

// runWriteConfig writes config.json from flags — invoked by the MSI custom action
// so install-time values (server address, enroll token, server name, install dir)
// land in the config without hand-editing. Blank flags keep the defaults.
func runWriteConfig(args []string) {
	fs := flag.NewFlagSet("writeconfig", flag.ContinueOnError)
	out := fs.String("out", "", `config.json path (default <install-dir>\SKVC\config.json)`)
	installDir := fs.String("install-dir", "", "install root (default: the directory of this exe)")
	server := fs.String("server", "", "server address (host:port)")
	serverName := fs.String("server-name", "", "expected server certificate name (TLS SAN)")
	token := fs.String("token", "", "per-tenant enrollment token")
	ca := fs.String("ca", "", `CA cert to install (copied to <install-dir>\SKVC\certs\ca.crt)`)
	_ = fs.Parse(args)

	// Default the install root to THIS exe's directory.
	root := strings.TrimRight(strings.TrimSpace(*installDir), `\/`)
	if root == "" {
		if exe, err := os.Executable(); err == nil {
			root = filepath.Dir(exe)
		} else {
			root = `C:\Program Files\AegisOne\Client`
		}
	}
	outPath := strings.TrimSpace(*out)
	if outPath == "" {
		outPath = filepath.Join(root, "SKVC", "config.json")
	}

	overrides := map[string]string{
		"AEGIS_SERVER_ADDRESS":  *server,
		"AEGIS_TLS_SERVER_NAME": *serverName,
		"AEGIS_ENROLL_TOKEN":    *token,
	}
	if err := config.WriteConfig(outPath, root, overrides); err != nil {
		fmt.Fprintf(os.Stderr, "writeconfig: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("wrote config to", outPath)

	if strings.TrimSpace(*ca) != "" {
		dst := filepath.Join(root, "SKVC", "certs", "ca.crt")
		if err := copyFile(*ca, dst); err != nil {
			fmt.Fprintf(os.Stderr, "writeconfig: install CA cert: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("installed CA cert to", dst)
	}
}

// copyFile copies src to dst, creating the destination directory.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// runProcess is the agent's actual work: set up everything and run the connect
// loop until ctx is cancelled (graceful shutdown, e.g. a service Stop). Both the
// console and Windows-service entrypoints call this.
func runProcess(ctx context.Context) {
	cfg := config.Load()

	// The agent always logs to a file (a service has no console). Default to
	// SKVC\logs\agent.log when AEGIS_LOG_FILE is not set; stdout is also written.
	logFile := cfg.LogFile
	if logFile == "" {
		logFile = filepath.Join(cfg.SecureDir, "logs", "agent.log")
	}
	logSink, err := logging.Init(cfg.LogLevel, logFile, logging.Rotation{
		MaxAgeDays: cfg.LogMaxAgeDays,
	})
	if err != nil {
		fmt.Println("failed to initialize logging:", err)
	}
	if logSink != nil {
		defer logSink.Close()
	}

	applyTuning(cfg) // push configurable tuning knobs into the patch package

	// Make config visibility explicit (a service has no console to debug from).
	if path, loaded, keys, cerr := config.LoadedFrom(); loaded {
		slog.Info("config file loaded", "path", path, "keys", keys)
	} else if cerr != nil {
		slog.Error("config file present but INVALID — using env/defaults; fix it", "path", path, "error", cerr.Error())
	} else {
		slog.Info("no config file found — using env vars / defaults", "looked_at", path)
	}

	// Resolve this device's stable unique identity. The hardware fingerprint lets
	// us detect a clone/reimage (id baked into a template, machine changed) and
	// issue a fresh id instead of colliding.
	// Durable identity: if a prior uninstall/reinstall wiped SKVC\identity.json,
	// restore it from the machine store (HKLM) BEFORE resolving — so the device
	// keeps its id instead of regenerating. No-op when the file is present.
	restoreIdentityIfMissing(cfg.SecureDir)
	signals := hardwareSignals()
	fingerprint := hardwareFingerprint()
	deviceID, idSource, err := resolveDeviceID(cfg.DeviceID, cfg.SecureDir, signals)
	if err != nil {
		log.Fatalf("device identity: %v", err)
	}
	cfg.DeviceID = deviceID
	slog.Info("device identity resolved", "device_id", deviceID, "source", idSource,
		"fingerprint", fingerprint, "signals", len(signals), "computer_id", loadComputerID(cfg.SecureDir))

	// Enrollment (Option B): if no client cert is provisioned, enroll with the
	// server (keypair+CSR → token-authenticated Enroll → signed per-device cert)
	// before mTLS. No-op when a cert already exists. Fail loud if it cannot proceed
	// (SCM restarts the service and retries).
	// Enroll with retry: a service must stay up even if the server is unreachable
	// at boot (don't exit — SCM would flap it). Retry with backoff until enrolled
	// or shutdown. No-op once a cert exists.
	tpmEK, sysUUID, machGUID := hardwareIDs()
	for {
		effectiveID, err := ensureEnrolled(ctx, cfg, cfg.DeviceID, tpmEK, sysUUID, machGUID, signals)
		if err == nil {
			if effectiveID != cfg.DeviceID {
				slog.Info("device id updated by enrollment reconciliation", "device_id", effectiveID)
				cfg.DeviceID = effectiveID
			}
			break
		}
		slog.Error("enrollment failed — will retry", "error", err.Error(), "retry_in", cfg.ReconnectBackoff.String())
		sleepOrDone(ctx, cfg.ReconnectBackoff)
		if ctx.Err() != nil {
			return // graceful shutdown requested while retrying
		}
	}
	// Identity is now final (device_id + any server-assigned computer_id). Mirror it
	// to the durable machine store so it survives a future uninstall/reinstall.
	mirrorIdentityToDurable(cfg.SecureDir)

	// Local health/status endpoint (localhost only) for monitoring/troubleshooting.
	agentHealth.update(func(h *healthState) {
		h.version, h.deviceID, h.hostname, h.osName, h.enrolled = cfg.AgentVersion, cfg.DeviceID, cfg.Hostname, cfg.OS, true
	})
	startHealthServer(ctx, cfg.HealthAddr)

	slog.Info("Aegis One Agent starting up",
		"server", cfg.ServerAddress,
		"heartbeat_interval", cfg.HeartbeatInterval.String(),
		"heartbeat_jitter", cfg.HeartbeatJitter.String())

	// Load the command-signing public key before anything can accept a command.
	// Fail loud if it is configured but invalid — the agent must never run unable
	// to verify server commands.
	if err := loadCommandVerifyKey(cfg); err != nil {
		log.Fatalf("command verify key: %v", err)
	}

	// Load + re-verify the persisted signed agent policy BEFORE the FIPS gate, so
	// a policy-mandated FIPS floor is honored fail-closed. A tampered/invalid
	// stored policy is ignored (logged), never trusted.
	loadStoredPolicy(cfg.SecureDir)

	// Fail-closed FIPS: if FIPS is required (by AEGIS_REQUIRE_FIPS or the active
	// policy), refuse to start unless the FIPS 140-3 module is active. A
	// compliance customer gets no agent rather than a non-FIPS one.
	if err := enforceFIPS(cfg); err != nil {
		log.Fatalf("FIPS policy: %v", err)
	}

	keys := secure.NewKeyProvider(cfg.SecureDir)
	bufPath := filepath.Join(cfg.SecureDir, "buffer", "patch-buffer.jsonl")
	buf, err := buffer.NewEncryptedFileBuffer(bufPath, keys)
	if err != nil {
		log.Fatalf("failed to create secure offline buffer: %v", err)
	}
	slog.Info("agent starting",
		"secure_dir", cfg.SecureDir,
		"buffer", bufPath,
		"cab", cfg.WsusScanCab,
		"device_state_interval", cfg.DeviceStateInterval.String(),
		"recon_scan_interval", cfg.ReconScanInterval.String())

	// Encrypted patch cache (staging store). The cache directory is created here
	// and always kept, so an admin has a known location to inspect.
	cacheDir := filepath.Join(cfg.SecureDir, "cache")
	tmpDir := filepath.Join(cfg.SecureDir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		log.Fatalf("failed to create temp dir: %v", err)
	}
	// Self-heal: clear any plaintext temp files left behind by an operation that
	// was interrupted (crash/kill/restart mid stage-or-install). Runs before the
	// workers start, so it never races an in-flight operation.
	sweepTempDir(tmpDir)
	// Download dir: where the server delivers runtime/adhoc patch files (distinct
	// from the staging cache). Always kept so admins have a known drop location.
	if err := os.MkdirAll(cfg.DownloadDir, 0o755); err != nil {
		log.Fatalf("failed to create download dir: %v", err)
	}
	slog.Info("download dir ready", "dir", cfg.DownloadDir)
	cache, err := patchcache.New(cacheDir, keys, cfg.CacheMaxBytes, cfg.CacheTTL)
	if err != nil {
		log.Fatalf("failed to open patch cache: %v", err)
	}
	slog.Info("patch cache ready", "dir", cacheDir, "max_bytes", cfg.CacheMaxBytes, "ttl", cfg.CacheTTL.String())

	// outbox serializes everything written to the stream; installJobs feeds the
	// serial patch worker. Both are process-level so the worker (and its queued
	// jobs) survive reconnects.
	outbox := make(chan *pb.AgentMessage, 256)
	installJobs := make(chan installJob, 32)
	// Tracks which installed KBs are awaiting a reboot (for the device-level
	// pending-reboot report). Shared by the installer (writes) and detection (reads).
	rebootStore := newRebootPendingStore(cfg.SecureDir)
	// Authoritative installed-KB set from the last full scan — the install worker
	// reads it as a reliable "already installed?" pre-check; detection refreshes it.
	installedSet := newInstalledStore(cfg.SecureDir)
	inst := &patchInstaller{
		outbox: outbox, cache: cache, tmpDir: tmpDir, deviceID: cfg.DeviceID, rebootStore: rebootStore,
		installedSet: installedSet,
		timeout:      cfg.InstallTimeout,
		maxAttempts:  cfg.InstallMaxAttempts, retryBackoff: cfg.InstallRetryBackoff,
	}
	go inst.patchWorker(installJobs)

	// Software distribution worker: executes signed software_deploy commands
	// (install/uninstall of catalog packages) on its own serial queue. It shares
	// the encrypted patch cache (server-delivered installers are stored sealed at
	// rest) and the tmp dir (transient decrypt-to-install, secure-deleted after).
	sdi := newSoftdistInstaller(outbox, cfg.DeviceID, cache, tmpDir, cfg.DownloadDir, cfg.SoftdistVerifyAttempts, cfg.SoftdistVerifyInterval, newJobJournal(cfg.SecureDir), cfg.FetchBytesPerSecond, filepath.Join(cfg.SecureDir, "logs"), cfg.LogScriptContent)
	sdi.maxSecretBytes = cfg.SoftdistMaxSecretBytes
	sdi.bundleLimits = softdist.BundleLimits{MaxBytes: cfg.SoftdistBundleMaxBytes, MaxFiles: cfg.SoftdistBundleMaxFiles}
	// Durable-journal recovery runs BEFORE the worker starts consuming new jobs
	// so any jobs that did not reach terminal state before a crash/restart emit
	// RECOVERED_FAILED status (buffered until next reconnect) and leak-free
	// cleanup of orphaned extraction directories happens first.
	sdi.recoverIncompleteJobs()
	go sdi.worker()

	// Stager: server-driven pre-caching of patches (stage_patch) ahead of a
	// scheduled install. Fetches via peer-cache and stores encrypted as staged.
	stg := newStager(outbox, cache, tmpDir, cfg.DeviceID, cfg.FetchBytesPerSecond)

	// Cache janitor: periodic TTL/cap enforcement (infrequent, to stay light).
	go cacheJanitor(cache, cfg.CacheJanitorInterval)

	// Baseline cache for the offline self-diff (server pushes it; Layer 1 uses it).
	baseline := newBaselineStore(cfg.SecureDir)
	// scanTrigger fires a full compliance scan on demand — when a new catalog (cab)
	// lands (set below) — instead of waiting for the weekly tick. Coalescing.
	scanTrigger := make(chan struct{}, 1)
	// deviceStateTrigger fires a Layer-1 DeviceStateReport on demand (resetting
	// the change-detection fingerprint so the next report is sent even when
	// nothing changed). Used by the signed "report_device_state" command (NSH
	// "inventory refresh"). Coalescing.
	deviceStateTrigger := make(chan struct{}, 1)
	// Cab version handler (server reports version; agent fetches if stale, then
	// triggers a scan against the new catalog).
	cabs := newCabManifestHandler(cfg.SecureDir, cfg.WsusScanCab, scanTrigger, cfg.FetchBytesPerSecond)
	// drainSignal lets detection wake the sender to flush the buffer promptly
	// (otherwise a report buffered after connect waits for the next heartbeat).
	drainSignal := make(chan struct{}, 1)

	// fullInventoryReconnectTrigger fires one Tier-3 full snapshot on connect-
	// after-disconnect. The flap floor (cfg.ReconnectSnapshotMinInterval) is
	// enforced INSIDE runFullInventoryReport so a flapping NIC does not DoS
	// the server. The 6-12 h scheduled timer is never suppressed by the floor.
	fullInventoryReconnectTrigger := make(chan struct{}, 1)

	// Audit log: open the local rolling buffer (seeds the chain from any
	// existing file so restarts continue the hash chain). The server
	// remains the AUTHORITATIVE compliance surface via streamed
	// AuditEvent messages; the local buffer is best-effort + offline-
	// grace.
	initAuditLogger(cfg)

	// Security posture: a clear, greppable startup callout of what protections are
	// in force (transport, command authorization, at-rest, audit, FIPS, client
	// cert) PLUS a streamed security_posture attestation so the console shows a
	// per-device "prove it" record and an operator can pull it via audit_query.
	reportSecurityPosture(cfg, outbox, cfg.DeviceID)

	// Detection runs independently and only writes to the buffer; the sender
	// drains it and sends. This keeps the two off each other's stream.
	go runPatchDetection(cfg, buf, baseline, rebootStore, installedSet, scanTrigger, deviceStateTrigger, drainSignal)

	// Tier-3 safety-net: full HW + SW inventory snapshot every
	// FullInventoryInterval (default 6 h) AND on reconnect-after-offline
	// (flap-floored). Collector cost is a few ms; no WMI, no COM, no cgo.
	go runFullInventoryReport(cfg, buf, fullInventoryReconnectTrigger, drainSignal)

	// Watch the servicing store (CBS) and debounce-trigger a re-scan on any local
	// install/uninstall — instant auto-detect without running the heavy scan on a
	// timer (event-driven; no-op on non-Windows).
	go watchServicingForRescan(ctx, scanTrigger, deviceStateTrigger)

	if err := runAgent(ctx, cfg, buf, outbox, installJobs, sdi, stg, baseline, cabs, keys, scanTrigger, deviceStateTrigger, fullInventoryReconnectTrigger, drainSignal); err != nil {
		slog.Error("agent exited with error", "error", err.Error())
	}
	slog.Info("agent stopped")
}
