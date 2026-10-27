package sqlite

import "testing"

// TestSuppressWALAnomaly pins the false-positive suppression for WAL state
// warnings.
//
// Two shapes were flooding the logs, both only ever with an EMPTY WAL:
//   - "WAL file DISAPPEARED" on close: 11,035 occurrences over 2026-10-20..27,
//     all with prev_size=0 (closing the last connection deletes the -wal file).
//   - "WAL file was REPLACED" after checkpoint(TRUNCATE): 26 on one restart,
//     because TRUNCATE deletes and recreates the file.
//
// Neither can strand frames. But prevSize > 0 MUST keep warning — that is the
// 2026-09-17 committed-data-loss signature, sampled 2 times in the same window
// (prev_size=107152). Losing those in the noise is the whole risk.
func TestSuppressWALAnomaly(t *testing.T) {
	cases := []struct {
		name    string
		anomaly string
		where   string
		prevSz  int64
		want    string
	}{
		// --- suppressed: empty WAL, lifecycle deletes it ---
		{"empty wal disappears on close", "missing", "close", 0, ""},
		{"empty wal disappears at TRUNCATE", "missing", "checkpoint(TRUNCATE)", 0, ""},
		{"empty wal replaced at TRUNCATE", "replaced", "checkpoint(TRUNCATE)", 0, ""},

		// --- must NOT be suppressed: frames were at stake ---
		{"non-empty wal missing on close", "missing", "close", 107152, "missing"},
		{"non-empty wal missing at TRUNCATE", "missing", "checkpoint(TRUNCATE)", 107152, "missing"},
		{"non-empty wal replaced at TRUNCATE", "replaced", "checkpoint(TRUNCATE)", 8192, "replaced"},

		// --- untouched: other observation points ---
		{"replaced outside lifecycle ops", "replaced", "open", 0, "replaced"},
		{"missing outside lifecycle ops", "missing", "open", 0, "missing"},

		// --- no anomaly stays empty ---
		{"no anomaly", "", "close", 0, ""},
		{"no anomaly non-empty", "", "checkpoint(TRUNCATE)", 4096, ""},
	}
	for _, c := range cases {
		if got := suppressWALAnomaly(c.anomaly, c.where, c.prevSz); got != c.want {
			t.Errorf("%s: suppressWALAnomaly(%q,%q,%d) = %q, want %q",
				c.name, c.anomaly, c.where, c.prevSz, got, c.want)
		}
	}
}
