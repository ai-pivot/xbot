package common

import (
	"fmt"
	"strings"
	"time"
)

// ─── Cross-session activity index ───

const (
	// ActivitySummaryMaxRunes is the rune-safe summary truncation length.
	ActivitySummaryMaxRunes = 200
	// ActivityLimitDefault is the default /api/activity page size.
	ActivityLimitDefault = 200
	// ActivityLimitMax caps the page size.
	ActivityLimitMax = 500
	// ActivityWindowDefault is the default active window (72h).
	ActivityWindowDefault = 72 * time.Hour
)

// activityTimeLayout is the fixed-width UTC form used by activity_index.created_at
// (string order == time order).
const activityTimeLayout = "2006-01-02T15:04:05.000000000Z"

// FormatActivityTime normalizes a time for activity_index.created_at (UTC).
func FormatActivityTime(t time.Time) string { return t.UTC().Format(activityTimeLayout) }

// NormalizeActivityLimit clamps a page size: <=0 -> default, >max -> max.
func NormalizeActivityLimit(limit int) int {
	if limit <= 0 {
		return ActivityLimitDefault
	}
	if limit > ActivityLimitMax {
		return ActivityLimitMax
	}
	return limit
}

// NormalizeActivityWindow clamps the active window: <=0 -> default.
func NormalizeActivityWindow(d time.Duration) time.Duration {
	if d <= 0 {
		return ActivityWindowDefault
	}
	return d
}

// ParseActivityTime parses activity_index.created_at (tolerant of RFC3339).
func ParseActivityTime(s string) (time.Time, error) {
	for _, layout := range []string{activityTimeLayout, time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid activity timestamp %q", s)
}

// ActivityIndexedKinds are the kinds allowed into the activity index. `text` is
// deliberately excluded: chat prose has no feed value and would bloat the index.
var ActivityIndexedKinds = map[string]bool{
	StructuredKindReport:   true,
	StructuredKindAskUser:  true,
	StructuredKindAnswer:   true,
	StructuredKindDecision: true,
	StructuredKindCommand:  true,
	StructuredKindEvent:    true,
}

// ActivityKindIndexed reports whether a kind belongs in the activity index.
func ActivityKindIndexed(kind string) bool { return ActivityIndexedKinds[kind] }

// TruncateActivitySummary truncates by runes (never splits multibyte text).
func TruncateActivitySummary(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= ActivitySummaryMaxRunes {
		return s
	}
	return string(runes[:ActivitySummaryMaxRunes])
}

// ActivityItem is one row of the cross-session activity feed.
type ActivityItem struct {
	TenantID   int64  `json:"-"`
	ChatID     string `json:"chat_id"`
	Channel    string `json:"channel"`
	MessageID  int64  `json:"message_id"`
	Kind       string `json:"kind"`
	SenderID   string `json:"-"`
	SenderName string `json:"sender_name"`
	Summary    string `json:"summary"`
	CreatedAt  string `json:"created_at"`
}
