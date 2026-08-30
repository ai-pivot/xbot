package cron

import (
	"testing"
	"time"
)

// TestNextCronTime_DomDowOrSemantics (m4) verifies standard cron day-field
// semantics: when BOTH day-of-month and day-of-week are restricted (neither is
// "*"), the day matches if EITHER field hits (OR) — the Vixie cron rule.
// Previously the code ANDed both fields, so "0 0 1 * 1" only fired on the 1st
// of a month that is also a Monday.
func TestNextCronTime_DomDowOrSemantics(t *testing.T) {
	// Wed Sep 2 2026, 10:30 local.
	now := time.Date(2026, 9, 2, 10, 30, 0, 0, time.Local)

	cases := []struct {
		name string
		expr string
		want time.Time
	}{
		// dom=1 AND dow=Monday both restricted → OR: nearest Monday
		// (Sep 7) wins over the next 1st (Oct 1, a Thursday).
		{
			name: "dom and dow both restricted: Monday before next 1st",
			expr: "0 0 1 * 1",
			want: time.Date(2026, 9, 7, 0, 0, 0, 0, time.Local),
		},
		// 13th-or-Friday both restricted: Friday Sep 4 precedes the 13th.
		{
			name: "dom and dow both restricted: Friday before the 13th",
			expr: "0 0 13 * 5",
			want: time.Date(2026, 9, 4, 0, 0, 0, 0, time.Local),
		},
		// dom only (dow unrestricted): AND semantics collapse to dom alone —
		// the 1st of October, unchanged behavior.
		{
			name: "dom only restricted",
			expr: "0 0 1 * *",
			want: time.Date(2026, 10, 1, 0, 0, 0, 0, time.Local),
		},
		// dow only (dom unrestricted): nearest Monday, unchanged behavior.
		{
			name: "dow only restricted",
			expr: "0 0 * * 1",
			want: time.Date(2026, 9, 7, 0, 0, 0, 0, time.Local),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextCronTime(tc.expr, now)
			if err != nil {
				t.Fatalf("nextCronTime(%q): %v", tc.expr, err)
			}
			if !got.Equal(tc.want) {
				t.Errorf("nextCronTime(%q) = %s, want %s", tc.expr, got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
			}
		})
	}
}
