package sqlite

// 误报（false positive）回归：2026-11-01 生产 `xbot-2026-11-01.log` 里 11 分钟
// 13085 条 `WAL guard: ... anomaly=missing`，覆盖 50 个会话库、每 2s 一条。
//
// 真实场景（不是外部进程，是我们自己）：
//   会话库连接池是 MaxIdleConns(1) + ConnMaxIdleTime(5min)（sessiondb.go:151）。
//   会话静默 5 分钟后池丢掉最后一个物理连接；SQLite 的 last-close 语义会
//   checkpoint + **unlink -wal**（enablePersistWAL 只对 openRawConn 里那 4 条
//   物理连接设了 SQLITE_FCNTL_PERSIST_WAL，后来按需新建的连接没有）。
//   下一轮查询新建物理连接，在新 inode 上重建 -wal；connHolder.walID 仍指着已
//   消失的旧文件 ⇒ walIdentityAnomaly 永远返回 missing/replaced，每 2s 刷一条。
//
// 判别式（本文件的断言）：良性场景里进程**不再持有任何指向旧 inode 的 fd**；
// 真异常（外部进程删/换 -wal）里，活连接的 fd 仍指着那个孤儿 inode。
// 所以「有没有孤儿 fd」才是判据，「池里开了几条连接」不是（良性场景下下一个
// 查询可能在两次巡检之间就把池重新开起来了）。

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// waitFor 轮询直到 cond 成立或超时。
func waitFor(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", d, what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestWALGuardIgnoresOwnIdleClose 复现生产误报：池 idle 关连接 ⇒ last-close
// unlink ⇒ 重开换 inode。守卫必须**不**报 anomaly，也不许换池（healed=0）。
func TestWALGuardIgnoresOwnIdleClose(t *testing.T) {
	// 断言 healed 精确值 ⇒ 必须独占巡检（否则后台 ticker 抢先处理）。
	resumeGuard := pauseWALGuard()
	defer resumeGuard()

	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	// heal=true 让「误报」表现为一次可数的换池（healed>=1），比只看日志更硬。
	walGuardEnabled, walGuardHealEnabled = true, true
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")

	db, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("open session db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Conn().Exec("INSERT INTO session_messages (tenant_id, role, content) VALUES (1,'user','before')"); err != nil {
		t.Fatalf("insert before: %v", err)
	}
	if _, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("precondition: -wal must exist while the pool is live: %v", err)
	}
	_, walIDBefore, _ := db.holder.snapshot()
	if walIDBefore == nil {
		t.Fatalf("precondition: holder must have recorded the -wal identity at open")
	}

	// 模拟「会话静默 5 分钟」：池丢掉所有物理连接（生产是 ConnMaxIdleTime=5min）。
	pool := db.holder.get()
	pool.SetMaxIdleConns(0)
	pool.SetConnMaxIdleTime(10 * time.Millisecond)
	waitFor(t, 5*time.Second, "the pool to drop every idle connection", func() bool {
		return pool.Stats().OpenConnections == 0
	})

	// 下一轮请求：新物理连接在新 inode 上重建 -wal，随后又被空闲关闭（last-close unlink）。
	if _, err := db.Conn().Exec("INSERT INTO session_messages (tenant_id, role, content) VALUES (1,'user','after')"); err != nil {
		t.Fatalf("insert after idle close: %v", err)
	}

	// 前提：这是良性场景 —— 进程里不该有任何 deleted 的 -wal/-shm fd。
	if got := deletedSidecarFDs(t, path); len(got) != 0 {
		t.Fatalf("precondition: the benign idle-close case must not leave deleted sidecar fds, got %v", got)
	}

	// 断言 1：身份不一致时不许报 anomaly。
	if anomaly := walIdentityAnomaly(path, db.holder); anomaly != "" {
		t.Fatalf("anomaly=%q after our own idle close — this is the production false positive "+
			"(50 session DBs x one line every 2s). The file was unlinked by SQLite's last-close "+
			"on a connection *we* closed; no fd of this process points at the old inode anymore.", anomaly)
	}

	// 断言 2：巡检不许因此换池（换池会让活连接全部重连）。
	if healed := walGuardTick(); healed != 0 {
		t.Fatalf("walGuardTick healed=%d, want 0: an idle-close inode change must not be treated as a foreign unlink", healed)
	}

	// 断言 3：对齐身份之后仍然是幂等的，而且数据没丢。
	if healed := walGuardTick(); healed != 0 {
		t.Fatalf("second tick healed=%d, want 0 (guard must stay idempotent)", healed)
	}
	var n int
	if err := db.Conn().QueryRow("SELECT count(*) FROM session_messages WHERE tenant_id = 1").Scan(&n); err != nil {
		t.Fatalf("count after idle close: %v", err)
	}
	if n != 2 {
		t.Fatalf("rows after idle close = %d, want 2 (no write may be lost)", n)
	}
}

// TestWALGuardStillAlarmsOnExternalUnlinkWithLiveHandle 是上一条的对照：
// 外部进程删掉 -wal 且我们还有活连接指着它 ⇒ 必须仍然报警（不许用「关掉守卫」充数）。
func TestWALGuardStillAlarmsOnExternalUnlinkWithLiveHandle(t *testing.T) {
	requirePOSIXUnlink(t)
	resumeGuard := pauseWALGuard()
	defer resumeGuard()

	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	walGuardEnabled, walGuardHealEnabled = true, false // warn：只报警，不换池
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")

	db, err := OpenSessionDB(path)
	if err != nil {
		t.Fatalf("open session db: %v", err)
	}
	defer func() { _ = db.Close() }()

	// 显式持有一条物理连接，保证 -wal 属于「活着的句柄」。
	ctx := context.Background()
	conn, err := db.holder.get().Conn(ctx)
	if err != nil {
		t.Fatalf("grab a physical connection: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "INSERT INTO session_messages (tenant_id, role, content) VALUES (2,'user','live')"); err != nil {
		t.Fatalf("insert on the held connection: %v", err)
	}

	// 模拟外部 C-sqlite 客户端 open+close / 任何外部进程 unlink。
	for _, s := range []string{"-wal", "-shm"} {
		if rmErr := os.Remove(path + s); rmErr != nil {
			t.Fatalf("simulate foreign unlink %s: %v", s, rmErr)
		}
	}

	if got := deletedSidecarFDs(t, path); len(got) == 0 {
		t.Fatalf("precondition: the live connection must still hold a deleted sidecar fd (diagnostic for the discriminator)")
	}
	if anomaly := walIdentityAnomaly(path, db.holder); anomaly == "" {
		t.Fatalf("external unlink with a live handle MUST be reported — the guard would be silently off")
	}
	if healed := walGuardTick(); healed != 0 {
		t.Fatalf("walGuardTick healed=%d with heal disabled, want 0", healed)
	}
}

// BenchmarkWALIdentityAnomalyIdleNoSidecar 钉住 idle 快路径的成本：
// 池已 idle 关连接、-wal 已被 last-close unlink（生产上 50+ 会话库的长期状态）。
// 这条路径必须**不**扫 /proc/self/fd（否则每库每 2s ≈0.25ms，59 库 ≈0.75% CPU 常驻）。
func BenchmarkWALIdentityAnomalyIdleNoSidecar(b *testing.B) {
	resumeGuard := pauseWALGuard()
	defer resumeGuard()

	dir := b.TempDir()
	path := filepath.Join(dir, "session.db")
	db, err := OpenSessionDB(path)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	pool := db.holder.get()
	pool.SetMaxIdleConns(0)
	pool.SetConnMaxIdleTime(time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for pool.Stats().OpenConnections > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	for _, s := range []string{"-wal", "-shm"} { // 让状态稳定成「无 sidecar、无连接」
		_ = os.Remove(path + s)
	}
	if pool.Stats().OpenConnections != 0 {
		b.Fatalf("precondition: pool still has open connections")
	}

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if got := walIdentityAnomaly(path, db.holder); got != "" {
			b.Fatalf("anomaly=%q on the idle fast path, want empty", got)
		}
	}
}
