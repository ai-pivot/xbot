package sqlite

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
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
	mu           sync.RWMutex
	historyLocks [historyLockStripes]sync.Mutex

	// WAL observability (see logWALState). Remembers the last observed
	// <db>-wal file so that a replacement (unlink + recreate) — which is how
	// committed frames can end up in a file that no longer has a name — is
	// logged instead of passing silently. Guarded by walMu.
	walMu       sync.Mutex
	lastWALInfo os.FileInfo
	lastWALSize int64
}

const schemaVersion = 69
const historyLockStripes = 64

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

	// Build DSN with pragmas that apply to ALL connections in the pool.
	// _pragma is essential when MaxOpenConns > 1: it ensures every connection
	// gets busy_timeout and journal_mode, not just the first one.
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Set connection pool settings.
	// WAL mode allows concurrent reads while a write is in progress.
	// MaxOpenConns > 1 enables Go's connection pool to serve reads from
	// idle connections even when one connection is mid-write.
	// This prevents API read queries from blocking on agent DB writes.
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)
	conn.SetConnMaxLifetime(0)

	// For non-:memory: databases, WAL/busy_timeout/foreign_keys are already
	// set via DSN _pragma. For :memory: (tests), set them here as fallback.
	if path == ":memory:" {
		if _, err := conn.Exec("PRAGMA journal_mode=WAL"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("set WAL mode: %w", err)
		}
		if _, err := conn.Exec("PRAGMA busy_timeout=10000"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("set busy_timeout: %w", err)
		}
		if _, err := conn.Exec("PRAGMA foreign_keys=ON"); err != nil {
			conn.Close()
			return nil, fmt.Errorf("enable foreign keys: %w", err)
		}
	}

	db := &DB{
		conn: conn,
		path: path,
	}

	// Initialize schema
	if err := db.initSchema(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("initialize schema: %w", err)
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
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.conn != nil {
		if err := db.conn.Close(); err != nil {
			return fmt.Errorf("close database: %w", err)
		}
		db.conn = nil
		log.WithFields(log.Fields{"path": db.path, "caller": callerTag(1)}).Info("SQLite database closed")
		db.logWALState("close")
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
		return nil
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

// parseSQLiteTime parses a time string from SQLite into time.Time.
// Delegates to internal.ParseTimestamp which correctly handles timezone
// interpretation for values stored by the modernc.org/sqlite driver.
func parseSQLiteTime(s string) time.Time {
	return internal.ParseTimestamp(s)
}
