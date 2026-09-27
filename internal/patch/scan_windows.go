//go:build windows

package patch

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// Update is the enterprise-grade metadata for a single applicable update, built
// from the WUA IUpdate COM object. Detection reports ALL applicable updates with
// their Classification/Type so the server/console can apply patch policy — the
// agent does not silently drop drivers, upgrades, or definition updates.
type Update struct {
	UpdateID            string   // Identity.UpdateID (GUID) — true primary key
	RevisionNumber      int      // Identity.RevisionNumber — identifies the exact revision
	KBArticleIDs        []string // KB numbers (without "KB" prefix); can be several
	Title               string   // Title
	Description         string   // Description
	Classification      string   // UpdateClassification category, verbatim from WUA, e.g. "Security Updates", "Critical Updates", "Definition Updates", "Drivers", "Upgrades", "Update Rollups"
	Products            []string // Product categories, e.g. "Windows 11", "Microsoft Defender Antivirus"
	Type                string   // "Software" | "Driver" | "Unknown"
	MsrcSeverity        string   // "Critical" | "Important" | "Moderate" | "Low" | "" (often empty for cumulative updates — real severity comes via CVEs)
	CveIDs              []string // CVE identifiers, for customers/servers that track by CVE
	SecurityBulletinIDs []string // Legacy Microsoft security bulletin IDs (e.g. MS17-010)
	SupersededUpdateIDs []string // UpdateIDs this update supersedes (server uses for correlation/dedup)
	RebootRequired      bool     // InstallationBehavior.RebootBehavior != NeverReboots
	SizeMB              float64  // MaxDownloadSize
	SupportURL          string   // SupportUrl

	// NOTE: the agent only reports this raw metadata. Correlation across
	// CVE/KB/bulletin, CVSS scoring, and patch policy are the server's job.
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

// scanAttemptTimeout bounds a SINGLE scan attempt. Without it, a hung WUA
// Search call (which returns no error, just never returns) would block the very
// first attempt forever — the retry schedule and the Tier-2 service reset would
// never be reached. With it, a hang becomes a failed attempt, so the resilience
// tiers actually run: retry → WUA service reset → retry.
//
// It is deliberately generous (30m): a legitimate search — even on a fresh box
// with a large backlog of missing updates or over a slow WAN — completes well
// under this, while a genuinely hung call runs indefinitely. The timeout must
// stay ABOVE the worst-case healthy scan, because the Tier-2 remedy (clearing
// the SoftwareDistribution cache) is destructive: tripping it on a slow-but-
// working service would throw away the catalog it was building. Meant to become
// server-tunable policy (LAN vs slow-WAN, endpoint size).
var scanAttemptTimeout = 30 * time.Minute

// hangThreshold is how many CONSECUTIVE attempt timeouts make us treat the
// service as genuinely hung (vs a transient error). On reaching it, Tier 1
// stops early and escalates straight to the Tier-2 WUA service reset instead of
// burning the whole widening retry schedule — a hang is a stronger signal than a
// transient failure, and resetting the service is its actual remedy. With the
// 30m timeout, this means ~60m of confirmed no-return before the disruptive
// reset — conservative on purpose. Meant to become server-tunable policy.
var hangThreshold = 2

// errScanTimeout marks a scan attempt that exceeded scanAttemptTimeout (a hang),
// so the retry loop can distinguish it from an ordinary error.
var errScanTimeout = errors.New("scan attempt timed out")

// ScanWindowsUpdates checks for missing OS-level security/cumulative updates.
// Three-tier resilience, fully internal to the agent — never surfaced to
// the end user, never requires a manual reboot:
//   Tier 1: retry on a widening schedule, capped at 1 hour total
//   Tier 2: reset WUA service+cache (Microsoft's own documented fix)
//   Tier 3: short confirmation retry; if still failing, return a clear
//           error for the server/console to flag — never a silent "0 missing"
func ScanWindowsUpdates() ([]Update, error) {
	slog.Info("patch scan starting", "tier", 1, "strategy", "retry on widening schedule", "budget", "1h")
	updates, err := retryScan(tier1Schedule, 1*time.Hour)
	if err == nil {
		slog.Info("patch scan succeeded", "tier", 1, "missing", len(updates))
		return updates, nil
	}

	slog.Warn("patch scan Tier 1 failed, escalating to Tier 2 (WUA service reset)", "error", err.Error())
	resetErr := resetWUAService()
	if resetErr != nil {
		slog.Error("patch scan Tier 2 WUA reset failed", "scan_error", err.Error(), "reset_error", resetErr.Error())
		return nil, fmt.Errorf("scan failed (%v); WUA reset also failed (%v)", err, resetErr)
	}

	slog.Info("patch scan Tier 3 starting (confirmation retry after WUA reset)", "budget", "2m")
	updates, err2 := retryScan(tier3Schedule, 2*time.Minute)
	if err2 == nil {
		slog.Info("patch scan succeeded", "tier", 3, "missing", len(updates))
		return updates, nil
	}

	slog.Error("patch scan failed after all tiers — device patch status is UNKNOWN, not compliant", "error", err2.Error())
	return nil, fmt.Errorf("scan failed after retry and WUA service reset — needs investigation: %v", err2)
}

// retryScan tries scanOnce, sleeping for schedule[i] between attempts.
// Stops early if the next scheduled wait would exceed overallBudget —
// this is what makes "1 hour max" an actual cap, not just a suggestion.
func retryScan(schedule []time.Duration, overallBudget time.Duration) ([]Update, error) {
	deadline := time.Now().Add(overallBudget)
	var lastErr error
	consecutiveTimeouts := 0

	for attempt := 0; ; attempt++ {
		slog.Info("scan attempt starting", "attempt", attempt+1, "attempt_timeout", scanAttemptTimeout.String())
		updates, err := scanOnceWithTimeout(scanAttemptTimeout)
		if err == nil {
			slog.Info("scan attempt succeeded", "attempt", attempt+1, "missing", len(updates))
			return updates, nil
		}
		lastErr = err

		if errors.Is(err, errScanTimeout) {
			consecutiveTimeouts++
			slog.Warn("scan attempt hung (timed out)", "attempt", attempt+1, "consecutive_timeouts", consecutiveTimeouts, "hang_threshold", hangThreshold)
			if consecutiveTimeouts >= hangThreshold {
				// Genuinely hung — stop retrying and let the caller escalate to
				// the Tier-2 WUA service reset now instead of waiting out the
				// rest of the schedule.
				slog.Warn("scan appears hung — escalating early to service reset", "consecutive_timeouts", consecutiveTimeouts)
				return nil, fmt.Errorf("scan hung after %d consecutive timeouts: %w", consecutiveTimeouts, lastErr)
			}
		} else {
			consecutiveTimeouts = 0
			slog.Warn("scan attempt failed", "attempt", attempt+1, "error", err.Error())
		}

		if attempt >= len(schedule) {
			slog.Warn("scan retry schedule exhausted", "attempts", attempt+1)
			break // schedule exhausted, stop regardless of remaining budget
		}
		wait := schedule[attempt]
		if time.Now().Add(wait).After(deadline) {
			slog.Warn("next retry would exceed time budget; stopping", "next_wait", wait.String())
			break // this wait would exceed the cap — stop here
		}
		slog.Info("waiting before next scan attempt", "wait", wait.String())
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

	slog.Info("WUA reset: stopping wuauserv service")
	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("could not stop wuauserv: %w", err)
	}
	if err := waitForState(s, svc.Stopped, 30*time.Second); err != nil {
		return err
	}
	slog.Info("WUA reset: wuauserv stopped")

	sysRoot := os.Getenv("SystemRoot")
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	cacheDir := filepath.Join(sysRoot, "SoftwareDistribution")
	backupDir := fmt.Sprintf("%s.bak-%d", cacheDir, time.Now().Unix())
	if _, err := os.Stat(cacheDir); err == nil {
		slog.Info("WUA reset: clearing SoftwareDistribution cache", "dir", cacheDir, "backup", backupDir)
		if err := os.Rename(cacheDir, backupDir); err != nil {
			slog.Warn("WUA reset: could not clear SoftwareDistribution cache", "error", err.Error())
		}
	}

	slog.Info("WUA reset: starting wuauserv service")
	if err := s.Start(); err != nil {
		return fmt.Errorf("could not start wuauserv: %w", err)
	}
	if err := waitForState(s, svc.Running, 30*time.Second); err != nil {
		return err
	}
	slog.Info("WUA reset: complete, wuauserv running")
	return nil
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

// scanOnceWithTimeout runs scanOnce but abandons it if it does not return within
// timeout. scanOnce runs on its own goroutine (with its own locked OS thread and
// COM apartment), so a hung WUA Search does not block the caller — the caller
// treats the timeout as a failed attempt and moves on to the next tier. The hung
// goroutine remains parked on the COM call until it eventually returns (often
// once the Tier-2 service reset unblocks it) or the process exits.
func scanOnceWithTimeout(timeout time.Duration) ([]Update, error) {
	type scanResult struct {
		updates []Update
		err     error
	}
	ch := make(chan scanResult, 1) // buffered so a late finish doesn't leak the goroutine on send

	go func() {
		updates, err := scanOnce()
		ch <- scanResult{updates, err}
	}()

	select {
	case r := <-ch:
		return r.updates, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("%w after %s (WUA Search unresponsive/hung)", errScanTimeout, timeout)
	}
}

func scanOnce() ([]Update, error) {
	cleanup, err := initCOM()
	if err != nil {
		return nil, err
	}
	defer cleanup()

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

	slog.Info("querying Windows Update catalog (first run can take several minutes)")
	stop := startHeartbeat("WUA catalog search")
	resultRaw, err := oleutil.CallMethod(searcher, "Search", "IsInstalled=0 and IsHidden=0")
	stop()
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
		// Item is an indexed property (propget), so it must be read with
		// GetProperty; CallMethod (DISPATCH_METHOD) throws on a propget-only
		// member, which would silently skip every update and report a false
		// "0 missing" on a machine that actually has pending updates.
		itemRaw, err := oleutil.GetProperty(updates, "Item", i)
		if err != nil {
			// Count said this item exists but we can't read it — surface it
			// loudly rather than under-reporting silently.
			slog.Warn("scan: could not read update item, skipping", "index", i, "of", count, "error", err.Error())
			continue
		}
		item := itemRaw.ToIDispatch()
		out = append(out, buildUpdate(item))
		item.Release()
	}

	return out, nil
}

// buildUpdate extracts the full metadata set from a WUA IUpdate COM object.
func buildUpdate(item *ole.IDispatch) Update {
	u := Update{
		Title:               stringProp(item, "Title"),
		Description:         stringProp(item, "Description"),
		MsrcSeverity:        stringProp(item, "MsrcSeverity"),
		SupportURL:          stringProp(item, "SupportUrl"),
		KBArticleIDs:        stringCollection(item, "KBArticleIDs"),
		CveIDs:              stringCollection(item, "CveIDs"),
		SecurityBulletinIDs: stringCollection(item, "SecurityBulletinIDs"),
		SupersededUpdateIDs: stringCollection(item, "SupersededUpdateIDs"),
	}

	if v, err := oleutil.GetProperty(item, "Type"); err == nil {
		switch int(v.Val) {
		case 1:
			u.Type = "Software"
		case 2:
			u.Type = "Driver"
		default:
			u.Type = "Unknown"
		}
	}

	if idRaw, err := oleutil.GetProperty(item, "Identity"); err == nil {
		id := idRaw.ToIDispatch()
		u.UpdateID = stringProp(id, "UpdateID")
		if rev, err := oleutil.GetProperty(id, "RevisionNumber"); err == nil {
			u.RevisionNumber = int(rev.Val)
		}
		id.Release()
	}

	// MaxDownloadSize is a DECIMAL VARIANT; decode it directly. Fall back to the
	// common numeric variant types, and debug-log anything still unrecognized.
	if sizeRaw, err := oleutil.GetProperty(item, "MaxDownloadSize"); err == nil {
		if bytes, ok := decimalVariantToFloat(sizeRaw); ok {
			u.SizeMB = bytes / 1024 / 1024
		} else {
			switch v := sizeRaw.Value().(type) {
			case float64:
				u.SizeMB = v / 1024 / 1024
			case int64:
				u.SizeMB = float64(v) / 1024 / 1024
			case uint64:
				u.SizeMB = float64(v) / 1024 / 1024
			default:
				slog.Debug("scan: unhandled MaxDownloadSize variant type",
					"go_type", fmt.Sprintf("%T", v), "vt", sizeRaw.VT)
			}
		}
	}

	if biRaw, err := oleutil.GetProperty(item, "InstallationBehavior"); err == nil {
		bi := biRaw.ToIDispatch()
		if rb, err := oleutil.GetProperty(bi, "RebootBehavior"); err == nil {
			u.RebootRequired = int(rb.Val) != 0 // 0 = NeverReboots
		}
		bi.Release()
	}

	u.Classification, u.Products = categories(item)
	return u
}

// stringProp reads a string property, returning "" on any error.
func stringProp(disp *ole.IDispatch, name string) string {
	if v, err := oleutil.GetProperty(disp, name); err == nil {
		return v.ToString()
	}
	return ""
}

// stringCollection reads an IStringCollection property (e.g. KBArticleIDs) into
// a Go slice. Elements are read via GetProperty because the collection's Item is
// an indexed property, not a method.
func stringCollection(item *ole.IDispatch, name string) []string {
	raw, err := oleutil.GetProperty(item, name)
	if err != nil {
		return nil
	}
	coll := raw.ToIDispatch()
	defer coll.Release()

	countRaw, err := oleutil.GetProperty(coll, "Count")
	if err != nil {
		return nil
	}
	n := int(countRaw.Val)
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		if v, err := oleutil.GetProperty(coll, "Item", i); err == nil {
			out = append(out, v.ToString())
		}
	}
	return out
}

// categories walks the update's Categories collection and returns its
// UpdateClassification (Security Updates / Drivers / Upgrades / ...) and the
// list of Product categories it belongs to.
func categories(item *ole.IDispatch) (classification string, products []string) {
	raw, err := oleutil.GetProperty(item, "Categories")
	if err != nil {
		return "", nil
	}
	cats := raw.ToIDispatch()
	defer cats.Release()

	countRaw, err := oleutil.GetProperty(cats, "Count")
	if err != nil {
		return "", nil
	}
	n := int(countRaw.Val)
	for i := 0; i < n; i++ {
		catRaw, err := oleutil.GetProperty(cats, "Item", i)
		if err != nil {
			continue
		}
		cat := catRaw.ToIDispatch()
		name := stringProp(cat, "Name")
		switch stringProp(cat, "Type") {
		case "UpdateClassification":
			if classification == "" {
				classification = name
			}
		case "Product":
			products = append(products, name)
		}
		cat.Release()
	}
	return classification, products
}
// ResetForTesting exposes resetWUAService for manual verification only.
// Not called by production code paths directly — ScanWindowsUpdates
// calls the internal resetWUAService itself when Tier 1 genuinely fails.
func ResetForTesting() error {
	return resetWUAService()
}