package sqlite

// 测试隔离：WAL 守卫的「巡检」在进程内有一条常驻 goroutine（每 walGuardInterval
// 一次），而 walGuardTick 同时是可手动调用的测试钩子。两者会抢同一个异常。
//
// G-20261027-14 的 -race 确定性红灯（walguard_datasafety_test.go 里 healed=0）
// 就是后台 ticker 在 unlink 之后、手动 tick 之前先把 -wal 自愈掉了；手动 tick
// 于是看到一个已经一致的状态。断言 healed 精确值的用例必须先用 pauseWALGuard()
// 把后台巡检停掉，只由测试自己驱动。
//
// 证据：court/evidence/gongbu/g14-walfd-20261030/13~16-*。

import (
	"path/filepath"
	"testing"
)

// pauseWALGuard 停掉后台巡检 goroutine 并等它在飞的 tick 结束，返回 resume 函数。
//
// 只有测试会调用：生产进程里巡检只有这一条 goroutine，不存在「两条执行流抢同一个
// 异常」的问题。
func pauseWALGuard() (resume func()) {
	walGuard.mu.Lock()
	walGuard.suppress = true
	stop, done := walGuard.stop, walGuard.done
	walGuard.stop, walGuard.done = nil, nil
	walGuard.started = false
	walGuard.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}

	return func() {
		walGuard.mu.Lock()
		walGuard.suppress = false
		walGuard.mu.Unlock()
		startWALGuard()
	}
}

// TestPauseWALGuardSuppressesTicker 钉住 seam 本身：暂停期间即使再打开库也不会
// 把后台 goroutine 拉起来（否则隔离形同虚设）；resume 之后恢复自动巡检。
func TestPauseWALGuardSuppressesTicker(t *testing.T) {
	resume := pauseWALGuard()
	defer resume()

	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")
	db, err := Open(path) // 打开库会调用 startWALGuard，但 suppress 期间必须不起 goroutine
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	walGuard.mu.Lock()
	started, stop := walGuard.started, walGuard.stop
	walGuard.mu.Unlock()
	if started || stop != nil {
		t.Fatalf("background WAL guard must stay stopped while paused (started=%v stop!=nil=%v)", started, stop != nil)
	}
}
