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
	"google.golang.org/grpc/credentials/insecure"
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

const publicKeyB64 = "sluXzV/LHkLeAR+1ldhlAZ+hBofkJWgCrVAkVicYLPg="

func verifyCommand(payload, signature []byte) bool {
	pubBytes, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil {
		return false
	}
	pub := ed25519.PublicKey(pubBytes)
	return ed25519.Verify(pub, payload, signature)
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

	fmt.Printf("Heartbeat sent successfully at %s\n", time.Now().Format(time.RFC3339))
	return nil
}

func runAgent(cfg config.Config, buf buffer.Buffer, outbox chan *pb.AgentMessage, installJobs chan installJob) error {
	for {
		conn, err := grpc.NewClient(cfg.ServerAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Printf("failed to connect to %s: %v", cfg.ServerAddress, err)
			time.Sleep(cfg.ReconnectBackoff)
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		client := pb.NewAgentControlPlaneClient(conn)
		stream, err := client.Connect(ctx)
		if err != nil {
			log.Printf("failed to open stream: %v", err)
			cancel()
			conn.Close()
			time.Sleep(cfg.ReconnectBackoff)
			continue
		}

		// Sender: the ONLY goroutine that writes to the stream (gRPC Send is not
		// safe for concurrent use). It drains the outbox (command results, install
		// status), beats the heartbeat, and flushes buffered patch scan results.
		go func() {
			defer cancel()
			interval := cfg.HeartbeatInterval
			if cfg.HeartbeatJitter > 0 {
				interval += time.Duration(rand.Int63n(int64(cfg.HeartbeatJitter)))
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			drainPatchResults(stream, buf) // flush backlog on (re)connect
			for {
				select {
				case <-ctx.Done():
					return
				case msg := <-outbox:
					if err := stream.Send(msg); err != nil {
						log.Printf("send failed: %v", err)
						return
					}
				case <-ticker.C:
					if err := sendHeartbeat(stream, cfg); err != nil {
						log.Printf("failed to send heartbeat: %v", err)
						return
					}
					drainPatchResults(stream, buf)
				}
			}
		}()

		// Receiver: reads commands and dispatches them. It never writes to the
		// stream — replies go through the outbox, and patch installs through the
		// serial install queue.
		go func() {
			defer cancel()
			for {
				msg, err := stream.Recv()
				if err != nil {
					log.Printf("server stream closed: %v", err)
					return
				}
				cmd := msg.GetCommand()
				if cmd == nil {
					continue
				}
				fmt.Printf("Received command -> id: %s, type: %s\n", cmd.CommandId, cmd.Type)
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

				output, execErr := executeCommand(cfg, cmd.Type, string(cmd.Payload))
				status, message := "success", "command received and acknowledged"
				if execErr != nil {
					status, message = "failed", execErr.Error()
				} else if output != "" {
					message = strings.TrimSpace(output)
				}
				send(ctx, outbox, buildCommandResultMessage(cmd.CommandId, status, message))
			}
		}()

		<-ctx.Done() // sender or receiver failed; tear the connection down and retry
		conn.Close()
		time.Sleep(cfg.ReconnectBackoff)
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
	cfg := config.Load()

	logFile, err := logging.Init(cfg.LogLevel, cfg.LogFile)
	if err != nil {
		fmt.Println("failed to initialize logging:", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}

	fmt.Println("Aegis One Agent - starting up")
	fmt.Println("Server address:", cfg.ServerAddress)
	fmt.Printf("Heartbeat interval: %s (jitter: %s)\n", cfg.HeartbeatInterval, cfg.HeartbeatJitter)

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
		"patch_scan_interval", cfg.PatchScanInterval.String())

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
	inst := &patchInstaller{outbox: outbox, cache: cache, tmpDir: tmpDir, timeout: cfg.InstallTimeout}
	go inst.patchWorker(installJobs)

	// Cache janitor: periodic TTL/cap enforcement (infrequent, to stay light).
	go cacheJanitor(cache, cfg.CacheJanitorInterval)

	// Detection runs independently and only writes to the buffer; the sender
	// drains it and sends. This keeps the two off each other's stream.
	go runPatchDetection(cfg, buf)

	if err := runAgent(cfg, buf, outbox, installJobs); err != nil {
		log.Fatalf("agent failed: %v", err)
	}
}
