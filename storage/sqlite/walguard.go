package sqlite

// WAL/SHM 侧文件守卫（G-20261027-14）。
//
// ── 问题（2026-10-30 隔离实例实证）──
//
// serve 用 modernc.org/sqlite，进程外任何 **读写** 型 C-sqlite 客户端（python3
// sqlite3 / sqlite3 CLI）在 serve 存活期间打开同一个库再关闭，就会触发 SQLite 的
// 「最后一个连接关闭 ⇒ checkpoint + unlink -wal/-shm」语义：它看不到本进程的
// 引用，认为自己是最后一个，于是把 serve 正在写的 -wal/-shm 删掉。实测一次
// open+close 立刻让 serve 的 4 个 -wal fd + 1 个 -shm fd 全部变成 "(deleted)"
// （fd 11/12/14/16/18，与生产 pid 2094617 逐个对应，见
// court/evidence/gongbu/g14-walfd-20261030/01-signature-repro-notes.md）。
//
// `journal_mode=WAL` + `SQLITE_FCNTL_PERSIST_WAL`（enablePersistWAL）只能让
// **我们自己**的连接关闭时不 unlink；它管不到别人删。删掉之后，serve 仍然把新帧
// 写进那个没有目录项的 inode：数据对调用方「已提交」，对任何新连接「不存在」，
// 且两边 checkpoint 抢同一批页（2026-10-21 malformed 的成因）。
//
// ── 两条不变量 ──
//
//  1. 删除只能发生在「没有任何连接打开该库之前」：包内唯一的 -wal/-shm 删除入口
//     是 RemoveWALSidecars，它先看引用计数（进程内连接句柄 + flock），不为零就
//     拒绝删除并告警。以前 session/sessiondb.go 直接 os.Remove，只要还有别的
//     handle 指着同一路径，就会造出孤儿 fd。
//  2. 替换/删除若由外部进程造成（我们拦不住），必须被**发现并自愈**：walGuard 每
//     walGuardInterval 检查一次「路径上的 -wal 是否还是我打开的那个文件」
//     （os.SameFile）。一旦 missing/replaced：先把孤儿 WAL 里已提交的帧 checkpoint
//     回主库文件（数据不丢），再把连接池整体换掉 —— 必须是「先关干净、再开新的」，
//     中间不留任何连接：sqlite 的 WAL-index 是按库文件在进程内共享的，只要还有一
//     条连接指着它，孤儿的 -shm fd 就不会释放（2026-10-30 实测：换池后 deleted
//     -shm 仍为 1，全部关闭后才归零）。
//
// 守卫幂等、可关：walGuardEnabled=false 只用于 RED/GREEN 自证（红=不自愈）。

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	log "xbot/logger"
)

// walGuardInterval 是自愈巡检周期。
//
// 2s 而不是 10s：从「外部 unlink」到「自愈完成」之间，任何新进程读这个库都只能看到
// 主库文件里的页，WAL 里那批已提交的帧对它是不可见的 —— 这个窗口越短越好。周期内
// 的开销只是对每个「当前还开着的库路径」做 1 次 os.Stat（微秒级），即使几百个会话库
// 同时开着也不构成压力；真正的换池动作只在检测到异常时才发生。
const walGuardInterval = 2 * time.Second

// ── kill switch（现场运维不许改二进制就能关） ──
//
//	XBOT_WAL_GUARD=on|off           默认 on   off  = 守卫整体停摆（连检测都不做）
//	XBOT_WAL_GUARD_HEAL=heal|warn   默认 warn warn = 只告警不自愈（默认）；heal = 尝试自愈
//
// 为什么**默认 warn**：自愈要关掉活体连接池再重开。2026-10-30 并发压测（8 写者 +
// 强制 unlink + heal）实测：换池后 `PRAGMA integrity_check` 报 `*** in database main ***`
// 且查询持续 `disk I/O error (522)` —— 「有并发写在飞时换池」会毁库。静默期换池是安全的
// （隔离实例 + 包级数据安全实验都是 200/200 行 100% 可读），但代码无法廉价地判定
// 「此刻真的静默」，所以默认只告警，把「要不要冒险自愈」交给运维显式 opt-in。
//
// 生效时机：**进程启动期一次**（包 init）。非法值不 panic：按默认值生效，并打一行
// Warn 写明收到的原值与采用的值。改完环境变量需要重启进程才生效（不做运行期热改，
// 避免「谁在什么时候翻了开关」无法追溯）。
const (
	envWALGuard     = "XBOT_WAL_GUARD"
	envWALGuardHeal = "XBOT_WAL_GUARD_HEAL"
)

// walGuardEnabled 关闭时守卫整体停摆（连检测都不做）。默认 on，可由 XBOT_WAL_GUARD 覆盖。
var walGuardEnabled = true

// walGuardHealEnabled=false 时守卫只**告警**不自愈（默认）。由 XBOT_WAL_GUARD_HEAL 覆盖。
var walGuardHealEnabled = false

func init() { applyWALGuardEnv(os.LookupEnv) }

// applyWALGuardEnv 解析 kill switch（拆出来是为了可测）。先落默认值再覆盖。
func applyWALGuardEnv(lookup func(string) (string, bool)) {
	walGuardEnabled = true
	walGuardHealEnabled = false // 默认 warn：见文件头并发压测结论
	if raw, ok := lookup(envWALGuard); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "on", "1", "true", "yes":
			walGuardEnabled = true
		case "off", "0", "false", "no":
			walGuardEnabled = false
		default:
			log.WithFields(log.Fields{"env": envWALGuard, "value": raw, "default": "on"}).
				Warn("Unrecognized WAL guard kill switch value — falling back to the default")
			walGuardEnabled = true
		}
	}
	if raw, ok := lookup(envWALGuardHeal); ok {
		switch strings.ToLower(strings.TrimSpace(raw)) {
		case "heal", "on", "true", "1":
			walGuardHealEnabled = true
		case "warn", "off", "false", "0":
			walGuardHealEnabled = false
		default:
			log.WithFields(log.Fields{"env": envWALGuardHeal, "value": raw, "default": "warn"}).
				Warn("Unrecognized WAL guard heal switch value — falling back to the default")
			walGuardHealEnabled = false
		}
	}
	if !walGuardEnabled {
		log.WithFields(log.Fields{envWALGuard: "off"}).
			Warn("WAL guard is DISABLED by kill switch — a foreign process can unlink xbot.db-wal/-shm and this process will keep writing to an unreachable inode (no detection, no heal)")
	}
}

// connHolder 是「同一路径、全进程唯一连接池」的间接层。
//
// 为什么需要间接层：自愈必须把底层 *sql.DB 整个换掉（SQLite 不会重新打开已删除的
// -wal），而同一个路径上有多个 DB handle（serverapp 的 sharedDB / tokenDB、
// multitenant、迁移路径……）。它们共享同一个 connHolder，所以换池对所有人同时可见，
// 不需要逐个通知，也不会有 handle 抱着已关闭的池。
//
// 写锁窗口（heal 期间）会让 get() 阻塞而不是返回 nil —— 调用方既不会拿到空指针，
// 也不会在「旧池已关、新池未开」的瞬间写进去。
type connHolder struct {
	mu          sync.RWMutex
	db          *sql.DB
	walID       os.FileInfo // 我们打开的那个 <path>-wal（身份，用于 SameFile 比对）
	shmID       os.FileInfo
	maxIdle     int
	maxIdleTime time.Duration
}

func (h *connHolder) get() *sql.DB {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.db
}

func (h *connHolder) snapshot() (db *sql.DB, walID, shmID os.FileInfo) {
	if h == nil {
		return nil, nil, nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.db, h.walID, h.shmID
}

// captureWALIdentity 记录当前路径上 -wal/-shm 的身份（可能为 nil：文件尚未创建）。
func captureWALIdentity(path string) (walID, shmID os.FileInfo) {
	if path == "" || path == ":memory:" {
		return nil, nil
	}
	walID, _ = os.Stat(path + "-wal")
	shmID, _ = os.Stat(path + "-shm")
	return walID, shmID
}

// ── 守卫循环 ──

var walGuard = struct {
	mu      sync.Mutex
	started bool
}{}

// startWALGuard 在第一次成功打开文件库时启动唯一的巡检 goroutine。
func startWALGuard() {
	walGuard.mu.Lock()
	if walGuard.started {
		walGuard.mu.Unlock()
		return
	}
	walGuard.started = true
	walGuard.mu.Unlock()

	log.WithFields(log.Fields{
		"enabled": walGuardEnabled, "heal": walGuardHealEnabled,
		"interval": walGuardInterval.String(),
	}).Info("WAL guard started (kill switches: " + envWALGuard + "=" + onOff(walGuardEnabled) + ", " + envWALGuardHeal + "=" + healWarn(walGuardHealEnabled) + ")")

	go func() {
		t := time.NewTicker(walGuardInterval)
		defer t.Stop()
		for range t.C {
			walGuardTick()
		}
	}()
}

// walGuardTick 巡检一遍当前仍然被引用的所有路径，返回自愈次数（测试钩子）。
func walGuardTick() int {
	if !walGuardEnabled {
		return 0
	}
	type entry struct {
		path string
		h    *connHolder
	}
	var entries []entry
	sharedConns.mu.Lock()
	for p, e := range sharedConns.m {
		entries = append(entries, entry{path: p, h: e.holder})
	}
	sharedConns.mu.Unlock()

	healed := 0
	for _, en := range entries {
		anomaly := walIdentityAnomaly(en.path, en.h)
		if anomaly == "" {
			continue
		}
		if !walGuardHealEnabled {
			log.WithFields(log.Fields{"path": en.path, "anomaly": anomaly}).
				Error("WAL guard: a foreign process removed/replaced the live -wal; heal is disabled (walGuardHealEnabled=false) — writes from now on may be unreachable")
			continue
		}
		if err := healForeignWALDeletion(en.path, en.h, anomaly); err != nil {
			log.WithError(err).WithField("path", en.path).
				Warn("WAL guard: foreign -wal loss detected but the heal attempt failed; writes from now on may be unreachable")
			continue
		}
		healed++
	}
	return healed
}

// walIdentityAnomaly 判断路径上的 -wal 是否还是我们打开的那个文件。
//
// 只认 -wal 的身份：-shm 在「池里没有物理连接」时会由 SQLite 正常删掉（池下次取
// 连接时重建），那是常态不是异常；而 -wal 有 PERSIST_WAL 保护，正常情况下只要池
// 还活着就必须还在。
func walIdentityAnomaly(path string, h *connHolder) string {
	if path == "" || path == ":memory:" || h == nil {
		return ""
	}
	_, walID, _ := h.snapshot()
	if walID == nil {
		return ""
	}
	cur, err := os.Stat(path + "-wal")
	if err != nil || cur == nil {
		return "missing"
	}
	if !os.SameFile(walID, cur) {
		return "replaced"
	}
	return ""
}

// healForeignWALDeletion 处理「外部进程删/替换了我们的 -wal」。
//
// 顺序即正确性：
//  1. 先对旧池做 wal_checkpoint(TRUNCATE)：帧从孤儿 inode 落回主库文件，已 ack 的
//     数据重新对所有人可见（数据安全的关键，先做）。
//  2. 关旧池、开新池，**中间不保留任何连接**：这一步让 sqlite 释放按库文件共享的
//     WAL-index，孤儿 -shm fd 才会真正关闭；新池在真实路径上重建 -wal/-shm。
//  3. 新池指针在 holder 写锁内发布，所有 handle 立刻生效。
//
// 竞态分析：
//   - 写锁窗口内所有 get() 阻塞（不返回 nil），窗口长度 = 关闭 + 打开一个池；
//     关旧池会等正在执行的查询返回（database/sql 语义），最长受 busy_timeout 约束。
//   - 旧池关闭时 PERSIST_WAL 保证它不会反过来 unlink -wal。
//   - sharedConns 条目与引用计数、flock 全程不变 —— 不会让第二个 xbot 进程趁虚而入。
func healForeignWALDeletion(path string, h *connHolder, anomaly string) error {
	if h == nil {
		return fmt.Errorf("heal %s: no connection holder", path)
	}
	start := time.Now()

	// 1. 数据优先：把孤儿 WAL 里已提交的帧 checkpoint 进主库文件。
	//
	// **这一步没成功就绝不关池**：关池（最后一个连接关闭）虽然还会做一次 checkpoint，
	// 但那一次同样可能拿不到锁；一旦两次都没成，未 checkpoint 的帧随着孤 inode 消失
	// ⇒ 真丢数据（2026-10-30 并发压测复现过：busy 的 checkpoint 之后关池，重开后
	// schema/表消失）。所以这里 checkpoint 必须 busy=0 且无错，否则本轮放弃自愈、
	// 留到下一个巡检周期重试（guard 每 2s 一轮，代价只是窗口多 2s）。
	if oldConn := h.get(); oldConn != nil {
		// FULL 而不是 TRUNCATE：FULL 只要把帧写回主库（能在 busy_timeout 内等写者让路），
		// TRUNCATE 还额外要求「此刻没有任何读者」才能把 WAL 归零，生产上更容易拿不到。
		// 数据安全只依赖 FULL 成功；TRUNCATE 只用来收缩文件，失败不影响判断。
		busy, logFrames, ckpt, err := walCheckpointFull(oldConn)
		if err != nil || busy != 0 {
			return fmt.Errorf("checkpoint of the orphaned WAL did not complete (busy=%d log=%d checkpointed=%d err=%v) "+
				"— refusing to reopen the pool: uncheckpointed frames would be lost (retrying on the next tick)", busy, logFrames, ckpt, err)
		}
		// 这里**不做 TRUNCATE**：池里还有别的连接可能在写，TRUNCATE 会把 WAL 从头重置，
		// 与并发写者叠加会打出 SQLITE_IOERR（2026-10-30 并发压测复现）。文件大小无所谓
		// —— 这个 inode 马上就会随旧池关闭而消失。
	}

	maxIdle, maxIdleTime := 4, time.Duration(0)
	if h.maxIdle > 0 {
		maxIdle, maxIdleTime = h.maxIdle, h.maxIdleTime
	}

	// 2. 关干净 → 开新的（中间零连接）。
	h.mu.Lock()
	defer h.mu.Unlock()
	old := h.db
	h.db, h.walID, h.shmID = nil, nil, nil
	if old != nil {
		_ = old.Close()
	}

	var (
		newConn *sql.DB
		err     error
	)
	for attempt := 1; attempt <= 3; attempt++ {
		newConn, err = openRawConn(path, maxIdle, maxIdleTime)
		if err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("reopen after foreign WAL loss: %w", err)
	}
	walID, shmID := captureWALIdentity(path)
	if walID == nil {
		_ = newConn.Close()
		return fmt.Errorf("reopened pool did not recreate %s-wal", path)
	}
	h.db, h.walID, h.shmID = newConn, walID, shmID

	log.WithFields(log.Fields{
		"path":          path,
		"anomaly":       anomaly,
		"new_wal_size":  walID.Size(),
		"new_wal_mtime": walID.ModTime().Format(time.RFC3339),
		"shm_present":   shmID != nil,
		"elapsed_ms":    time.Since(start).Milliseconds(),
	}).Error("WAL guard: a foreign process removed/replaced the live -wal (SQLite last-close unlink). Healed: orphan frames checkpointed into the main DB file, pool closed and reopened so the deleted -wal/-shm fds are released and fresh sidecars exist at the real path")
	return nil
}

// ── 删除入口 ──

// RemoveWALSidecars 是包内（以及 session 包）删除 <db> / <-wal> / <-shm> 的唯一入口。
//
// 它强制不变量 1：**只有在没有任何连接引用该路径时**才允许删除。否则返回错误、
// 一个文件都不动 —— 把「删了别人正在写的 WAL」变成一次可观测的失败，而不是一批
// deleted fd 加一批读不到的数据。
func RemoveWALSidecars(dbPath string) error {
	if dbPath == "" || dbPath == ":memory:" {
		return nil
	}
	if refs := sharedConnRefs(dbPath); refs > 0 {
		return fmt.Errorf("refusing to delete %s{-wal,-shm}: %d connection handle(s) still reference it "+
			"— deleting now would orphan their -wal fds (close all handles first)", dbPath, refs)
	}
	if refs := dbLockRefs(dbPath); refs > 0 {
		return fmt.Errorf("refusing to delete %s{-wal,-shm}: the process flock is still held (%d refs)", dbPath, refs)
	}
	var firstErr error
	removed := 0
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(dbPath + suffix); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("remove %s: %w", dbPath+suffix, err)
			}
			continue
		}
		removed++
	}
	if removed > 0 {
		log.WithFields(log.Fields{"path": dbPath, "files_removed": removed}).
			Debug("Removed database file with its WAL/SHM sidecars (no open handle, flock released)")
	}
	return firstErr
}

func onOff(v bool) string {
	if v {
		return "on"
	}
	return "off"
}

func healWarn(v bool) string {
	if v {
		return "heal"
	}
	return "warn"
}

// walCheckpointFull 跑 PRAGMA wal_checkpoint(FULL)：把所有已提交帧写回主库文件
// （等价于「数据落盘」），但不要求把 WAL 归零，因此比 TRUNCATE 容易成功。
func walCheckpointFull(conn *sql.DB) (busy, logFrames, checkpointed int, err error) {
	if scanErr := conn.QueryRow("PRAGMA wal_checkpoint(FULL)").Scan(&busy, &logFrames, &checkpointed); scanErr != nil {
		if _, execErr := conn.Exec("PRAGMA wal_checkpoint(FULL)"); execErr != nil {
			return 0, 0, 0, fmt.Errorf("wal_checkpoint(FULL): %w", execErr)
		}
		return 0, 0, 0, nil
	}
	return busy, logFrames, checkpointed, nil
}

// dbLockRefs 返回该路径上 flock 的进程内引用计数（0 = 没有 handle 持锁）。
func dbLockRefs(path string) int {
	dbLocks.mu.Lock()
	defer dbLocks.mu.Unlock()
	if e, ok := dbLocks.m[path]; ok {
		return e.refs
	}
	return 0
}
