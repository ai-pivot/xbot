package sqlite

// G-20261027-14 并发场景判据（工部 2026-10-30 要求）。
//
// 结论先行（实验证据见 court/evidence/gongbu/g14-walfd-20261030/07-concurrent-heal.log）：
// **有并发写在飞时换池会毁库**（实测 `*** in database main ***` + 持续 disk I/O error），
// 所以守卫默认 `warn`（只告警不自愈），`heal` 需运维显式 opt-in。
//
// 因此这里钉住的判据是「默认模式下，外部 unlink 期间并发写不会把库写坏」：
//   ① 巡检不换池（heal 次数 = 0）；
//   ② 进程仍持有 deleted 的 -wal/-shm fd（RED 语义，等重启或运维 opt-in）；
//   ③ PRAGMA integrity_check 仍然 ok（默认模式**不会**引入新的损坏）。

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWALGuardWarnModeKeepsDBConsistentUnderConcurrentWrites(t *testing.T) {
	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	walGuardEnabled, walGuardHealEnabled = true, false // 默认配置：on + warn
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Conn().Exec("CREATE TABLE cw (id INTEGER PRIMARY KEY, g INTEGER, n INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}

	const writers, perWriter = 8, 30
	var stop int32
	var wg sync.WaitGroup
	var okCount, errCount int64
	for g := 0; g < writers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				if atomic.LoadInt32(&stop) == 1 {
					return
				}
				id := g*perWriter + i
				if _, execErr := db.Conn().Exec("INSERT OR IGNORE INTO cw (id, g, n) VALUES (?, ?, ?)", id, g, i); execErr != nil {
					atomic.AddInt64(&errCount, 1)
					continue
				}
				atomic.AddInt64(&okCount, 1)
				time.Sleep(time.Millisecond)
			}
		}(g)
	}

	time.Sleep(10 * time.Millisecond) // 让写者先跑起来
	for _, s := range []string{"-wal", "-shm"} {
		if rmErr := os.Remove(path + s); rmErr != nil && !os.IsNotExist(rmErr) {
			t.Fatalf("unlink %s: %v", s, rmErr)
		}
	}

	healed := 0
	for attempt := 0; attempt < 10; attempt++ {
		healed += walGuardTick()
		time.Sleep(10 * time.Millisecond)
	}
	if healed != 0 {
		t.Fatalf("default mode (warn) must never reopen the pool, got healed=%d", healed)
	}
	if got := deletedSidecarFDs(t, path); len(got) == 0 {
		t.Fatalf("expected the orphaned (deleted) sidecar fds to remain in warn mode")
	}

	var integrity string
	if err := db.Conn().QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check query: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q, want ok — warn mode must not corrupt the database", integrity)
	}

	atomic.StoreInt32(&stop, 1)
	wg.Wait()
	t.Logf("warn-mode under concurrent writes: ok=%d err=%d integrity=%s heals=0 deleted_fds=%d",
		atomic.LoadInt64(&okCount), atomic.LoadInt64(&errCount), integrity, len(deletedSidecarFDs(t, path)))
}

// 静默期换池是安全的 —— 与 walguard_datasafety_test.go 同义，这里只加一条「heal 显式
// opt-in 时确实会换池」的最小断言，防止 opt-in 路径被误删。
func TestWALGuardHealOptInStillWorksWhenQuiescent(t *testing.T) {
	// 断言 healed 精确值 ⇒ 必须独占「这一轮巡检」，否则后台 ticker 会在 unlink
	// 之后抢先自愈，手动 tick 只能看到已一致的状态（G-20261027-14 -race 红灯）。
	resumeGuard := pauseWALGuard()
	defer resumeGuard()

	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	walGuardEnabled, walGuardHealEnabled = true, true
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Conn().Exec("CREATE TABLE q (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.Conn().Exec("INSERT INTO q (id) VALUES (1)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for _, s := range []string{"-wal", "-shm"} {
		if rmErr := os.Remove(path + s); rmErr != nil {
			t.Fatalf("unlink %s: %v", s, rmErr)
		}
	}
	if healed := walGuardTick(); healed != 1 {
		t.Fatalf("opt-in heal on a quiescent DB must succeed, got healed=%d", healed)
	}
	if got := deletedSidecarFDs(t, path); len(got) != 0 {
		t.Fatalf("after an opt-in heal no deleted sidecar fd may remain: %v", got)
	}
	var integrity string
	if err := db.Conn().QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Fatalf("integrity_check = %q after quiescent heal, want ok", integrity)
	}
}
