package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "xbot/logger"
)

// sessiondb.go — 每会话一个 SQLite 库（one session, one DB）。
//
// 背景（2026-09-28 设计）：所有会话的消息记录此前共用一个主库（实测 3.4GB 且持续
// 增长），单写者（writeMu 全局串行）+ 删除不回收空间 + 迁移/备份代价随全库线性
// 增长。拆分后：session_messages + iteration_history 落进每会话独立文件
// （<主库目录>/sessions/<channel>/<bucket>/<name>.db），主库只保留注册表（tenants，
// 含 db_path/migrated/preview 列）与全局/用户级数据。
//
// 会话库 schema 是主库的**子集**（session_messages + iteration_history + 索引），
// tenant_id 列保留（值恒为该会话的 tenant_id）——所有 `WHERE tenant_id = ?` 查询
// 代码零改动。FK 指向 tenants 表的子句省略（会话库没有 tenants 表；删除路径是
// 显式 DELETE，不依赖 FK 级联）。
//
// 并发模型：每个会话库是独立的 *DB 实例（独立 writeMu + WAL）——不同会话的写
// 真正并行（这是拆分的核心收益之一）。活跃会话的库**永不关闭**（无 LRU 上限，
// 用户要求支持任意数量并发会话）；只有 TenantSession 缓存驱逐（24h 空闲）、
// 显式销毁、停机时才 checkpoint + close。

// sessionSchemaVersion 是会话库自己的 schema 版本（与主库 schemaVersion 完全独立
// 的命名空间——会话库迁移只重写几 MB 的会话文件，永不触碰主库版本链）。
//
// 版本历史：
//   - 1: v71 拆库时的初始会话库 schema。
//   - 2: 新增复合索引 idx_iter_history_turn_iter(tenant_id, turn_id, iteration)，
//     支撑区域段取回（`iteration < ? ORDER BY iteration`）与
//     (turn_id, iteration) 详情单查。**该变更不需要迁移链**：initSessionSchema
//     每次打开会话库都重放整份幂等 DDL（CREATE ... IF NOT EXISTS），新库与既有库
//     在同一收口点收敛；版本号只记录 schema 修订，不参与分支判断。
const sessionSchemaVersion = 2

// sessionSchema 是会话库的完整 DDL（主库 schema 的子集，FK 省略）。
// 与 schema.go 的 session_messages/iteration_history 列定义保持逐列一致——
// 惰性迁移用 INSERT INTO ... SELECT * 按列位复制，两边列序必须相同。
// （var 而非 const：版本号经 fmt.Sprintf 注入，schema_version_test 守护一致性。）
var sessionSchema = fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS session_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    tool_call_id TEXT,
    tool_name TEXT,
    tool_arguments TEXT,
    tool_calls TEXT,
    detail TEXT,
    reasoning_content TEXT DEFAULT '',
    reasoning_items TEXT DEFAULT '',
    display_only INTEGER DEFAULT 0,
    internal_only INTEGER DEFAULT 0,
    context_tokens INTEGER DEFAULT 0,
    turn_id INTEGER DEFAULT 0,
    record_type TEXT NOT NULL DEFAULT 'message',
    target_history_id INTEGER,
    record_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_session_messages_tenant_created ON session_messages(tenant_id, created_at);
CREATE INDEX IF NOT EXISTS idx_session_messages_tenant_history ON session_messages(tenant_id, id);
CREATE INDEX IF NOT EXISTS idx_sm_tenant_record ON session_messages(tenant_id, record_type, target_history_id) WHERE record_type != 'message';
CREATE INDEX IF NOT EXISTS idx_sm_tenant_role_id ON session_messages(tenant_id, role, id) WHERE role IN ('user','assistant');

CREATE TABLE IF NOT EXISTS iteration_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL DEFAULT 0,
    tenant_id INTEGER NOT NULL,
    turn_id INTEGER NOT NULL DEFAULT 0,
    iteration INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    reasoning TEXT NOT NULL DEFAULT '',
    tools TEXT NOT NULL DEFAULT '[]',
    tokens INTEGER NOT NULL DEFAULT 0,
    ttft_ms INTEGER NOT NULL DEFAULT 0,
    tokens_per_sec INTEGER NOT NULL DEFAULT 0,
    total_ms INTEGER NOT NULL DEFAULT 0,
    tpot_ms INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    subscription_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id);
CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id);
-- v2: 复合索引把 (tenant_id, turn_id) 的等值定位延伸到 iteration 范围 ——
-- 支撑 /api/regions 的区域段取回（iteration < ? ORDER BY iteration）与
-- /api/iteration_detail 的 (turn_id, iteration) 单查。旧的 idx_iter_history_turn
-- 只到 turn_id，范围查询要在 turn 内全扫。
CREATE INDEX IF NOT EXISTS idx_iter_history_turn_iter ON iteration_history(tenant_id, turn_id, iteration);

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);
-- 版本表是**单行哨兵**（主库同语义：一行 = 当前版本）。旧的
-- INSERT OR REPLACE 只在主键冲突时替换 —— 版本从 1 升到 2 会留下两行
-- （{1,2}），SELECT version ... LIMIT 1 就可能读到陈旧值。先清空再写入，
-- 保证升级后读到的永远是当前版本（幂等：重复打开结果一致）。
DELETE FROM schema_version;
INSERT INTO schema_version (version) VALUES (%d);
`, sessionSchemaVersion)

// OpenSessionDB 打开（或创建）一个会话库。连接设置与主库 Open 相同（WAL +
// busy_timeout + foreign_keys），但连接池按会话库的访问模式调优：
// MaxIdleConns(1) + ConnMaxIdleTime(5min) —— 空闲会话只占 1 个连接，5 分钟无
// 查询后连接归还（fd 预算与活跃会话数解耦）；活跃会话最多 4 个并发查询（与主库
// 一致）。无 LRU 上限：活跃会话的库永不关闭（用户要求任意数量并发会话）。
func OpenSessionDB(path string) (*DB, error) {
	db, err := openSQLite(path, 1, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	if err := db.initSessionSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init session schema: %w", err)
	}
	log.WithFields(log.Fields{"path": path, "caller": callerTag(1)}).Debug("Session database opened")
	return db, nil
}

// openSQLite 是 Open / OpenSessionDB 共用的连接建立逻辑（目录创建 + DSN pragma +
// 连接池）。maxIdleConns / maxIdleTime 由调用方按访问模式调优（主库 4/0，会话库
// 1/5min）。数据安全告警（0 字节 / 非 SQLite 头 / 同级备份）只在主库 Open 报——
// 会话库由宿主管理生命周期（删除即删文件），不需要那套启动告警。
func openSQLite(path string, maxIdleConns int, maxIdleTime time.Duration) (*DB, error) {
	if path != ":memory:" {
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	dsn := path
	if path != ":memory:" {
		dsn = "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)"
	}
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(maxIdleConns)
	conn.SetConnMaxLifetime(0)
	if maxIdleTime > 0 {
		conn.SetConnMaxIdleTime(maxIdleTime)
	}
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
	db := &DB{conn: conn, path: path}
	db.logWALState("open")
	return db, nil
}

// initSessionSchema 创建会话库 schema（幂等：CREATE TABLE IF NOT EXISTS）。
// 会话库版本独立（sessionSchemaVersion），不做迁移链——schema 变更时 bump
// 版本号 + 在此处按版本升级（会话库小，迁移代价低）。
func (db *DB) initSessionSchema() error {
	if _, err := db.Conn().Exec(sessionSchema); err != nil {
		return fmt.Errorf("create session schema: %w", err)
	}
	return nil
}

// SessionDBRelPath 派生一个会话库的**相对**路径（相对主库所在目录）：
// sessions/<channel>/<bucket>/<name>.db
//
//   - bucket = sha256(channel:chatID) 前 2 hex（256 桶/渠道，避免单目录文件数爆炸）；
//   - name = 净化后的 chatID（截断 64）+ "-" + sha256 前 12 hex（保证唯一——
//     chatID 可能含路径分隔符/中文/超长，净化后冲突由 hash 后缀兜底）；
//   - channel 也做净化（渠道名是固定集合，但防御性处理）。
//
// 绝对路径由调用方 Join(主库目录, relPath)。tenants.db_path 存这个相对路径
// （注册表是唯一权威——不做路径反推，重命名/迁移只改注册表）。
func SessionDBRelPath(channel, chatID string) string {
	sum := sha256.Sum256([]byte(channel + ":" + chatID))
	bucket := fmt.Sprintf("%02x", sum[0])
	name := sanitizePathSegment(chatID, 64) + "-" + fmt.Sprintf("%x", sum[:6]) + ".db"
	return filepath.Join("sessions", sanitizePathSegment(channel, 32), bucket, name)
}

// sanitizePathSegment 把任意字符串变成文件系统安全的路径段：只保留字母数字、
// 点、下划线、连字符，其余替换为 '_'，并截断到 maxRunes。
func sanitizePathSegment(s string, maxRunes int) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		out = "_"
	}
	if runes := []rune(out); len(runes) > maxRunes {
		out = string(runes[:maxRunes])
	}
	return out
}

// CopyTenantDataFromMainDB copies a tenant's session_messages/iteration_history
// from the main DB (at mainDBPath) into this session DB, in a single
// transaction (DELETE+INSERT, idempotent).
//
// Shared core (one implementation for both paths): the lazy migration
// (session/sessiondb.go) and the v72 bulk-completion migration
// (migrations.go migrateV71ToV72) both funnel through this method — never
// duplicate the copy SQL.
//
// Explicit column lists (no SELECT *): the main DB's physical column order
// differs between migration-chain DBs (ALTER TABLE ADD COLUMN appends to the
// tail) and fresh createSchema DBs — positional copying would misalign
// columns. The column list is the contract with sessionSchema (same order).
//
// ⛔ Re-run contract (data-loss guard): only safe to re-run while
// tenants.migrated=0 — the DELETE clears the session DB before re-inserting,
// which would wipe post-migration writes. Callers MUST set migrated=1 BEFORE
// deleting the tenant's main-DB rows: the inverse order (delete-then-flag)
// with a crash in between leaves the tenant unmigrated with EMPTY main-DB
// rows, and the next migration re-copies from EMPTY = permanent data loss.
// Flag-first + crash in between leaves only harmless stale main-DB rows.
func (db *DB) CopyTenantDataFromMainDB(mainDBPath string, tenantID int64) error {
	ctx := context.Background()
	pinned, err := db.Conn().Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin session db connection: %w", err)
	}
	defer pinned.Close()

	// ATTACH the main DB (copy source). Quote-escape: single quotes doubled
	// (SQL string literal).
	escaped := strings.ReplaceAll(mainDBPath, "'", "''")
	if _, err := pinned.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE '%s' AS maindb", escaped)); err != nil {
		return fmt.Errorf("attach main db: %w", err)
	}
	detached := false
	defer func() {
		if !detached {
			_, _ = pinned.ExecContext(ctx, "DETACH DATABASE maindb")
		}
	}()

	tx, err := pinned.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer tx.Rollback()

	// Idempotent: clear the target first (interrupted migration re-run =
	// re-clear + re-copy, same result).
	if _, err := tx.ExecContext(ctx, "DELETE FROM session_messages WHERE tenant_id = ?", tenantID); err != nil {
		return fmt.Errorf("clear session_messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM iteration_history WHERE tenant_id = ?", tenantID); err != nil {
		return fmt.Errorf("clear iteration_history: %w", err)
	}
	// Copy with explicit column lists (column-order contract with
	// sessionSchema — see the method comment).
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_messages
			(id, tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls,
			 detail, reasoning_content, reasoning_items, display_only, internal_only,
			 context_tokens, turn_id, record_type, target_history_id, record_data, created_at)
		SELECT id, tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls,
			 detail, reasoning_content, reasoning_items, display_only, internal_only,
			 context_tokens, turn_id, record_type, target_history_id, record_data, created_at
		FROM maindb.session_messages WHERE tenant_id = ?`, tenantID); err != nil {
		return fmt.Errorf("copy session_messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO iteration_history
			(id, message_id, tenant_id, turn_id, iteration, content, reasoning, tools,
			 tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens,
			 model, subscription_id, created_at)
		SELECT id, message_id, tenant_id, turn_id, iteration, content, reasoning, tools,
			 tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens,
			 model, subscription_id, created_at
		FROM maindb.iteration_history WHERE tenant_id = ?`, tenantID); err != nil {
		return fmt.Errorf("copy iteration_history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	if _, err := pinned.ExecContext(ctx, "DETACH DATABASE maindb"); err != nil {
		// COMMIT succeeded — a DETACH failure only affects connection reuse
		// (the conn returns to the pool with maindb attached; queries without
		// the maindb prefix are unaffected). Log and continue.
		log.WithError(err).Warn("session db migration: DETACH main db failed (committed; connection returns to pool with stale attach)")
	}
	detached = true
	return nil
}
