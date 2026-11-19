package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"runtime"
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
func startHealthServer(ctx context.Context, addr, deviceID string, pprofOn bool) {
	if strings.TrimSpace(addr) == "" {
		return // disabled
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	// Runtime diagnostics (localhost-only; not sensitive) — goroutine count + heap, so
	// a slow leak is visible with a single GET /diag over time.
	mux.HandleFunc("/diag", func(w http.ResponseWriter, _ *http.Request) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]any{
			"num_goroutine":    runtime.NumGoroutine(),
			"heap_alloc_bytes": m.HeapAlloc,
			"heap_sys_bytes":   m.HeapSys,
			"heap_objects":     m.HeapObjects,
			"sys_bytes":        m.Sys,
			"num_gc":           m.NumGC,
		})
	})
	// Full pprof (goroutine/heap/profile) for a detailed leak hunt — opt-in via
	// AEGIS_PPROF=1, still localhost-only.
	if pprofOn {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		slog.Info("pprof diagnostics ENABLED on the health endpoint (localhost)", "addr", addr)
	}
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

// runDiagLogger logs the goroutine count + heap every 10 minutes, so a slow
// per-reconnect leak shows up as a steady climb in the normal agent log (no tooling
// needed). Cheap: ReadMemStats once per tick.
func runDiagLogger(ctx context.Context) {
	defer recoverLog("diag-logger")
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			slog.Info("diag: runtime",
				"goroutines", runtime.NumGoroutine(),
				"heap_alloc_mb", m.HeapAlloc/1024/1024,
				"heap_sys_mb", m.HeapSys/1024/1024,
				"heap_objects", m.HeapObjects,
				"num_gc", m.NumGC)
		}
	}
}
