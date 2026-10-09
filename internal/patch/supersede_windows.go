//go:build windows

package patch

import "strings"

// dropSuperseded removes updates that are superseded by another update present in
// the SAME result set, and returns the filtered slice plus the number dropped.
//
// Why this exists: an offline wsusscn2.cab scan (unlike live Windows Update)
// returns superseded updates as applicable. So an old cumulative update and the
// newer one that replaces it both show up as "missing" — inflating both the
// reported count and the summed MaxDownloadSize with redundant, non-actionable
// entries (this is the source of the misleadingly large multi-GB totals).
//
// An update is dropped ONLY when its replacement is also in the set, so nothing
// actionable is ever hidden: if a superseder is installed it is not in the
// IsInstalled=0 results, so its (now also unneeded) predecessor is left in place
// rather than silently removed. The surviving superseder still carries
// SupersededUpdateIDs, so the server keeps full correlation/dedup information.
//
// UpdateIDs are GUIDs; WUA is not consistent about their case between an update's
// Identity.UpdateID and another update's SupersededUpdateIDs, so match case-folded.
func dropSuperseded(updates []Update) ([]Update, int) {
	superseded := make(map[string]struct{})
	for _, u := range updates {
		for _, id := range u.SupersededUpdateIDs {
			if id = strings.TrimSpace(id); id != "" {
				superseded[strings.ToLower(id)] = struct{}{}
			}
		}
	}
	if len(superseded) == 0 {
		return updates, 0
	}

	out := make([]Update, 0, len(updates))
	dropped := 0
	for _, u := range updates {
		if _, isSuperseded := superseded[strings.ToLower(strings.TrimSpace(u.UpdateID))]; isSuperseded {
			dropped++
			continue
		}
		out = append(out, u)
	}
	return out, dropped
}
