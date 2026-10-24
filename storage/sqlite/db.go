package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"

	"xbot/storage/internal"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database connection with schema management.
// Uses WAL mode with a read-write pool (max 4 conns) for concurrent reads.
// WAL allows one writer + multiple readers simultaneously, so API read
// queries (session-tree, get_history, get_context_usage) don't block on
// agent writes (IncrementalPersist, SaveState).
//
// busy_timeout is set via DSN (_pragma) so ALL connections in the pool
// retry on SQLITE_BUSY, not just the first one. This is critical when
// MaxOpenConns > 1 — without DSN-level pragma, only the first connection
// gets busy_timeout, and concurrent writes on other connections fail
// immediately with SQLITE_BUSY.
type DB struct {
	conn         *sql.DB
	path         string
	closed       atomic.Bool // Close 幂等（D-01）
	mu           sync.RWMutex
	historyLocks [historyLockStripes]sync.Mutex

	// writeMu serializes EVERY in-process write transaction.
	//
	// SQLite allows exactly one writer at a time; WAL only removes the
	// reader/writer conflict. The DSN's busy_timeout(10000) makes a second
	// writer wait — but the modernc (pure-Go) driver can return SQLITE_BUSY on
	// the **write-lock acquisition path without consulting the busy handler**
	// (the same hole user_token_usage.go originally worked around with its own
	// writeMu). Under a large multi-agent orchestration many sessions/SubAgents
	// (each its own tenant) write concurrently, so a collision used to abort a
	// whole turn — 2026-09-18 P0:
	//   `persist message batch: begin immediate history write:
	//    database is locked (5) (SQLITE_BUSY)` (20 iterations of work lost).
	//
	// Serializing in-process removes the collision at the SOURCE — no retries,
	// no fallbacks, no defensive busy-checks. SQLite itself serializes writers,
	// so the gate costs nothing in write throughput; it only prevents the
	// driver-level collision. EVERY write transaction in this package MUST hold
	// it for the whole BEGIN..COMMIT window.
	writeMu sync.Mutex

	// WAL observability (see logWALState). Remembers the last observed
	// <db>-wal file so that a replacement (unlink + recreate) — which is how
	// committed frames can end up in a file that no longer has a name — is
	// logged instead of passing silently. Guarded by walMu.
	walMu       sync.Mutex
	lastWALInfo os.FileInfo
	lastWALSize int64

	// lockFile 持有 <db>.lock 上的 flock(LOCK_EX)。进程存活期间一直持有，
	// Close 或进程退出（含崩溃）时由 OS 释放。
	//
	// 存在理由（2026-10-21 主库损坏事故）：SQLite 的 WAL 多进程共享只在
	// 各进程都通过 SQLite 加锁时才安全；一旦有两个 xbot 实例（例如从
	// /tmp/xbot-team 误启动、没设 XBOT_HOME 而落到生产库的那个）打开同一个
	// 文件，先退出的实例会 checkpoint 并 unlink 掉 -wal，正在写的实例随即
	// 读到结构损坏的页 —— `database disk image is malformed (11)`，
	// page 248/247/4 全坏，只能从备份恢复。
	//
	// 独占锁把这种事故变成「第二个实例启动失败」，生产实例不受影响。
	lockFile *os.File
}

const schemaVersion = 77
const historyLockStripes = 64

// dbLockEntry 是进程内对某个数据库文件持有的 flock，带引用计数。
//
// 为什么需要引用计数：本进程内有多处独立 `sqlite.Open()` 同一个主库
// （serverapp 的 sharedDB / tokenDB、storage.MigrateMemoryToFiles、
// session.MultiTenant），而 Linux 的 flock 是 **open file description** 级 ——
// 同一个进程再次 open + flock(LOCK_EX) 会失败（EWOULDBLOCK）。所以锁必须按
// 路径复用：第一次真正 flock，后续 Open 只增加引用计数，最后一个 Close 才释放。
type dbLockEntry struct {
	f    *os.File
	refs int
}

var dbLocks = struct {
	mu sync.Mutex
	m  map[string]*dbLockEntry
}{m: make(map[string]*dbLockEntry)}

// acquireDBLock 取得（必要时创建）该路径的进程级独占锁。
//
// 为什么不用 SQLite 自己的锁：SQLite 的多进程锁只在**所有参与者都通过 SQLite
// 访问**时成立。它能防止同时写，但**无法阻止**第二个进程在退出时 checkpoint
// 并 unlink 掉 -wal —— 那正是 2026-10-21 主库被写坏的原因（page 248/247/4
// 全部 malformed，只能从 pre-v75 备份恢复）。flock 是内核级的「同一时刻只有
// 一个进程持有」，第二个进程在打开数据库之前就被拦住，生产实例不受影响。
func acquireDBLock(path string) (*os.File, error) {
	dbLocks.mu.Lock()
	defer dbLocks.mu.Unlock()

	if e, ok := dbLocks.m[path]; ok {
		e.refs++
		return e.f, nil
	}

	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database lock file %s: %w", lockPath, err)
	}
	if err := flockExclusive(f); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("database %s is already in use by another xbot process (flock %s: %w) "+
			"— refusing to open it: two processes sharing one WAL database can corrupt it",
			path, lockPath, err)
	}
	// 记录持锁者 PID，便于排查「谁占着这个库」。
	_ = f.Truncate(0)
	if _, werr := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); werr != nil {
		log.WithError(werr).WithField("path", lockPath).Warn("Failed to record database lock owner pid")
	}
	dbLocks.m[path] = &dbLockEntry{f: f, refs: 1}
	return f, nil
}

// releaseDBLock 减少引用计数，归零时才真正释放 flock 并关闭锁文件（幂等）。
// 进程崩溃时 OS 也会释放锁。
func releaseDBLock(path string) {
	dbLocks.mu.Lock()
	defer dbLocks.mu.Unlock()

	e, ok := dbLocks.m[path]
	if !ok {
		return
	}
	e.refs--
	if e.refs > 0 {
		return
	}
	delete(dbLocks.m, path)
	_ = funlock(e.f)
	_ = e.f.Close()
}

// Open opens or creates a SQLite database at the given path
// If the database doesn't exist, it will be created with the required schema
func Open(path string) (*DB, error) {
	// Ensure directory exists (skip for :memory: which is in-memory SQLite)
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}

	// ── 数据安全（2026-09-16 用户要求：防止 xbot-cli 运行覆盖已有 db 数据）──
	// 设计铁律：**启动路径永不阻塞**。xbot-server 由 supervisor 托管，这里一旦返回错误
	// 就会形成「启动失败 → 自动重启 → 再失败」的崩溃循环，所以只做**告警 + 数据保全**：
	//   ① 文件存在但 0 字节 → 可能是新库（SQLite 首次写入前就是 0 字节），也可能是数据
	//      丢失现场 ⇒ 只 WARN（若同级有备份，一并把备份路径打出来）。
	//   ② 文件存在且非空但不是 SQLite → 只 WARN，绝不改写/截断该文件；是不是数据库交给
	//      驱动判定（驱动自己会报错，不需要我们额外制造一条启动失败路径）。
	//   ③ 文件不存在但同目录有备份 → 只 WARN 并列出备份路径（不自动恢复、不阻塞启动）。
	if path != ":memory:" {
		if fi, statErr := os.Stat(path); statErr == nil {
			if fi.Size() == 0 {
				if baks := siblingDBBackups(path); len(baks) > 0 {
					log.WithFields(log.Fields{"path": path, "backups": baks}).
						Warn("Database file exists but is EMPTY (0 bytes) and backup(s) exist — if this is unexpected data loss, STOP and restore from a backup; continuing")
				} else {
					log.WithField("path", path).
						Warn("Database file exists but is EMPTY (0 bytes) — treating as a fresh database (normal before the first write)")
				}
			} else if hdrErr := verifySQLiteHeader(path); hdrErr != nil {
				log.WithFields(log.Fields{"path": path, "reason": hdrErr.Error()}).
					Warn("Database file does not start with the SQLite magic — leaving it untouched (never truncated/overwritten); the driver will report an error if it is not a database")
			}
		} else if os.IsNotExist(statErr) {
			if baks := siblingDBBackups(path); len(baks) > 0 {
				log.WithFields(log.Fields{"path": path, "backups": baks}).
					Warn("Creating a NEW empty database while backup(s) exist — if you did not expect an empty database, STOP and restore from one of the backups listed above")
			} else {
				log.WithField("path", path).
					Warn("Creating a NEW empty database file (none existed). If this is unexpected, STOP and restore from backup.")
			}
		}
	}

	// 进程级独占锁：在打开数据库**之前**取 flock。第二个实例在这里就失败，
	// 绝不会走到「共享同一个 WAL 文件」那一步。
	var lockFile *os.File
	if path != ":memory:" {
		lf, lockErr := acquireDBLock(path)
		if lockErr != nil {
			return nil, lockErr
		}
		lockFile = lf
	}

	// 主库连接池：MaxIdleConns(4)（常驻热连接，主库是全局单点）；会话库走
	// openSQLite 的会话参数（MaxIdleConns(1) + ConnMaxIdleTime，见 OpenSessionDB）。
	db, err := openSQLite(path, 4, 0)
	if err != nil {
		releaseDBLock(path)
		return nil, err
	}
	db.lockFile = lockFile

	// Initialize schema
	if err := db.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
	}

	// 启动兜底（幂等）：补建「不在迁移链里、可能整库都没有」的表 —— oauth_tokens 与
	// agent 记忆表（xbot_short_term_memories / xbot_long_term_memories）。本地/生产库
	// 都已是当前版本，initSchema 在 version == schemaVersion 时直接 return，不会补这类表，
	// 所以必须在这里补；否则 OAuth 登录/记忆功能会撞 no such table。
	// 铁律：ensureStartupSchema **不返回错误**，只打 WARN —— 启动路径永不阻塞。
	{
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ensureStartupSchema(ctx, db)
		cancel()
	}

	log.WithFields(log.Fields{"path": path, "caller": callerTag(1)}).Info("SQLite database opened")
	db.logWALState("open")
	return db, nil
}

// historyLock serializes compound history operations using a bounded striped
// lock table. Different tenants normally proceed independently, while the
// fixed stripe count avoids retaining one mutex for every historical tenant.
func (db *DB) historyLock(tenantID int64) *sync.Mutex {
	stripe := uint64(tenantID) % uint64(len(db.historyLocks))
	return &db.historyLocks[stripe]
}

// Close closes the database connection.
//
// It logs the caller and the WAL file state: closing the LAST connection to a
// WAL database checkpoints and DELETES the -wal file. An unexpected closer (a
// component that opens/closes the same database on its own) is therefore
// enough to reset the WAL of a connection that is still writing — exactly the
// class of event that lost ~2 minutes of committed writes on 2026-09-17.
func (db *DB) Close() error {
	// 幂等：同一 handle 被 Close 两次时，不得再减一次引用计数。
	// 2026-10-22 审计 D-01 实证：Open 两次 → a.Close() 两次 → b 还活着，
	// 但锁已 LOCK_UN，子进程成功开库（正是 10-21 事故要拦的那一步）。
	if !db.closed.CompareAndSwap(false, true) {
		return nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	// D-02（2026-10-22 审计）：conn.Close() 失败时也必须释放锁并清空 conn。
	// 旧实现错误路径直接 return，而 closed 已置 true ⇒ 再次 Close 直接返回 nil，
	// 引用计数永久泄漏、锁永不释放（比不修更糟）。
	var closeErr error
	if db.conn != nil {
		closeErr = db.conn.Close()
		db.conn = nil
		if closeErr != nil {
			log.WithFields(log.Fields{"path": db.path, "error": closeErr.Error()}).Warn("SQLite close returned error")
		} else {
			log.WithFields(log.Fields{"path": db.path, "caller": callerTag(1)}).Info("SQLite database closed")
		}
		db.logWALState("close")
	}
	// 释放进程级独占锁：必须在 conn.Close() 之后（checkpoint 已做完），
	// 且无论 Close 成败都要释放。
	releaseDBLock(db.path)
	db.lockFile = nil
	if closeErr != nil {
		return fmt.Errorf("close database: %w", closeErr)
	}
	return nil
}

// callerTag renders "file.go:line" for the caller `skip` frames up the stack.
// Used to record WHO opened/closed the database: the 2026-09-17 incident showed
// the set of callers matters — a second opener/closer of the same file is
// enough to reset the WAL of a connection that is still writing.
func callerTag(skip int) string {
	if _, file, line, ok := runtime.Caller(skip + 1); ok {
		return fmt.Sprintf("%s:%d", filepath.Base(file), line)
	}
	return "?"
}

// walAnomaly classifies the current WAL observation against the previous one:
//
//	"missing"  — a WAL existed at the previous observation and the path is now
//	             GONE while the connection is still open. This is the primary
//	             signature: the file was unlinked, so every frame written into it
//	             afterwards lives in a file with no name left (acknowledged to
//	             callers, unreachable to everyone else, lost when the process
//	             dies). It cannot be fooled by filesystem inode reuse.
//	"replaced" — the path holds a DIFFERENT file than before (os.SameFile).
//	             Best effort only: a recycled inode can hide this case, which is
//	             why "missing" carries the weight.
//	""         — nothing anomalous (first observation, or the same file).
func walAnomaly(prevInfo os.FileInfo, curPresent bool, curInfo os.FileInfo) string {
	if prevInfo != nil && !curPresent {
		return "missing"
	}
	if prevInfo != nil && curPresent && curInfo != nil && !os.SameFile(prevInfo, curInfo) {
		return "replaced"
	}
	return ""
}

// logWALState records and logs the state of the <db>-wal file (presence, size,
// mtime, and whether it disappeared/was replaced since the last observation).
// Log-only: it changes no behaviour and is safe to call from any goroutine.
func (db *DB) logWALState(where string) {
	if db == nil || db.path == "" || db.path == ":memory:" {
		return
	}
	fi, statErr := os.Stat(db.path + "-wal")
	present := statErr == nil && fi != nil

	db.walMu.Lock()
	prevInfo, prevSize := db.lastWALInfo, db.lastWALSize
	anomaly := walAnomaly(prevInfo, present, fi)
	db.lastWALInfo, db.lastWALSize = fi, 0
	if present {
		db.lastWALSize = fi.Size()
	}
	db.walMu.Unlock()

	fields := log.Fields{
		"path":          db.path,
		"where":         where,
		"wal_present":   present,
		"wal_prev_size": prevSize,
	}
	if present {
		fields["wal_size"] = fi.Size()
		fields["wal_mtime"] = fi.ModTime().Format(time.RFC3339)
	}
	switch anomaly {
	case "missing":
		log.WithFields(fields).Warn("WAL file DISAPPEARED while this connection is open — frames written after the unlink are unreachable (the 2026-09-17 committed-data-loss signature)")
		return
	case "replaced":
		log.WithFields(fields).Warn("WAL file was REPLACED (different file at the same path) — frames written to the previous WAL file are unreachable")
		return
	}
	if !present {
		log.WithFields(fields).Info("WAL file absent")
		return
	}
	log.WithFields(fields).Info("WAL state")
}

// CheckpointForShutdown copies every committed WAL frame into the main database
// file and resets the WAL.
//
// Call it on SIGTERM: AFTER the recovery markers have been written (they must
// land in the main file too) and AFTER the agent loops were cancelled, but
// BEFORE the teardown steps that can hang (webhook / dispatcher / plugins). A
// SIGKILL that arrives later (supervisor stopwaitsecs, OOM killer, kill -9)
// then can no longer lose data that was already acknowledged — it is in the
// main database file, not only in the WAL.
//
// Best effort by design: bounded by the connection's busy_timeout (10s), one
// retry, non-fatal for the caller, and the outcome is logged either way.
func (db *DB) CheckpointForShutdown() error {
	if db == nil {
		return nil
	}
	db.mu.RLock()
	conn, path := db.conn, db.path
	db.mu.RUnlock()
	if conn == nil {
		return nil
	}
	start := time.Now()
	var lastBusy, lastLog, lastMoved int
	for attempt := 1; attempt <= 2; attempt++ {
		busy, logFrames, moved, err := walCheckpointTruncate(conn)
		if err != nil {
			log.WithError(err).WithFields(log.Fields{"path": path, "attempt": attempt}).
				Warn("wal_checkpoint(TRUNCATE) failed during shutdown")
			return err
		}
		lastBusy, lastLog, lastMoved = busy, logFrames, moved
		log.WithFields(log.Fields{
			"path": path, "attempt": attempt, "busy": busy,
			"wal_frames": logFrames, "checkpointed": moved,
			"elapsed_ms": time.Since(start).Milliseconds(),
		}).Info("WAL checkpoint on shutdown")
		if busy == 0 {
			db.logWALState("checkpoint(TRUNCATE)")
			return nil
		}
		if attempt == 1 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	db.logWALState("checkpoint(TRUNCATE)")
	return fmt.Errorf("wal_checkpoint(TRUNCATE) still busy (busy=%d log=%d checkpointed=%d) — a reader may still be active", lastBusy, lastLog, lastMoved)
}

// walCheckpointTruncate runs PRAGMA wal_checkpoint(TRUNCATE) and returns the
// (busy, log, checkpointed) counters SQLite reports for it.
func walCheckpointTruncate(conn *sql.DB) (busy, logFrames, checkpointed int, err error) {
	if scanErr := conn.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); scanErr != nil {
		// Some driver/path combinations do not return a row for an admin
		// pragma: fall back to Exec, which still performs the checkpoint.
		if _, execErr := conn.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); execErr != nil {
			return 0, 0, 0, fmt.Errorf("wal_checkpoint(TRUNCATE): %w", execErr)
		}
		return 0, 0, 0, nil
	}
	return busy, logFrames, checkpointed, nil
}

// Conn returns the underlying database connection
func (db *DB) Conn() *sql.DB {
	db.mu.RLock()
	defer db.mu.RUnlock()
	return db.conn
}

// initSchema creates the database schema if it doesn't exist, and runs migrations
func (db *DB) initSchema() error {
	conn := db.Conn()

	// Check if schema already exists by checking tenants table
	var tableName string
	err := conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='tenants'").Scan(&tableName)
	if err == sql.ErrNoRows {
		if err := db.createSchema(); err != nil {
			return err
		}
		// createSchema creates the full schema at the current schemaVersion.
		// No migrations needed for fresh databases.
		return nil
	}
	if err != nil {
		return fmt.Errorf("check schema: %w", err)
	}

	// Schema exists — check version and run migrations
	var version int
	err = conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		version = 1
	}

	// ── 数据安全（2026-09-16 用户要求：「我主要是想防止清除已有数据」）────────────
	// 既有库走迁移是**写主库**的操作，而迁移里含会删行的迁移（如 orphan 清理）。这里的
	// 保护全部是**非阻塞**的（铁律：启动路径永不阻塞 —— xbot-server 由 supervisor 托管，
	// 一旦返回错误就会「启动失败 → 自动重启 → 再失败」崩溃循环）：
	//   ① version > schemaVersion（旧二进制配新库）⇒ 只 WARN，**不跑任何迁移**（既有数据
	//      原样不动），照常启动；
	//   ② version < schemaVersion ⇒ **尽力**先整库备份（VACUUM INTO
	//      <db>.pre-v<from>-<UTC>.bak，与仓库既有迁移的备份约定一致）；备份失败只 WARN，
	//      **照常迁移、照常启动**。
	if version > schemaVersion {
		log.WithFields(log.Fields{"db_schema": version, "binary_schema": schemaVersion}).
			Warn("Stored schema version is NEWER than this binary supports; no migrations will run and existing data is left untouched")
		return nil
	}
	if version == schemaVersion {
		// 已有库也要补齐「后来才加入 schema 的表」：createSchema 只在建库时跑一次，
		// 迁移链只在版本变化时跑，两者都覆盖不到「同版本但缺某张表」的库
		// （真实案例：oauth_tokens 曾由 oauth 包惰性建表，从未启用过 OAuth 的库
		// 就没有这张表；改用存储层后没人建它 ⇒ 写入直接报 no such table）。
		return db.ensureAdditiveTables()
	}
	if db.path != "" && db.path != ":memory:" {
		// **确定性命名 + 已存在即跳过**：每个 from-version 最多留一份整库副本，
		// 迁移重试 / 崩溃重启都不会反复复制（用户 2026-09-16：库有 2.5GB，
		// 「迁移一次备份一次」太浪费空间）。命名与仓库既有约定一致：
		// migrateV61ToV62 → <db>.pre-v62.bak。
		backup := fmt.Sprintf("%s.pre-v%d.bak", db.path, version)
		if _, statErr := os.Stat(backup); os.IsNotExist(statErr) {
			escaped := strings.ReplaceAll(backup, "'", "''")
			if _, err := conn.Exec("VACUUM INTO '" + escaped + "'"); err != nil {
				log.WithFields(log.Fields{"path": db.path, "from_version": version, "reason": err.Error()}).
					Warn("Pre-migration backup FAILED (disk space?) — continuing with the migration; startup must never be blocked")
			} else {
				log.WithFields(log.Fields{"backup": backup, "from_version": version, "to_version": schemaVersion}).
					Info("Existing database backed up before schema migration (data safety)")
			}
		} else {
			log.WithField("backup", backup).
				Info("Pre-migration backup already exists for this schema version; reusing it (no extra copy)")
		}
	}
	return db.migrateSchema(version)
}

// ensureAdditiveTables creates tables that were added to the schema after a
// database was first created, without bumping schema_version.
//
// Why this exists: createSchema only runs on a brand-new database and the
// migration chain only runs when the version differs. A database already at the
// current version therefore never receives a newly-added table. That is not
// theoretical — oauth_tokens used to be created lazily by the OAuth package, so a
// database where OAuth was never enabled lacks it, and the backend-neutral token
// store then fails with "no such table: oauth_tokens".
//
// Every statement must be CREATE ... IF NOT EXISTS so this stays a no-op on
// databases that already have everything.
func (db *DB) ensureAdditiveTables() error {
	additive := `
CREATE TABLE IF NOT EXISTS oauth_tokens (
    provider TEXT NOT NULL,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    expires_at INTEGER NOT NULL,
    scopes TEXT,
    raw TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (provider, channel, chat_id)
);
CREATE INDEX IF NOT EXISTS oauth_tokens_expires_at ON oauth_tokens(expires_at);
`
	if _, err := db.Conn().Exec(additive); err != nil {
		return fmt.Errorf("ensure additive tables: %w", err)
	}
	return nil
}

// canonicalTimestamp 是**全部 session_messages / iteration_history created_at 写点
// 的唯一出口**（时间表示铁律，2026-10-14）：一律经 `.Local()` 重投影再 Format ——
//   - host TZ 非 UTC：恒产出 "+08:00" 类**带偏移**形态 ⇒ 读侧 ParseTimestamp case 1
//     精确解析，绝不进入 case 2 的「Z 串按本地猜」歧义；
//   - host TZ = UTC：产出 "Z" 形态，但此时 time.Local ≡ UTC、case 2 的本地猜 ≡
//     字面 UTC —— **构造上无害**（错位只在「写 Z 串 & 读侧 local≠UTC」时发生，
//     本写法让该组合不可能出现）。
//
// ⛔ 不许任何写点绕过本函数直接 `ts.Format(time.RFC3339*)` 落这两张表
// （判据：TestIterationCreatedAt_CanonicalWriteAndRoundTrip + TestGetSyntheticPairRowsByTurn
// 双时区跑 —— 后者在规范化前于 TZ=Asia/Shanghai 必红，因为 UTC 时区的时间值会
// 被写成 "Z" 串、被读侧 +8h 猜错）。
func canonicalTimestamp(ts time.Time) string {
	return ts.Local().Format(time.RFC3339Nano)
}

// parseSQLiteTime parses a time string from SQLite into time.Time.
// Delegates to internal.ParseTimestamp which correctly handles timezone
// interpretation for values stored by the modernc.org/sqlite driver.
func parseSQLiteTime(s string) time.Time {
	return internal.ParseTimestamp(s)
}

// parseIterationTime parses **iteration_history.created_at ONLY** —— 表级形态
// 契约见 internal.ParseIterationTimestamp 长注释（⛔ 不得挪用于其他表：
// session_messages 的老裸串是本地墙钟，与本表语义**相反**）。
//
// 未知格式一律 ERROR + 零值（fail-safe）：下游 buildTurnSpans 跳过该行 /
// compactionIteration 回落 standalone 压缩标记行（信息不丢、绝不静默错位）。
//
// ⛔ 本表禁止对 created_at 做**字符串排序**：裸串形态混用本地偏移（见 ParseTimestamp
// 注释），DST 切换处字符串序可能与真实时间序相反。只允许解析成 time.Time 后比较。
var iterationParseErrCount atomic.Int64

func parseIterationTime(s string) time.Time {
	t, err := internal.ParseIterationTimestamp(s)
	if err != nil {
		// P2 2026-10-18：坏行批量存在时旧实现每行一条 ERROR ⇒ 历史读放大成日志风暴。
		// 保留前 3 条 + 每 100 条的汇总（带 count），仍可定位且不刷屏。
		n := iterationParseErrCount.Add(1)
		if n <= 3 || n%100 == 0 {
			log.WithFields(log.Fields{
				"table": "iteration_history", "created_at": s, "count": n,
			}).Error("created_at 无法解析 —— 按无时间戳处理；压缩锚点对该行将 fail-safe 回落")
		}
	}
	return t
}
