//go:build windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"sync"

	"github.com/Microsoft/go-winio"

	"github.com/aegisoneuem/aegis_one_agent/internal/uimsg"
)

// UI notification pipe (service side). The service hosts an ACL'd named pipe; each
// tray UI (one per logged-on session) connects. The service BROADCASTS notifications
// to every connected UI and reads back user responses. Service-only.

var (
	uiConnsMu sync.Mutex
	uiConns   = map[net.Conn]*bufio.Writer{}
)

func uiPipePath() string { return `\\.\pipe\` + uimsg.PipeName }

// runUINotifyServer hosts the UI pipe and accepts tray-UI connections. The DACL grants
// SYSTEM + Administrators full access and interactive users read/write, so the
// user-session UI (medium integrity) can connect while others cannot.
func runUINotifyServer(ctx context.Context) {
	defer recoverLog("ui-notify-server")
	if !runningAsService {
		return
	}
	pc := &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;SY)(A;;GA;;;BA)(A;;GRGW;;;IU)"}
	l, err := winio.ListenPipe(uiPipePath(), pc)
	if err != nil {
		slog.Debug("ui-notify: listen failed", "error", err.Error())
		return
	}
	go func() { <-ctx.Done(); _ = l.Close() }()
	for {
		conn, err := l.Accept()
		if err != nil {
			return // listener closed (ctx done)
		}
		uiConnsMu.Lock()
		uiConns[conn] = bufio.NewWriter(conn)
		uiConnsMu.Unlock()
		slog.Debug("ui-notify: tray UI connected")
		go readUIResponses(ctx, conn)
	}
}

func unregisterUIConn(c net.Conn) {
	uiConnsMu.Lock()
	delete(uiConns, c)
	uiConnsMu.Unlock()
	_ = c.Close()
}

// deliverUIMessage broadcasts a UI message (notify/message/action) to every connected
// tray UI and returns how many it reached (0 = none connected).
func deliverUIMessage(m uimsg.Message) int { return broadcastUI(m) }

// broadcastUI writes m to every connected UI and returns the number reached.
func broadcastUI(m uimsg.Message) int {
	b, err := json.Marshal(m)
	if err != nil {
		return 0
	}
	b = append(b, '\n')
	uiConnsMu.Lock()
	defer uiConnsMu.Unlock()
	n := 0
	for _, w := range uiConns {
		if _, err := w.Write(b); err == nil {
			_ = w.Flush()
			n++
		}
	}
	slog.Debug("ui-notify: broadcast", "kind", m.Kind, "reached", n)
	return n
}

// readUIResponses reads user responses (Step 3) until the UI disconnects.
func readUIResponses(_ context.Context, c net.Conn) {
	defer unregisterUIConn(c)
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		var m uimsg.Message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Kind == uimsg.KindResponse {
			slog.Info("ui-notify: user response", "id", m.ID, "action", m.Action)
			reportUserResponse(m.ID, m.Action, m.SnoozeSecs)
		}
	}
}
