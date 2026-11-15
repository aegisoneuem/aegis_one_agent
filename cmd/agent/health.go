package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// healthState is the agent's live status, updated by the connect loop and read by
// the local health endpoint.
type healthState struct {
	mu            sync.Mutex
	start         time.Time
	version       string
	deviceID      string
	hostname      string
	osName        string
	enrolled      bool
	connected     bool
	lastHeartbeat time.Time
}

var agentHealth = &healthState{start: time.Now()}

func (h *healthState) update(fn func(*healthState)) {
	h.mu.Lock()
	fn(h)
	h.mu.Unlock()
}

func (h *healthState) snapshot() map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := "disconnected"
	if h.connected {
		status = "connected"
	}
	lastHB := ""
	if !h.lastHeartbeat.IsZero() {
		lastHB = h.lastHeartbeat.UTC().Format(time.RFC3339)
	}
	return map[string]any{
		"status":         status,
		"version":        h.version,
		"device_id":      h.deviceID,
		"hostname":       h.hostname,
		"os":             h.osName,
		"enrolled":       h.enrolled,
		"connected":      h.connected,
		"last_heartbeat": lastHB,
		"uptime_seconds": int(time.Since(h.start).Seconds()),
	}
}

// startHealthServer runs a LOCALHOST-ONLY HTTP server for liveness/status checks
// (no remote access, no auth needed). GET /health → 200; GET /status → JSON.
func startHealthServer(ctx context.Context, addr, deviceID string) {
	if strings.TrimSpace(addr) == "" {
		return // disabled
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(agentHealth.snapshot())
	})
	// Token-gated local stop: the ONLY local (non-server) stop path when the service
	// is self-protected. The endpoint is localhost-only, but the AUTHORIZATION is the
	// server-signed unprotect token in the body — any local user can reach it but
	// cannot mint a valid token. POST the token; valid → agent shuts down.
	mux.HandleFunc("/control/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
		secret := strings.TrimSpace(string(body))
		if ok, reason := authorizeUnprotect(secret, deviceID); !ok {
			slog.Warn("tamper-protect: LOCAL stop refused", "reason", reason)
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("refused: " + reason + "\n"))
			return
		}
		slog.Warn("tamper-protect: LOCAL stop AUTHORIZED via control endpoint — shutting down")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("stop authorized — agent shutting down\n"))
		go func() {
			time.Sleep(300 * time.Millisecond)
			requestAgentStop()
		}()
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	go func() {
		defer recoverLog("health-server")
		slog.Info("health endpoint listening (localhost only)", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Warn("health server stopped", "error", err.Error())
		}
	}()
}
