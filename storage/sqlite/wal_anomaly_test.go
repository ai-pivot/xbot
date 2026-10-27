package sqlite

import "testing"

// TestSuppressWALAnomaly pins the false-positive fix for the
// "WAL file was REPLACED" warning.
//
// A TRUNCATE checkpoint deletes and recreates the -wal file by design, so the
// inode always differs. When the old WAL held zero frames there is nothing that
// could become unreachable, yet every restart on 2026-10-27 logged one warning
// per session database (26 total).
func TestSuppressWALAnomaly(t *testing.T) {
	cases := []struct {
		name    string
		anomaly string
		where   string
		prevSz  int64
		want    string
	}{
		{
			name:    "empty wal truncated by checkpoint is normal",
			anomaly: "replaced", where: "checkpoint(TRUNCATE)", prevSz: 0,
			want: "",
		},
		{
			name:    "non-empty wal after TRUNCATE must still be reported",
			anomaly: "replaced", where: "checkpoint(TRUNCATE)", prevSz: 8192,
			want: "replaced",
		},
		{
			name:    "replaced outside checkpoint is untouched",
			anomaly: "replaced", where: "open", prevSz: 0,
			want: "replaced",
		},
		{
			name:    "missing is never suppressed",
			anomaly: "missing", where: "checkpoint(TRUNCATE)", prevSz: 0,
			want: "missing",
		},
		{
			name:    "no anomaly stays empty",
			anomaly: "", where: "checkpoint(TRUNCATE)", prevSz: 0,
			want: "",
		},
	}
	for _, c := range cases {
		if got := suppressWALAnomaly(c.anomaly, c.where, c.prevSz); got != c.want {
			t.Errorf("%s: suppressWALAnomaly(%q,%q,%d) = %q, want %q",
				c.name, c.anomaly, c.where, c.prevSz, got, c.want)
		}
	}
}
