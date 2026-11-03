package runtimeinfo

// runtimeinfo_test.go — P0-1 验收：二进制漂移检测与健康快照。
//
// 覆盖（工部 P0-1 验收第 ① ② 条）：
//   - 漂移检测纯函数的三态：指纹不变 / mtime 变 / sha256 变（外加文件消失）；
//   - CheckOnce 用真实临时文件跑一遍，且错误结论「粘住」不被后续一致读数抹掉；
//   - Health 的字段契约（含 schema_newer=true 的 degraded 场景），
//     并断言载荷里不含任何文件系统路径。
//
// 变异自证（见证据目录）：把 Drifted 恒返回 false，RunDrift 的「mtime 变」
// 「sha 变」两行必须变红；把 db.go 的 ERROR 降回 WARN 由
// storage/sqlite 的日志级别用例覆盖。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func fixedSnapshot(sha string, mtime time.Time, size int64) Snapshot {
	return Snapshot{Path: "/nonexistent/xbot", SHA256: sha, MTime: mtime, Size: size}
}

func TestDriftedThreeStates(t *testing.T) {
	base := time.Date(2026, 11, 3, 2, 51, 0, 0, time.UTC)
	const shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	snap := fixedSnapshot(shaA, base, 4096)

	cases := []struct {
		name string
		obs  Observation
		want bool
	}{
		{
			name: "unchanged fingerprint",
			obs:  Observation{Exists: true, SHA256: shaA, MTime: base, Size: 4096},
			want: false,
		},
		{
			name: "mtime changed (binary replaced, same path)",
			obs:  Observation{Exists: true, SHA256: shaA, MTime: base.Add(time.Minute), Size: 4096},
			want: true,
		},
		{
			name: "sha256 changed (different build, same size and mtime)",
			obs:  Observation{Exists: true, SHA256: shaB, MTime: base, Size: 4096},
			want: true,
		},
		{
			name: "size changed",
			obs:  Observation{Exists: true, SHA256: shaA, MTime: base, Size: 8192},
			want: true,
		},
		{
			name: "file disappeared",
			obs:  Observation{Exists: false},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Drifted(snap, tc.obs); got != tc.want {
				t.Fatalf("Drifted(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestCheckOnceDetectsDiskDrift exercises the same three states through the real
// filesystem path (CapturePath + ObserveFile), not just the pure function.
func TestCheckOnceDetectsDiskDrift(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "xbot-cli")
	if err := os.WriteFile(bin, []byte("build-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := New()
	if err := st.CapturePath(bin); err != nil {
		t.Fatalf("capture: %v", err)
	}
	snap, ok := st.Snapshot()
	if !ok || snap.SHA256 == "" || snap.Size != int64(len("build-one")) {
		t.Fatalf("unexpected snapshot: %+v (ok=%v)", snap, ok)
	}
	if st.CheckOnce() {
		t.Fatal("freshly captured binary must not report drift")
	}
	if h := st.Health(); h.Status != StatusOK || h.BinaryChanged {
		t.Fatalf("health after clean check = %+v, want ok/not-changed", h)
	}

	// State 1: mtime only — a redeploy that keeps the bytes (touch / re-link).
	// The file is still "not the file we booted from", so it counts.
	later := snap.MTime.Add(2 * time.Minute)
	if err := os.Chtimes(bin, later, later); err != nil {
		t.Fatal(err)
	}
	if !st.CheckOnce() {
		t.Fatal("mtime change must be reported as drift")
	}
	if !st.BinaryChanged() {
		t.Fatal("BinaryChanged must be sticky after drift")
	}
	h := st.Health()
	if h.Status != StatusDegraded || !h.BinaryChanged {
		t.Fatalf("health after drift = %+v, want degraded/changed", h)
	}
	if len(h.BinarySHA256) != shaExposedLen {
		t.Fatalf("binary_sha256 = %q, want %d hex chars", h.BinarySHA256, shaExposedLen)
	}

	// State 2: sha only — a different build dropped in place. Even if we reset
	// the drift flag by re-capturing, the new content must be caught.
	if err := os.WriteFile(bin, []byte("build-two"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, snap.MTime, snap.MTime); err != nil {
		t.Fatal(err)
	}
	if !st.CheckOnce() {
		t.Fatal("sha256 change must be reported as drift")
	}

	// State 3: the stale file is replaced by exactly the bytes we booted from
	// (same size, same mtime, same sha) ⇒ the *current* verdict is "no drift",
	// but BinaryChanged stays true: one missed restart is not erased by a later
	// consistent read.
	if err := os.WriteFile(bin, []byte("build-one"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(bin, snap.MTime, snap.MTime); err != nil {
		t.Fatal(err)
	}
	if st.CheckOnce() {
		t.Fatal("restored binary must match the snapshot again")
	}
	if !st.BinaryChanged() {
		t.Fatal("BinaryChanged must stay sticky after a consistent re-read")
	}
}

func TestCapturePathMissingFile(t *testing.T) {
	st := New()
	if err := st.CapturePath(filepath.Join(t.TempDir(), "gone")); err == nil {
		t.Fatal("capturing a missing binary must return an error")
	}
	if _, ok := st.Snapshot(); ok {
		t.Fatal("failed capture must not install a snapshot")
	}
}

func TestHealthSchemaNewerIsDegraded(t *testing.T) {
	st := New()
	dir := t.TempDir()
	bin := filepath.Join(dir, "xbot-cli")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.CapturePath(bin); err != nil {
		t.Fatal(err)
	}

	// aligned: ok
	st.SetSchemaVersions(80, 80)
	h := st.Health()
	if h.Status != StatusOK || h.SchemaNewer || h.SchemaBinary != 80 || h.SchemaDB != 80 {
		t.Fatalf("aligned schema health = %+v, want ok/80/80", h)
	}

	// database newer than binary: the exact 11-02 incident
	st.SetSchemaVersions(73, 80)
	h = st.Health()
	if !h.SchemaNewer {
		t.Fatalf("schema_newer must be true for binary=73 db=80: %+v", h)
	}
	if h.Status != StatusDegraded {
		t.Fatalf("status = %q, want %q when schema_newer", h.Status, StatusDegraded)
	}

	// unknown database (never reported, e.g. PG backend) is not "newer"
	st.SetSchemaVersions(80, 0)
	if h := st.Health(); h.SchemaNewer || h.Status != StatusOK {
		t.Fatalf("unreported db version must not degrade: %+v", h)
	}
}

// TestHealthJSONFieldContract pins the exact wire contract external probes and
// scripts/restart-all-instances.sh rely on.
func TestHealthJSONFieldContract(t *testing.T) {
	st := New()
	dir := t.TempDir()
	bin := filepath.Join(dir, "xbot-cli")
	if err := os.WriteFile(bin, []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := st.CapturePath(bin); err != nil {
		t.Fatal(err)
	}
	st.SetSchemaVersions(73, 80)

	raw, err := json.Marshal(st.Health())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"status", "binary_sha256", "binary_mtime", "binary_changed",
		"schema_binary", "schema_db", "schema_newer", "checked_at",
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sort.Strings(want)
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Fatalf("health JSON keys = %v, want %v", keys, want)
	}

	if got["status"] != StatusDegraded {
		t.Fatalf("status = %v, want %q", got["status"], StatusDegraded)
	}
	if got["schema_newer"] != true || got["schema_binary"] != float64(73) || got["schema_db"] != float64(80) {
		t.Fatalf("schema fields = %v/%v/%v", got["schema_newer"], got["schema_binary"], got["schema_db"])
	}
	if got["binary_changed"] != false {
		t.Fatalf("binary_changed = %v, want false", got["binary_changed"])
	}

	// Timestamps are RFC3339 UTC and the payload leaks no filesystem path.
	for _, field := range []string{"binary_mtime", "checked_at"} {
		s, ok := got[field].(string)
		if !ok || s == "" {
			t.Fatalf("%s = %v, want RFC3339 string", field, got[field])
		}
		if _, err := time.Parse(time.RFC3339, s); err != nil {
			t.Fatalf("%s = %q is not RFC3339: %v", field, s, err)
		}
	}
	for field, value := range got {
		if s, ok := value.(string); ok && strings.Contains(s, dir) {
			t.Fatalf("%s = %q leaks a filesystem path", field, s)
		}
	}
	if len(got["binary_sha256"].(string)) != shaExposedLen {
		t.Fatalf("binary_sha256 = %q, want %d chars", got["binary_sha256"], shaExposedLen)
	}
}

func TestIntervalFromEnv(t *testing.T) {
	t.Setenv(DriftIntervalEnv, "")
	if got := IntervalFromEnv(); got != DefaultDriftInterval {
		t.Fatalf("default interval = %v", got)
	}
	t.Setenv(DriftIntervalEnv, "15s")
	if got := IntervalFromEnv(); got != 15*time.Second {
		t.Fatalf("configured interval = %v", got)
	}
	t.Setenv(DriftIntervalEnv, "nonsense")
	if got := IntervalFromEnv(); got != DefaultDriftInterval {
		t.Fatalf("invalid interval must fall back to default, got %v", got)
	}
}

// TestStartWatcherChecksPeriodically proves the ticker actually drives CheckOnce
// (so binary_changed is not dependent on someone hitting the health endpoint).
func TestStartWatcherChecksPeriodically(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "xbot-cli")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	st := New()
	if err := st.CapturePath(bin); err != nil {
		t.Fatal(err)
	}
	st.StartWatcher(10 * time.Millisecond)
	t.Cleanup(st.StopWatcher)

	if err := os.WriteFile(bin, []byte("v2-different"), 0o755); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st.BinaryChanged() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("watcher never detected the replaced binary")
}
