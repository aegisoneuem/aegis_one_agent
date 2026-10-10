//go:build windows

package patch

import "strings"

// markSuperseded tags (does NOT remove) each update that is superseded by another
// update present in the SAME result set, setting its Superseded flag, and returns
// how many were tagged.
//
// Why tag, not drop: an offline wsusscn2.cab scan (unlike live Windows Update)
// returns superseded updates as applicable, so an old cumulative update and the
// newer one that replaces it both show up. The UEM console needs to SEE the
// superseded ones as their own category ("no longer required"), so the agent
// reports everything and flags it — categorization is the server/console's job.
// Consumers that want only the actionable set filter on !Superseded (which also
// gives an accurate missing count and download-size total).
//
// An update is flagged ONLY when its replacement is also in the set: if a
// superseder is installed it is absent from the IsInstalled=0 results, so its
// (now also unneeded) predecessor is left untagged rather than wrongly flagged.
//
// UpdateIDs are GUIDs; WUA is not consistent about their case between an update's
// Identity.UpdateID and another update's SupersededUpdateIDs, so match case-folded.
func markSuperseded(updates []Update) int {
	superseded := make(map[string]struct{})
	for _, u := range updates {
		for _, id := range u.SupersededUpdateIDs {
			if id = strings.TrimSpace(id); id != "" {
				superseded[strings.ToLower(id)] = struct{}{}
			}
		}
	}
	if len(superseded) == 0 {
		return 0
	}

	tagged := 0
	for i := range updates {
		if _, isSuperseded := superseded[strings.ToLower(strings.TrimSpace(updates[i].UpdateID))]; isSuperseded {
			updates[i].Superseded = true
			tagged++
		}
	}
	return tagged
}
