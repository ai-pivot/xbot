package sqlite

// G-20261027-14 守卫测试：
//   - 外部进程删掉活库的 -wal/-shm（SQLite last-close unlink）后，守卫必须自愈：
//     孤儿帧 checkpoint 回主库、真实路径上重建 -wal/-shm、**本进程不再持有任何
//     deleted 的 -wal/-shm fd**（这就是生产判据 /proc/<pid>/fd 的数法）。
//   - 关闭守卫时（RED 基线）必须留下孤儿 —— 保证断言能区分红绿。
//   - 删除入口 RemoveWALSidecars 在还有 handle 引用时不得删任何文件。

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// deletedSidecarFDs 列出本进程里指向 path 的、已 deleted 的 -wal/-shm fd。
// 与生产判据 `ls -l /proc/<pid>/fd | grep -c 'xbot.db-wal (deleted)'` 同义。
func deletedSidecarFDs(t *testing.T, path string) []string {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("needs /proc (linux)")
	}
	base := filepath.Base(path)
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("read /proc/self/fd: %v", err)
	}
	var out []string
	for _, e := range entries {
		target, readErr := os.Readlink("/proc/self/fd/" + e.Name())
		if readErr != nil {
			continue
		}
		if strings.Contains(target, base) && strings.Contains(target, "(deleted)") {
			out = append(out, e.Name()+" -> "+target)
		}
	}
	return out
}

func TestWALGuardHealsForeignWALRemoval(t *testing.T) {
	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	walGuardEnabled, walGuardHealEnabled = true, true // 显式 opt-in 自愈（默认是 warn）
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Conn().Exec("CREATE TABLE probe (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Conn().Exec("INSERT INTO probe (v) VALUES ('before')"); err != nil {
		t.Fatalf("insert before: %v", err)
	}
	before, statErr := os.Stat(path + "-wal")
	if statErr != nil {
		t.Fatalf("precondition: -wal should exist while the pool is open: %v", statErr)
	}

	// 模拟外部 C-sqlite 客户端 open+close：它认为自己是最后一个连接 ⇒ unlink -wal/-shm。
	for _, s := range []string{"-wal", "-shm"} {
		if rmErr := os.Remove(path + s); rmErr != nil {
			t.Fatalf("simulate foreign unlink %s: %v", s, rmErr)
		}
	}
	if _, err := os.Stat(path + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("precondition: -wal should be gone after the simulated unlink")
	}

	if healed := walGuardTick(); healed < 1 {
		t.Fatalf("walGuardTick healed=%d, want >= 1 (the orphaned -wal must be detected)", healed)
	}

	// 1) 真实路径上重新有 -wal，且它就是我们池子现在持有的那个文件。
	after, err := os.Stat(path + "-wal")
	if err != nil {
		t.Fatalf("after heal: %s-wal is missing: %v", path, err)
	}
	_, walID, shmID := db.holder.snapshot()
	if walID == nil {
		t.Fatalf("after heal: holder has no -wal identity")
	}
	// 注意：不能断言 `!os.SameFile(before, after)` —— 旧 inode 释放后可能被新 -wal
	// 立刻复用，同号不代表没换池。真正能判死的是：路径上的文件必须就是池里当前
	// 持有的那个（walID），且进程不再留任何 deleted 的 -wal/-shm fd（见下一条）。
	_ = before
	if !os.SameFile(walID, after) {
		t.Fatalf("the -wal at the path is not the file the pool holds — writes would still go to an orphan")
	}
	if shmID == nil {
		t.Fatalf("after heal: holder has no -shm identity (a fresh -shm should exist)")
	}

	// 2) 关键判据：本进程不再持有任何 deleted 的 -wal/-shm fd。
	if got := deletedSidecarFDs(t, path); len(got) != 0 {
		t.Fatalf("after heal the process still holds %d deleted sidecar fd(s): %v", len(got), got)
	}

	// 3) 孤儿 WAL 里已提交的帧已 checkpoint 进主库文件，数据没丢。
	var v string
	if err := db.Conn().QueryRow("SELECT v FROM probe WHERE id = 1").Scan(&v); err != nil {
		t.Fatalf("read row written before the unlink: %v", err)
	}
	if v != "before" {
		t.Fatalf("row written before the unlink = %q, want %q", v, "before")
	}

	// 4) 自愈后的池可正常写。
	if _, err := db.Conn().Exec("INSERT INTO probe (v) VALUES ('after')"); err != nil {
		t.Fatalf("insert after heal: %v", err)
	}

	// 5) 幂等：状态正常时下一次巡检不再自愈（否则会无谓地换池）。
	if healed := walGuardTick(); healed != 0 {
		t.Fatalf("second tick healed=%d, want 0 (guard must be idempotent once healed)", healed)
	}
}

// RED 基线：守卫关闭时，同样的外部删除必须留下孤儿 —— 这条用例证明上面那条
// 断言有判别力（mutation：把 walGuardTick 变成 no-op，本用例仍绿而上一条变红）。
func TestWALGuardDisabledLeavesOrphanedWAL(t *testing.T) {
	oldEnabled, oldHeal := walGuardEnabled, walGuardHealEnabled
	walGuardEnabled, walGuardHealEnabled = false, false
	defer func() { walGuardEnabled, walGuardHealEnabled = oldEnabled, oldHeal }()

	dir := t.TempDir()
	path := filepath.Join(dir, "xbot.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.Conn().Exec("CREATE TABLE probe (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	for _, s := range []string{"-wal", "-shm"} {
		if rmErr := os.Remove(path + s); rmErr != nil {
			t.Fatalf("simulate foreign unlink %s: %v", s, rmErr)
		}
	}

	if healed := walGuardTick(); healed != 0 {
		t.Fatalf("walGuardTick with the guard disabled healed=%d, want 0", healed)
	}
	if _, statErr := os.Stat(path + "-wal"); !os.IsNotExist(statErr) {
		t.Fatalf("with the guard disabled the -wal must stay missing (orphan), stat err=%v", statErr)
	}
	if got := deletedSidecarFDs(t, path); len(got) == 0 {
		t.Fatalf("with the guard disabled the process must still hold deleted sidecar fds (RED baseline)")
	}
}

func TestRemoveWALSidecarsRefusesWhileReferenced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Conn().Exec("CREATE TABLE probe (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatalf("create table: %v", err)
	}

	if err := RemoveWALSidecars(path); err == nil {
		t.Fatalf("RemoveWALSidecars while a handle is open must be refused")
	}
	for _, suffix := range []string{"", "-wal"} {
		if _, statErr := os.Stat(path + suffix); statErr != nil {
			t.Fatalf("%s%s must still exist after the refusal: %v", path, suffix, statErr)
		}
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := RemoveWALSidecars(path); err != nil {
		t.Fatalf("RemoveWALSidecars after close: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, statErr := os.Stat(path + suffix); !os.IsNotExist(statErr) {
			t.Fatalf("%s%s should be gone after RemoveWALSidecars", path, suffix)
		}
	}
}
