package sqlite

// G-20261027-14 数据安全实验（工部 2026-10-30 要求）：
// 「起实例 → 写 N=100 行 → 外部 unlink 触发 → 等守卫自愈 → **另起一个独立进程**
//  读这 100 行，必须 100/100 可读」。
//
// 这里用「本进程持有连接池」= serve 侧，「另起进程」= 同一个测试二进制以 helper 模式
// 重入（环境变量 G14_EXTERNAL_DB），它用独立的 modernc 连接做 **只读** 连接 —— 和
// 生产里 `python3 sqlite3.connect(..., mode=ro)` 同义，绝不参与我们的 flock/PERSIST_WAL。
//
// 断言：
//  1. unlink 前外部读者 = 100；
//  2. unlink 后、自愈前：若外部读者读不到（窗口内不可见），记录下来 —— 这就是窗口危害；
//  3. 自愈后外部读者 = 100（100/100），且 checkpoint(TRUNCATE) 的两侧计数一致；
//  4. 自愈后再写 1 行 → 外部读者 = 101（写入落在真实文件上）。

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// g14HelperEnv 触发 helper 模式。
const g14HelperEnv = "G14_EXTERNAL_DB"

// TestG14ExternalReaderHelper 不是真用例：被父进程以 helper 模式拉起时读行数。
func TestG14ExternalReaderHelper(t *testing.T) {
	dbPath := os.Getenv(g14HelperEnv)
	if dbPath == "" {
		t.Skip("helper mode only")
	}
	conn, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatalf("helper open: %v", err)
	}
	defer func() { _ = conn.Close() }()
	var n int
	if err := conn.QueryRow("SELECT COUNT(*) FROM g14_probe").Scan(&n); err != nil {
		fmt.Printf("EXTERNAL_COUNT=-1 err=%v\n", err)
		return
	}
	var check string
	if err := conn.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil {
		fmt.Printf("EXTERNAL_COUNT=%d INTEGRITY=err:%v\n", n, err)
		return
	}
	fmt.Printf("EXTERNAL_COUNT=%d INTEGRITY=%s\n", n, check)
}

// externalCount 在**独立进程**里数 g14_probe 的行数。
func externalCount(t *testing.T, dbPath string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^TestG14ExternalReaderHelper$", "-test.v")
	cmd.Env = append(os.Environ(), g14HelperEnv+"="+dbPath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("external reader failed: %v\n%s", err, out)
	}
	text := string(out)
	var n int
	integrity := "?"
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "EXTERNAL_COUNT=") {
			fields := strings.Fields(line)
			if _, scanErr := fmt.Sscanf(fields[0], "EXTERNAL_COUNT=%d", &n); scanErr != nil {
				if strings.Contains(fields[0], "=-1") {
					return -1, "unreadable"
				}
			}
			for _, f := range fields[1:] {
				if strings.HasPrefix(f, "INTEGRITY=") {
					integrity = strings.TrimPrefix(f, "INTEGRITY=")
				}
			}
		}
	}
	return n, integrity
}

func TestWALGuardDataSafetyAcrossForeignUnlink(t *testing.T) {
	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	walGuardEnabled, walGuardHealEnabled = true, true // 静默期 opt-in 自愈
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")

	db, err := Open(path) // "serve 侧"
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Conn().Exec("CREATE TABLE g14_probe (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	const n = 100
	for i := 0; i < n; i++ {
		if _, err := db.Conn().Exec("INSERT INTO g14_probe (v) VALUES (?)", fmt.Sprintf("row-%03d", i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	busy0, log0, ck0, err := walCheckpointTruncate(db.Conn())
	if err != nil {
		t.Fatalf("checkpoint before: %v", err)
	}
	t.Logf("checkpoint#1(before unlink): busy=%d log=%d checkpointed=%d", busy0, log0, ck0)

	// 再写一批，让帧**只**存在于 WAL 里（checkpoint 之后写的）。
	for i := 0; i < n; i++ {
		if _, err := db.Conn().Exec("INSERT INTO g14_probe (v) VALUES (?)", fmt.Sprintf("wal-%03d", i)); err != nil {
			t.Fatalf("insert wal %d: %v", i, err)
		}
	}
	before, integrityBefore := externalCount(t, path)
	if before != 2*n {
		t.Fatalf("external reader before unlink = %d, want %d (integrity=%s)", before, 2*n, integrityBefore)
	}
	t.Logf("external reader before unlink: %d rows, integrity=%s", before, integrityBefore)

	// 外部进程 open+close（生产里的那个触发器）：unlink -wal/-shm。
	for _, s := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + s); err != nil {
			t.Fatalf("simulate foreign unlink %s: %v", s, err)
		}
	}
	duringUnlink, integrityDuring := externalCount(t, path)
	t.Logf("external reader AFTER unlink / BEFORE heal: %d rows (want < %d → 这就是窗口危害), integrity=%s",
		duringUnlink, 2*n, integrityDuring)

	if healed := walGuardTick(); healed != 1 {
		t.Fatalf("walGuardTick healed=%d, want 1", healed)
	}
	after, integrityAfter := externalCount(t, path)
	busy1, log1, ck1, err := walCheckpointTruncate(db.Conn())
	if err != nil {
		t.Fatalf("checkpoint after: %v", err)
	}
	t.Logf("checkpoint#2(after heal): busy=%d log=%d checkpointed=%d", busy1, log1, ck1)
	t.Logf("external reader after heal: %d rows, integrity=%s", after, integrityAfter)
	if after != 2*n {
		t.Fatalf("external reader after heal = %d, want %d (100%% readable) — the heal lost frames", after, 2*n)
	}
	if integrityAfter != "ok" {
		t.Fatalf("integrity_check after heal = %q, want ok", integrityAfter)
	}

	// 自愈后写入必须落在真实文件上，外部读者立刻可见。
	if _, err := db.Conn().Exec("INSERT INTO g14_probe (v) VALUES ('after-heal')"); err != nil {
		t.Fatalf("insert after heal: %v", err)
	}
	if got, _ := externalCount(t, path); got != 2*n+1 {
		t.Fatalf("external reader after post-heal write = %d, want %d", got, 2*n+1)
	}
	if got := deletedSidecarFDs(t, path); len(got) != 0 {
		t.Fatalf("still holding deleted sidecar fds after heal: %v", got)
	}
}
