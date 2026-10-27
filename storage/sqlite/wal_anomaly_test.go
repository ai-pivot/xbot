package sqlite

import "testing"

// TestSuppressWALAnomaly pins the P0 correction made on 2026-10-27.
//
// 背景：生产 pid 1024363 上实测到
//
//	fd 11,14,16,...(8 个) → xbot.db-wal (deleted)  inode 2908514, 4,181,832 B
//	磁盘上的 -wal                                  inode 2930465, size = 0
//
// 即一个 4MB、含已提交帧的 WAL 被 unlink，而进程仍持有它；磁盘上换成了空 inode。
//
// 上一版抑制逻辑把 `checkpoint(TRUNCATE)` 也放进了白名单，理由是"TRUNCATE 会
// 删掉并重建 -wal"。它依赖 prevSize == 0 来判断"没有帧可丢"，但 prevSize 只是
// **上一次采样**的大小：中间写满 4MB 再被 unlink，prevSize 仍然是 0，于是这条
// 本该报警的 REPLACED 被静默吞掉 —— 降噪修复掩盖了真事故。
//
// 现在的规则：inode 变化（replaced）一律不抑制。只保留
// `missing + close + prevSize==0` 这一种规定语义内的抑制。
func TestSuppressWALAnomaly(t *testing.T) {
	cases := []struct {
		name    string
		anomaly string
		where   string
		prevSz  int64
		want    string
	}{
		// ── 永不抑制：inode 变了就是事故信号 ──
		{"replaced at TRUNCATE with empty sampled WAL", "replaced", "checkpoint(TRUNCATE)", 0, "replaced"},
		{"replaced at close", "replaced", "close", 0, "replaced"},
		{"replaced with data (the 4MB incident shape)", "replaced", "close", 4181832, "replaced"},
		{"replaced at open", "replaced", "open", 0, "replaced"},

		// ── 保留的唯一抑制：关库时删 -wal 是规定语义，且上次采样为空 ──
		{"empty wal missing on close", "missing", "close", 0, ""},

		// ── 其余一律照报 ──
		{"non-empty wal missing on close", "missing", "close", 107152, "missing"},
		{"empty wal missing at TRUNCATE", "missing", "checkpoint(TRUNCATE)", 0, "missing"},
		{"empty wal missing at open", "missing", "open", 0, "missing"},

		// ── 无异常 ──
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

// TestSuppressWALAnomaly_NeverHidesInodeChange states the invariant directly:
// no combination of (where, prevSize) may suppress a replaced anomaly. This is
// the property whose violation hid a 4MB unlinked WAL on production.
func TestSuppressWALAnomaly_NeverHidesInodeChange(t *testing.T) {
	places := []string{"open", "close", "checkpoint(TRUNCATE)", "unknown", ""}
	sizes := []int64{0, 1, 4096, 107152, 4181832}
	for _, where := range places {
		for _, sz := range sizes {
			if got := suppressWALAnomaly("replaced", where, sz); got != "replaced" {
				t.Errorf("where=%q prevSize=%d: replaced anomaly was suppressed — "+
					"an inode change with uncheckpointed frames would go unnoticed", where, sz)
			}
		}
	}
}
