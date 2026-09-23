//go:build windows

package patch

import (
	"fmt"
	"time"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

type Update struct {
	Title    string
	KB       string
	Severity string
	SizeMB   float64
}

// ScanWindowsUpdates checks for missing OS-level security/cumulative updates.
// Retries transient failures (e.g. WUA service busy with other work) instead
// of requiring a reboot — matching how mature UEM tools behave in practice.
// ScanWindowsUpdates checks for missing OS-level security/cumulative updates.
// Retries transient failures (e.g. WUA service busy) for up to a total time
// budget — not a fixed retry count — because the real question is "how long
// is acceptable to wait," not "how many attempts."
func ScanWindowsUpdates() ([]Update, error) {
	const timeBudget = 2 * time.Minute
	deadline := time.Now().Add(timeBudget)

	var lastErr error
	attempt := 0

	for time.Now().Before(deadline) {
		attempt++
		updates, err := scanOnce()
		if err == nil {
			return updates, nil
		}
		lastErr = err

		backoff := time.Duration(attempt*attempt) * 2 * time.Second
		if time.Now().Add(backoff).After(deadline) {
			break // no point sleeping past the deadline
		}
		time.Sleep(backoff)
	}

	return nil, fmt.Errorf("scan failed after %d attempts over %s: %w",
		attempt, timeBudget, lastErr)
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