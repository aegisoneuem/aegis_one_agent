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
	pb "github.com/aegisoneuem/aegis_one_proto/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
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

func runAgent(cfg config.Config, buf buffer.Buffer) error {
	for {
		ctx, cancel := context.WithCancel(context.Background())
		conn, err := grpc.NewClient(cfg.ServerAddress, grpc.WithTransportCredentials(insecure.NewCredentials()))
		if err != nil {
			log.Printf("failed to connect to %s: %v", cfg.ServerAddress, err)
			time.Sleep(cfg.ReconnectBackoff)
			continue
		}

		client := pb.NewAgentControlPlaneClient(conn)
		stream, err := client.Connect(ctx)
		if err != nil {
			log.Printf("failed to open stream: %v", err)
			conn.Close()
			time.Sleep(cfg.ReconnectBackoff)
			continue
		}

		// On (re)connect, flush any patch scan results collected while offline.
		drainPatchResults(stream, buf)

		recvDone := make(chan error, 1)
		go func() {
			for {
				msg, err := stream.Recv()
				if err != nil {
					recvDone <- err
					return
				}
				if cmd := msg.GetCommand(); cmd != nil {
					fmt.Printf("Received command -> id: %s, type: %s\n", cmd.CommandId, cmd.Type)
					if !verifyCommand(cmd.Payload, cmd.Signature) {
						fmt.Printf("Command %s SIGNATURE INVALID — rejecting!\n", cmd.CommandId)
						result := buildCommandResultMessage(cmd.CommandId, "rejected", "signature verification failed")
						stream.Send(result)
						continue
					}
					output, err := executeCommand(cfg, cmd.Type, string(cmd.Payload))
					status := "success"
					message := "command received and acknowledged"
					if err != nil {
						status = "failed"
						message = err.Error()
					} else if output != "" {
						message = strings.TrimSpace(output)
					}
					result := buildCommandResultMessage(cmd.CommandId, status, message)
					if err := stream.Send(result); err != nil {
						log.Printf("failed to send command result: %v", err)
						return
					}
					fmt.Printf("Sent command result -> command_id: %s, status: %s, message: %s\n", cmd.CommandId, status, message)
				}
			}
		}()

		interval := cfg.HeartbeatInterval
		if cfg.HeartbeatJitter > 0 {
			interval += time.Duration(rand.Int63n(int64(cfg.HeartbeatJitter)))
		}
		ticker := time.NewTicker(interval)
		connected := true

		for connected {
			select {
			case err := <-recvDone:
				log.Printf("server stream closed: %v", err)
				connected = false
			case <-ticker.C:
				if err := sendHeartbeat(stream, cfg); err != nil {
					log.Printf("failed to send heartbeat: %v", err)
					connected = false
					break
				}
				// Send any patch scan results the detection goroutine buffered.
				drainPatchResults(stream, buf)
			}
		}

		ticker.Stop()
		cancel()
		conn.Close()
		time.Sleep(cfg.ReconnectBackoff)
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

	bufPath := filepath.Join(os.TempDir(), "aegis-patch-buffer.jsonl")
	buf, err := buffer.NewFileBuffer(bufPath)
	if err != nil {
		log.Fatalf("failed to create offline buffer: %v", err)
	}
	slog.Info("agent starting",
		"buffer", bufPath,
		"cab", cfg.WsusScanCab,
		"patch_scan_interval", cfg.PatchScanInterval.String())

	// Detection runs independently and only writes to the buffer; the connect
	// loop drains it and sends. This keeps the two off each other's stream.
	go runPatchDetection(cfg, buf)

	if err := runAgent(cfg, buf); err != nil {
		log.Fatalf("agent failed: %v", err)
	}
}
