//go:build !windows

package trayui

// ServiceAliveEventName is Windows-only; defined here so cross-platform builds compile.
const ServiceAliveEventName = ""

// Run is a no-op on non-Windows (the tray UI is Windows-specific). Linux/macOS user
// messaging will use their own mechanisms in a later port.
func Run() {}
