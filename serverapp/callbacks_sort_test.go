package serverapp

import (
	"testing"

	"xbot/channel/web"
)

// RFC3339 timestamps carry mixed timezone offsets across sources: CLI-local
// session files store local-time offsets (+08:00) while server-tenant rows are
// rendered in UTC (Z). Lexicographic string comparison is wrong across offsets:
//
//	"2026-08-29T12:00:00+08:00" > "2026-08-29T10:00:00Z"   (lexicographic)
//	 2026-08-29T12:00:00+08:00  = 04:00Z < 10:00Z           (chronological)
//
// sortUserChats / sortSessionTreeMains must compare parsed time.Time values.

func TestSortUserChats_MixedTimezoneRFC3339(t *testing.T) {
	rows := []web.UserChatWithPreview{
		{ChatID: "cli-local", LastActive: "2026-08-29T12:00:00+08:00"}, // 04:00Z (earlier)
		{ChatID: "server-utc", LastActive: "2026-08-29T10:00:00Z"},     // 10:00Z (later)
	}
	sortUserChats(rows)
	if rows[0].ChatID != "server-utc" {
		t.Errorf("sortUserChats: mixed-offset RFC3339 must compare chronologically — want server-utc (10:00Z, later) first, got %s", rows[0].ChatID)
	}
}

func TestSortUserChats_SameOffsetStillSorted(t *testing.T) {
	rows := []web.UserChatWithPreview{
		{ChatID: "old", LastActive: "2026-08-28T10:00:00Z"},
		{ChatID: "new", LastActive: "2026-08-29T10:00:00Z"},
	}
	sortUserChats(rows)
	if rows[0].ChatID != "new" {
		t.Errorf("sortUserChats: same-offset rows must stay last-active desc — want new first, got %s", rows[0].ChatID)
	}
}

func TestSortSessionTreeMains_MixedTimezoneLastActive(t *testing.T) {
	rows := []web.UserChatWithPreview{
		{ChatID: "cli-local", LastActive: "2026-08-29T12:00:00+08:00"}, // 04:00Z (earlier)
		{ChatID: "server-utc", LastActive: "2026-08-29T10:00:00Z"},     // 10:00Z (later)
	}
	sortSessionTreeMains(rows)
	if rows[0].ChatID != "server-utc" {
		t.Errorf("sortSessionTreeMains: mixed-offset LastActive must compare chronologically — want server-utc first, got %s", rows[0].ChatID)
	}
}

func TestSortSessionTreeMains_MixedTimezoneCreatedAtTiebreak(t *testing.T) {
	// Equal LastActive (different offset representations of the same instant),
	// equal sort_order → created_at decides. Lexicographic comparison picks
	// "2026-08-29T09:00:00Z" < "2026-08-29T17:00:00+08:00" (both 09:00Z), but
	// equal instants must fall through to created_at, not LastActive string order.
	rows := []web.UserChatWithPreview{
		{ChatID: "a", LastActive: "2026-08-29T17:00:00+08:00", CreatedAt: "2026-08-20T10:00:00+08:00"}, // 02:00Z
		{ChatID: "b", LastActive: "2026-08-29T09:00:00Z", CreatedAt: "2026-08-19T10:00:00Z"},           // 10:00Z earlier
	}
	sortSessionTreeMains(rows)
	// Same LastActive instant (09:00Z) → tiebreak created_at asc: b (2026-08-19) first.
	if rows[0].ChatID != "b" {
		t.Errorf("sortSessionTreeMains: equal-instant LastActive (mixed offsets) must tiebreak on CreatedAt chronologically — want b first, got %s", rows[0].ChatID)
	}
}

func TestSortSessionTreeMains_PinnedBeforeUnpinnedOnEqualLastActive(t *testing.T) {
	rows := []web.UserChatWithPreview{
		{ChatID: "unpinned", LastActive: "2026-08-29T10:00:00Z", SortOrder: 0},
		{ChatID: "pinned", LastActive: "2026-08-29T10:00:00Z", SortOrder: 3},
	}
	sortSessionTreeMains(rows)
	if rows[0].ChatID != "pinned" {
		t.Errorf("sortSessionTreeMains: pinned (sort_order>0) must come first on equal LastActive — got %s", rows[0].ChatID)
	}
}
