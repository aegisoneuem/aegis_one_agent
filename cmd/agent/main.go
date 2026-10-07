package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
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
	"github.com/aegisoneuem/aegis_one_agent/internal/config"
	"github.com/aegisoneuem/aegis_one_agent/internal/logging"
	"github.com/aegisoneuem/aegis_one_agent/internal/patchcache"
	"github.com/aegisoneuem/aegis_one_agent/internal/secure"
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/protobuf/proto"
)

func buildHeartbeatMessage(deviceID, version, osName, hostname string, now time.Time) *pb.AgentMessage {
	return &pb.AgentMessage{
		Payload: &pb.AgentMessage_Heartbeat{
			Heartbeat: &pb.Heartbeat{
				DeviceId:     deviceID,
				AgentVersion: version,
				Os:           osName,
				Hostname:     hostname,
				Timestamp:    now.Unix(),
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
	if usingDev {
		slog.Warn("command signatures verified with the BUILT-IN DEV key — set AEGIS_CMD_VERIFY_KEY (or _FILE) for production")
	} else {
		slog.Info("command verify key loaded", "source", source)
	}
	return nil
}

func verifyCommand(payload, signature []byte) bool {
	if len(cmdVerifyKey) != ed25519.PublicKeySize {
		return false // no key loaded → trust nothing (fail-loud)
	}
	return ed25519.Verify(cmdVerifyKey, payload, signature)
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

func runAgent(rootCtx context.Context, cfg config.Config, buf buffer.Buffer, outbox chan *pb.AgentMessage, installJobs chan installJob, stg *stager, baseline *baselineStore, cabs *cabManifestHandler, keys secure.KeyProvider, drainSignal <-chan struct{}) error {
	// mTLS is mandatory (security is not toggleable). Load the agent's client
	// credentials once; fail loud if the certs are missing/unreadable rather than
	// silently falling back to an insecure channel. keys protects the private key
	// at rest (DPAPI-sealed) — see secure.ClientCredentials.
	creds, err := secure.ClientCredentials(cfg.TLSCAFile, cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSServerName, keys)
	if err != nil {
		return fmt.Errorf("mTLS setup failed (certs required): %w", err)
	}
	slog.Info("mTLS enabled", "server_name", cfg.TLSServerName, "ca", cfg.TLSCAFile, "client_cert", cfg.TLSCertFile)

	connFail := 0 // consecutive connection failures, to throttle reconnect logging
	for {
		if rootCtx.Err() != nil {
			return nil // graceful shutdown requested
		}
		conn, err := grpc.NewClient(cfg.ServerAddress,
			grpc.WithTransportCredentials(creds),
			grpc.WithKeepaliveParams(keepalive.ClientParameters{
				Time:                cfg.KeepaliveTime,
				Timeout:             cfg.KeepaliveTimeout,
				PermitWithoutStream: true, // keep the link alive across reconnect gaps
			}),
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
				slog.Info("command received", "command_id", cmd.CommandId, "type", cmd.Type)
				if !verifyCommand(cmd.Payload, cmd.Signature) {
					slog.Warn("command signature invalid — rejecting", "command_id", cmd.CommandId)
					send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, "rejected", "signature verification failed"))
					continue
				}

				// install_patch runs on the serial patch worker and reports phased
				// InstallStatus; it must not block the receiver (DISM takes minutes).
				if strings.EqualFold(strings.TrimSpace(cmd.Type), "install_patch") {
					var spec pb.InstallPatchSpec
					if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
						slog.Error("install_patch: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
						send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid install payload"))
						continue
					}
					slog.Info("install_patch: accepted, queued", "command_id", cmd.CommandId, "kb", spec.Kb)
					send(ctx, outbox, &pb.AgentMessage{Payload: &pb.AgentMessage_InstallStatus{
						InstallStatus: &pb.InstallStatus{CommandId: cmd.CommandId, Kb: spec.Kb, Phase: pb.InstallPhase_INSTALL_PHASE_QUEUED}}})
					select {
					case installJobs <- installJob{commandID: cmd.CommandId, spec: &spec}:
					case <-ctx.Done():
						return
					}
					continue
				}

				// stage_patch: pre-cache a patch (encrypted, StateStaged) ahead of a
				// scheduled install. Fetched via peer-cache; runs in the background so
				// it does not block the receiver.
				if strings.EqualFold(strings.TrimSpace(cmd.Type), "stage_patch") {
					var spec pb.InstallPatchSpec
					if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
						slog.Error("stage_patch: invalid payload", "command_id", cmd.CommandId, "error", err.Error())
						send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid stage payload"))
						continue
					}
					slog.Info("stage_patch: accepted", "command_id", cmd.CommandId, "kb", spec.Kb)
					stg.stage(ctx, client, cmd.CommandId, &spec)
					continue
				}

				// reboot / cancel_reboot: server-driven (signed) system reboot.
				if strings.EqualFold(strings.TrimSpace(cmd.Type), "reboot") {
					var spec pb.RebootSpec
					if err := proto.Unmarshal(cmd.Payload, &spec); err != nil {
						send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, "failed", "invalid reboot payload"))
						continue
					}
					status, message := handleReboot(&spec)
					send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, status, message))
					continue
				}
				if strings.EqualFold(strings.TrimSpace(cmd.Type), "cancel_reboot") {
					status, message := handleCancelReboot()
					send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, status, message))
					continue
				}

				output, execErr := executeCommand(cfg, cmd.Type, string(cmd.Payload))
				status, message := "success", "command received and acknowledged"
				if execErr != nil {
					status, message = "failed", execErr.Error()
				} else if output != "" {
					message = strings.TrimSpace(output)
				}
				slog.Info("command executed", "command_id", cmd.CommandId, "type", cmd.Type, "status", status)
				send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, status, message))
			}
		}()

		<-ctx.Done() // sender or receiver failed (or shutdown); tear down and retry
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
		if runControlCommand(arg) { // install/uninstall/start/stop (Windows)
			return
		}
		fmt.Fprintf(os.Stderr, "unknown command %q\nusage: %s [config|install|uninstall|start|stop]\n(no argument runs the agent)\n",
			arg, filepath.Base(os.Args[0]))
		os.Exit(2)
	}
	// No args: run as a Windows service when launched by the SCM; otherwise run
	// in the console (dev). Platform-split in service_windows.go / service_other.go.
	runAgentEntry()
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
	fingerprint := hardwareFingerprint()
	deviceID, idSource, err := resolveDeviceID(cfg.DeviceID, cfg.SecureDir, fingerprint)
	if err != nil {
		log.Fatalf("device identity: %v", err)
	}
	cfg.DeviceID = deviceID
	slog.Info("device identity resolved", "device_id", deviceID, "source", idSource, "fingerprint", fingerprint)

	// Enrollment (Option B): if no client cert is provisioned, enroll with the
	// server (keypair+CSR → token-authenticated Enroll → signed per-device cert)
	// before mTLS. No-op when a cert already exists. Fail loud if it cannot proceed
	// (SCM restarts the service and retries).
	sysUUID, machGUID := hardwareIDs()
	effectiveID, err := ensureEnrolled(ctx, cfg, cfg.DeviceID, sysUUID, machGUID, fingerprint)
	if err != nil {
		log.Fatalf("enrollment: %v", err)
	}
	if effectiveID != cfg.DeviceID {
		slog.Info("device id updated by enrollment reconciliation", "device_id", effectiveID)
		cfg.DeviceID = effectiveID
	}

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
	inst := &patchInstaller{
		outbox: outbox, cache: cache, tmpDir: tmpDir, timeout: cfg.InstallTimeout,
		maxAttempts: cfg.InstallMaxAttempts, retryBackoff: cfg.InstallRetryBackoff,
	}
	go inst.patchWorker(installJobs)

	// Stager: server-driven pre-caching of patches (stage_patch) ahead of a
	// scheduled install. Fetches via peer-cache and stores encrypted as staged.
	stg := newStager(outbox, cache, tmpDir, cfg.DeviceID)

	// Cache janitor: periodic TTL/cap enforcement (infrequent, to stay light).
	go cacheJanitor(cache, cfg.CacheJanitorInterval)

	// Baseline cache for the offline self-diff (server pushes it; Layer 1 uses it).
	baseline := newBaselineStore(cfg.SecureDir)
	// Cab version handler (server reports version; agent fetches if stale).
	cabs := newCabManifestHandler(cfg.SecureDir, cfg.WsusScanCab)
	// drainSignal lets detection wake the sender to flush the buffer promptly
	// (otherwise a report buffered after connect waits for the next heartbeat).
	drainSignal := make(chan struct{}, 1)

	// Detection runs independently and only writes to the buffer; the sender
	// drains it and sends. This keeps the two off each other's stream.
	go runPatchDetection(cfg, buf, baseline, drainSignal)

	if err := runAgent(ctx, cfg, buf, outbox, installJobs, stg, baseline, cabs, keys, drainSignal); err != nil {
		slog.Error("agent exited with error", "error", err.Error())
	}
	slog.Info("agent stopped")
}
