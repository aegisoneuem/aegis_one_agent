//go:build windows

package patch

import (
	"context"
	"log/slog"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	modadvapi32               = windows.NewLazySystemDLL("advapi32.dll")
	procRegNotifyChangeKeyVal = modadvapi32.NewProc("RegNotifyChangeKeyValue")
)

const (
	// REG_NOTIFY_CHANGE_NAME (subkey added/removed) | REG_NOTIFY_CHANGE_LAST_SET
	// (a value changed, e.g. a package's CurrentState) — together they catch any
	// servicing operation on any package (install/uninstall, incl. .NET).
	regNotifyFilter = 0x00000001 | 0x00000004
)

// WatchServicing blocks (run it in a goroutine) watching the CBS Packages registry
// subtree and does a non-blocking send on `fire` whenever anything changes — a
// patch installed, uninstalled, staged or state-changed. Event-driven via
// RegNotifyChangeKeyValue (the kernel notifies; no polling, ~zero CPU), so the
// agent can react to a change WITHOUT running the heavy cab scan on a timer.
//
// It returns when ctx is cancelled (checked on a short wait timeout). The caller
// is expected to debounce the fires before triggering a scan (a single install
// touches many package keys).
func WatchServicing(ctx context.Context, fire chan<- struct{}) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE, cbsPackagesKey, registry.NOTIFY)
	if err != nil {
		slog.Warn("servicing watch: cannot open CBS Packages for notify (auto re-scan on change disabled)", "error", err.Error())
		return
	}
	defer key.Close()

	event, err := windows.CreateEvent(nil, 1 /*manual-reset*/, 0 /*nonsignaled*/, nil)
	if err != nil {
		slog.Warn("servicing watch: cannot create event", "error", err.Error())
		return
	}
	defer windows.CloseHandle(event)

	slog.Info("servicing watch: monitoring CBS for install/uninstall changes (event-driven)")
	for {
		if ctx.Err() != nil {
			return
		}
		// Arm a one-shot async notification: the event signals on the next change.
		r, _, callErr := procRegNotifyChangeKeyVal.Call(
			uintptr(key), 1 /*watchSubtree*/, uintptr(regNotifyFilter), uintptr(event), 1 /*asynchronous*/)
		if r != 0 {
			slog.Warn("servicing watch: RegNotifyChangeKeyValue failed, retrying", "rc", r, "err", callErr.Error())
			if sleepCtx(ctx, 30*time.Second) {
				return
			}
			continue
		}
		// Wait for the change, waking every few seconds to honour ctx cancellation.
		for {
			s, _ := windows.WaitForSingleObject(event, 5000)
			if s == uint32(windows.WAIT_OBJECT_0) {
				windows.ResetEvent(event)
				select {
				case fire <- struct{}{}:
				default: // a fire is already pending; one is enough
				}
				break // re-arm (notifications are one-shot)
			}
			// WAIT_TIMEOUT (or abandoned) — re-check ctx, keep waiting.
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// sleepCtx sleeps for d or until ctx is done; returns true if ctx was cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return true
	case <-t.C:
		return false
	}
}
