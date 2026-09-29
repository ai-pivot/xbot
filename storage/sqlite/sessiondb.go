package sqlite

import (
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
const sessionSchemaVersion = 1

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

CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY
);
INSERT OR REPLACE INTO schema_version (version) VALUES (%d);
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
