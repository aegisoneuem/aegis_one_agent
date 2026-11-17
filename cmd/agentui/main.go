// Command agentui is the Aegis One tray UI (AegisOneClientUI.exe) — a small, separately
// signed companion to the service (AegisOneClient.exe). The service launches it into
// each logged-on user's desktop; it shows the tray icon and renders user
// notifications. All UI logic lives in internal/trayui so this binary stays tiny.
package main

import "github.com/aegisoneuem/aegis_one_agent/internal/trayui"

func main() {
	trayui.Run()
}
