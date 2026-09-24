//go:build windows

package patch

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

type Update struct {
	Title    string
	KB       string
	Severity string
	SizeMB   float64
}

// Tier-1 schedule: fast retries first (catch momentary glitches),
// then widening minute-scale gaps (avoid hammering a genuinely busy
// service). The overall 1-hour deadline in ScanWindowsUpdates caps
// this automatically — a later gap that would exceed the deadline
// is simply not used.
var tier1Schedule = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	5 * time.Minute,
	10 * time.Minute,
	15 * time.Minute,
	20 * time.Minute,
	30 * time.Minute,
}

// Tier-3 (post-reset) schedule: short and fast — the service was just
// reset, so a quick confirmation retry is enough; no need to wait long.
var tier3Schedule = []time.Duration{
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
}

// ScanWindowsUpdates checks for missing OS-level security/cumulative updates.
// Three-tier resilience, fully internal to the agent — never surfaced to
// the end user, never requires a manual reboot:
//   Tier 1: retry on a widening schedule, capped at 1 hour total
//   Tier 2: reset WUA service+cache (Microsoft's own documented fix)
//   Tier 3: short confirmation retry; if still failing, return a clear
//           error for the server/console to flag — never a silent "0 missing"
func ScanWindowsUpdates() ([]Update, error) {
	updates, err := retryScan(tier1Schedule, 1*time.Hour)
	if err == nil {
		return updates, nil
	}

	resetErr := resetWUAService()
	if resetErr != nil {
		return nil, fmt.Errorf("scan failed (%v); WUA reset also failed (%v)", err, resetErr)
	}

	updates, err2 := retryScan(tier3Schedule, 2*time.Minute)
	if err2 == nil {
		return updates, nil
	}

	return nil, fmt.Errorf("scan failed after retry and WUA service reset — needs investigation: %v", err2)
}

// retryScan tries scanOnce, sleeping for schedule[i] between attempts.
// Stops early if the next scheduled wait would exceed overallBudget —
// this is what makes "1 hour max" an actual cap, not just a suggestion.
func retryScan(schedule []time.Duration, overallBudget time.Duration) ([]Update, error) {
	deadline := time.Now().Add(overallBudget)
	var lastErr error

	for attempt := 0; ; attempt++ {
		updates, err := scanOnce()
		if err == nil {
			return updates, nil
		}
		lastErr = err

		if attempt >= len(schedule) {
			break // schedule exhausted, stop regardless of remaining budget
		}
		wait := schedule[attempt]
		if time.Now().Add(wait).After(deadline) {
			break // this wait would exceed the cap — stop here
		}
		time.Sleep(wait)
	}
	return nil, fmt.Errorf("failed after retries: %w", lastErr)
}

// resetWUAService performs Microsoft's own documented "reset Windows Update
// Agent" fix via the native Service Control Manager API (no external
// executables — consistent with avoiding PowerShell/net.exe dependencies
// that locked-down environments may block).
func resetWUAService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %w", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService("wuauserv")
	if err != nil {
		return fmt.Errorf("could not open wuauserv: %w", err)
	}
	defer s.Close()

	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("could not stop wuauserv: %w", err)
	}
	if err := waitForState(s, svc.Stopped, 30*time.Second); err != nil {
		return err
	}

	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	cacheDir := filepath.Join(sysRoot, "SoftwareDistribution")
	backupDir := fmt.Sprintf("%s.bak-%d", cacheDir, time.Now().Unix())
	if _, err := os.Stat(cacheDir); err == nil {
		if err := os.Rename(cacheDir, backupDir); err != nil {
			fmt.Println("warning: could not clear SoftwareDistribution cache:", err)
		}
	}

	if err := s.Start(); err != nil {
		return fmt.Errorf("could not start wuauserv: %w", err)
	}
	return waitForState(s, svc.Running, 30*time.Second)
}

func waitForState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		status, err := s.Query()
		if err != nil {
			return err
		}
		if status.State == want {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for wuauserv to reach state %v", want)
}

func scanOnce() ([]Update, error) {
	if err := ole.CoInitialize(0); err != nil {
		return nil, fmt.Errorf("CoInitialize failed: %v", err)
	}
	defer ole.CoUninitialize()

	unknown, err := oleutil.CreateObject("Microsoft.Update.Session")
	if err != nil {
		return nil, fmt.Errorf("failed to create Update.Session (is WUA available?): %v", err)
	}
	defer unknown.Release()

	session, err := unknown.QueryInterface(ole.IID_IDispatch)
	if err != nil {
		return nil, err
	}
	defer session.Release()

	searcherRaw, err := oleutil.CallMethod(session, "CreateUpdateSearcher")
	if err != nil {
		return nil, err
	}
	searcher := searcherRaw.ToIDispatch()
	defer searcher.Release()

	resultRaw, err := oleutil.CallMethod(searcher, "Search", "IsInstalled=0 and IsHidden=0")
	if err != nil {
		if oleErr, ok := err.(*ole.OleError); ok {
			return nil, fmt.Errorf("search failed: code=0x%X, description=%s",
				oleErr.Code(), oleErr.Description())
		}
		return nil, fmt.Errorf("search failed: %v", err)
	}
	result := resultRaw.ToIDispatch()
	defer result.Release()

	updatesRaw, err := oleutil.GetProperty(result, "Updates")
	if err != nil {
		return nil, err
	}
	updates := updatesRaw.ToIDispatch()
	defer updates.Release()

	countRaw, err := oleutil.GetProperty(updates, "Count")
	if err != nil {
		return nil, err
	}
	count := int(countRaw.Val)

	var out []Update
	for i := 0; i < count; i++ {
		itemRaw, err := oleutil.CallMethod(updates, "Item", i)
		if err != nil {
			continue
		}
		item := itemRaw.ToIDispatch()

		title, _ := oleutil.GetProperty(item, "Title")
		severity, _ := oleutil.GetProperty(item, "MsrcSeverity")
		sizeRaw, _ := oleutil.GetProperty(item, "MaxDownloadSize")

		kb := ""
		if kbRaw, err := oleutil.GetProperty(item, "KBArticleIDs"); err == nil {
			kbColl := kbRaw.ToIDispatch()
			if kbCountRaw, err := oleutil.GetProperty(kbColl, "Count"); err == nil && int(kbCountRaw.Val) > 0 {
				if kbItem, err := oleutil.CallMethod(kbColl, "Item", 0); err == nil {
					kb = kbItem.ToString()
				}
			}
			kbColl.Release()
		}

		sizeMB := 0.0
		if v := sizeRaw.Value(); v != nil {
			if f, ok := v.(float64); ok {
				sizeMB = f / 1024 / 1024
			}
		}

		out = append(out, Update{
			Title:    title.ToString(),
			KB:       kb,
			Severity: severity.ToString(),
			SizeMB:   sizeMB,
		})
		item.Release()
	}

	return out, nil
}
// ResetForTesting exposes resetWUAService for manual verification only.
// Not called by production code paths directly — ScanWindowsUpdates
// calls the internal resetWUAService itself when Tier 1 genuinely fails.
func ResetForTesting() error {
	return resetWUAService()
}