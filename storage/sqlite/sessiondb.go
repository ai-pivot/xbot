package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
//   - 3: iteration 列（合成工具对归属迭代）。
//   - 4: 群组信封列（group_id / sender_type / sender_id / sender_name）。
//   - 5: 结构化消息列（chat_id / kind / correlation_id / reply_to /
//     idempotency_key / structured_json / direction / answered / cancelled）
//     与三个索引。旧库按列幂等 ALTER 补齐。
const sessionSchemaVersion = 5

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
    iteration INTEGER NOT NULL DEFAULT 0,
    record_type TEXT NOT NULL DEFAULT 'message',
    target_history_id INTEGER,
    record_data TEXT,
    group_id TEXT NOT NULL DEFAULT '',
    sender_type TEXT NOT NULL DEFAULT '',
    sender_id TEXT NOT NULL DEFAULT '',
    sender_name TEXT NOT NULL DEFAULT '',
    -- v5: 结构化消息信封（sendmessage-structured 设计 §7.2 的会话库子集）。
    --   chat_id          会话 ID（会话库本身已按 chat 隔离；列用于幂等复合索引）
    --   kind             消息语义类型（text/ask_user/answer/report/decision/command/event）
    --   correlation_id   请求—响应关联键
    --   reply_to         直接回复的行 id（字符串形式，允许悬空）
    --   idempotency_key  发送者作用域幂等键
    --   structured_json  完整信封 JSON
    --   direction        in | out
    --   answered/cancelled  ask_user 终态（0/1）
    chat_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    structured_json TEXT NOT NULL DEFAULT '',
    direction TEXT NOT NULL DEFAULT '',
    answered INTEGER NOT NULL DEFAULT 0,
    cancelled INTEGER NOT NULL DEFAULT 0,
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
// ── 进程内按路径共享 *sql.DB（与 flock 同一套引用计数）──
//
// 为什么必须共享：Open() 在进程内被多处独立调用（serverapp 的 sharedDB /
// tokenDB、storage.MigrateMemoryToFiles、session.MultiTenant）。flock 早已按路径
// 复用（见 acquireDBLock），但 sql.DB **没有** —— 每次 Open 都新建一套连接池。
// 后果是三合一的，生产 2026-10-27 实测：
//
//	· fd 随打开的库数膨胀：2440 个 .db fd，而磁盘上只有 1358 个会话库
//	· **某个短连接 Close 时，SQLite 认为「这一连接集的最后一个连接关掉了」，
//	  checkpoint 并 unlink 掉 -wal —— 而常驻池仍持有它**。那些帧只能写进一个
//	  已经没有目录项的 inode（实测 4MB / 1015 帧，2 个池共 8 个 fd 指向它）
//	· 关停要逐个 close 这些多余连接，把停机拖到 52s
//
// 共享之后同一路径全进程只有一个 *sql.DB，上面三件事一起消失。
//
// :memory: 不参与共享 —— 每个 :memory: 库天然相互独立，共享会让不同测试
// 看到彼此的数据。
var sharedConns = struct {
	mu sync.Mutex
	m  map[string]*sharedConn
}{m: make(map[string]*sharedConn)}

// sharedConn 是「一个路径一行」的注册项。conn 经 connHolder 间接持有，
// 这样 WAL 自愈（walguard.go）能对整个进程一次性换池。
type sharedConn struct {
	holder *connHolder
	refs   int
}

// acquireSharedConn returns the process-wide connection pool for path, creating
// it on first use. Every successful call must be paired with releaseSharedConn.
func acquireSharedConn(path string, maxIdleConns int, maxIdleTime time.Duration) (*connHolder, error) {
	sharedConns.mu.Lock()
	defer sharedConns.mu.Unlock()
	if e, ok := sharedConns.m[path]; ok {
		e.refs++
		return e.holder, nil
	}
	conn, err := openRawConn(path, maxIdleConns, maxIdleTime)
	if err != nil {
		return nil, err
	}
	// 记录刚打开的 -wal/-shm 身份：WAL 守卫据此判断「路径上还是不是我那个文件」。
	walID, shmID := captureWALIdentity(path)
	h := &connHolder{db: conn, walID: walID, shmID: shmID, maxIdle: maxIdleConns, maxIdleTime: maxIdleTime}
	sharedConns.m[path] = &sharedConn{holder: h, refs: 1}
	startWALGuard()
	return h, nil
}

// releaseSharedConn drops one reference; the underlying pool is closed only when
// the last holder releases it. Returns the Close error of that final close.
func releaseSharedConn(path string) error {
	sharedConns.mu.Lock()
	defer sharedConns.mu.Unlock()
	e, ok := sharedConns.m[path]
	if !ok {
		return nil
	}
	e.refs--
	if e.refs > 0 {
		log.WithFields(log.Fields{"path": path, "refs": e.refs}).
			Debug("Database connection still referenced elsewhere; not closing the pool")
		return nil
	}
	delete(sharedConns.m, path)
	conn := e.holder.get()
	if conn == nil {
		return nil
	}
	return conn.Close()
}

// sharedConnRefs reports the current reference count for path (0 = not open).
// Used by tests, by the WAL sidecar guard (RemoveWALSidecars) and by shutdown
// diagnostics.
func sharedConnRefs(path string) int {
	sharedConns.mu.Lock()
	defer sharedConns.mu.Unlock()
	if e, ok := sharedConns.m[path]; ok {
		return e.refs
	}
	return 0
}

// openRawConn creates a fresh connection pool. It does NOT touch the shared
// registry — acquireSharedConn is the only caller.
func openRawConn(path string, maxIdleConns int, maxIdleTime time.Duration) (*sql.DB, error) {
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
	enablePersistWAL(conn)
	return conn, nil
}

func openSQLite(path string, maxIdleConns int, maxIdleTime time.Duration) (*DB, error) {
	if path == ":memory:" {
		// 每个 :memory: 库天然独立，不能共享。
		conn, err := openRawConn(path, maxIdleConns, maxIdleTime)
		if err != nil {
			return nil, err
		}
		db := &DB{conn: conn, path: path}
		db.logWALState("open")
		return db, nil
	}
	holder, err := acquireSharedConn(path, maxIdleConns, maxIdleTime)
	if err != nil {
		return nil, err
	}
	// conn 只是「打开那一刻的快照」，读写一律走 activeConn()（可能已被 WAL 守卫换成新池）。
	db := &DB{conn: holder.get(), holder: holder, path: path}
	db.logWALState("open")
	return db, nil
}

// enablePersistWAL 对本进程连接池里的**每一个物理连接**打开
// SQLITE_FCNTL_PERSIST_WAL，让连接关闭时不再 unlink 掉 -wal 文件。
//
// 2026-10-21 事故（硬证据）：serve 进程持有 xbot.db-wal 的 fd 期间，另一个短命
// 进程（python 脚本 / xbot-cli 一次性命令）打开了同一主库并正常关闭。SQLite 的
// 语义是「最后一个连接关闭时 checkpoint + unlink -wal」——那个短命进程看不到
// serve 的连接（它的 -shm 视角里 nRef==0），于是它删了 WAL。结果是 serve 之后
// 一直往**已删除的 inode** 追加（实测 4.3MB 且持续增长），任何新连接（网页、
// CLI、脚本）都读不到 serve 写的这批数据；而两边的 checkpoint 又在争抢同一批
// 数据库页 → 复现 2026-10-21 17:17 的「database disk image is malformed」。
//
// PERSIST_WAL 把 unlink 这一步整个去掉：WAL 留在磁盘上，serve 与新进程始终共享
// 同一个 -shm/-wal，不再产生孤儿 inode。
//
// 注：这是 per-connection 的 file control（不是 pragma，DSN 传不进去），所以必须
// 对池里的每个物理连接各设一次；连接池随后的复用会让设置一直有效（主库
// MaxIdleConns(4)+MaxConnLifetime(0) 常驻，不会回收重建）。
func enablePersistWAL(conn *sql.DB) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 先并发占满池子拿到**不同的**物理连接，逐一设置，最后统一归还。
	held := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		c, err := conn.Conn(ctx)
		if err != nil {
			break
		}
		held = append(held, c)
	}
	for _, c := range held {
		_ = c.Raw(func(driverConn any) error {
			fc, ok := driverConn.(interface {
				FileControlPersistWAL(dbName string, mode int) (int, error)
			})
			if !ok {
				return nil
			}
			_, err := fc.FileControlPersistWAL("main", 1)
			return err
		})
	}
	for _, c := range held {
		_ = c.Close()
	}
}

// initSessionSchema 创建会话库 schema（幂等：CREATE TABLE IF NOT EXISTS）。
// 会话库版本独立（sessionSchemaVersion），不做迁移链——schema 变更时 bump
// 版本号 + 在此处按版本升级（会话库小，迁移代价低）。
func (db *DB) initSessionSchema() error {
	if _, err := db.Conn().Exec(sessionSchema); err != nil {
		return fmt.Errorf("create session schema: %w", err)
	}
	// v3: session_messages 新增 iteration 列（合成工具对归属迭代，427ea14d）。
	// sessionSchema 用 CREATE TABLE IF NOT EXISTS——对已存在的表是 no-op，
	// 旧库（v2 及更早）缺列需幂等 ALTER TABLE 补上（columnExists 守卫）。
	exists, err := columnExists(db.Conn(), "session_messages", "iteration")
	if err != nil {
		return fmt.Errorf("check session_messages.iteration: %w", err)
	}
	if !exists {
		if _, err := db.Conn().Exec("ALTER TABLE session_messages ADD COLUMN iteration INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add session_messages.iteration: %w", err)
		}
	}
	// v4: 群组消息信封列（group_id / sender_type / sender_id / sender_name）。
	// 与 v3 同策略：旧库逐列幂等补齐；索引随会话库 DDL 的 IF NOT EXISTS 建好。
	for _, c := range []struct{ name, ddl string }{
		{"group_id", "ALTER TABLE session_messages ADD COLUMN group_id TEXT NOT NULL DEFAULT ''"},
		{"sender_type", "ALTER TABLE session_messages ADD COLUMN sender_type TEXT NOT NULL DEFAULT ''"},
		{"sender_id", "ALTER TABLE session_messages ADD COLUMN sender_id TEXT NOT NULL DEFAULT ''"},
		{"sender_name", "ALTER TABLE session_messages ADD COLUMN sender_name TEXT NOT NULL DEFAULT ''"},
	} {
		ok, err := columnExists(db.Conn(), "session_messages", c.name)
		if err != nil {
			return fmt.Errorf("check session_messages.%s: %w", c.name, err)
		}
		if !ok {
			if _, err := db.Conn().Exec(c.ddl); err != nil {
				return fmt.Errorf("add session_messages.%s: %w", c.name, err)
			}
		}
	}
	if _, err := db.Conn().Exec(`CREATE INDEX IF NOT EXISTS ix_sm_group_cursor ON session_messages(group_id, id DESC)`); err != nil {
		return fmt.Errorf("create ix_sm_group_cursor: %w", err)
	}
	// v5: 结构化消息列。与 v3/v4 同策略：旧库逐列幂等补齐（新库由上面的
	// CREATE TABLE 直接建好）。列顺序与 sessionSchema 末尾一致，便于人工核对。
	for _, c := range []struct{ name, ddl string }{
		{"chat_id", "ALTER TABLE session_messages ADD COLUMN chat_id TEXT NOT NULL DEFAULT ''"},
		{"kind", "ALTER TABLE session_messages ADD COLUMN kind TEXT NOT NULL DEFAULT ''"},
		{"correlation_id", "ALTER TABLE session_messages ADD COLUMN correlation_id TEXT NOT NULL DEFAULT ''"},
		{"reply_to", "ALTER TABLE session_messages ADD COLUMN reply_to TEXT NOT NULL DEFAULT ''"},
		{"idempotency_key", "ALTER TABLE session_messages ADD COLUMN idempotency_key TEXT NOT NULL DEFAULT ''"},
		{"structured_json", "ALTER TABLE session_messages ADD COLUMN structured_json TEXT NOT NULL DEFAULT ''"},
		{"direction", "ALTER TABLE session_messages ADD COLUMN direction TEXT NOT NULL DEFAULT ''"},
		{"answered", "ALTER TABLE session_messages ADD COLUMN answered INTEGER NOT NULL DEFAULT 0"},
		{"cancelled", "ALTER TABLE session_messages ADD COLUMN cancelled INTEGER NOT NULL DEFAULT 0"},
	} {
		ok, err := columnExists(db.Conn(), "session_messages", c.name)
		if err != nil {
			return fmt.Errorf("check session_messages.%s: %w", c.name, err)
		}
		if !ok {
			if _, err := db.Conn().Exec(c.ddl); err != nil {
				return fmt.Errorf("add session_messages.%s: %w", c.name, err)
			}
		}
	}
	// 幂等索引：同一会话 + 同一业务键只有一行。空键不参与（否则第二条空键
	// 消息会被误判为重复）。
	if _, err := db.Conn().Exec(`
		CREATE UNIQUE INDEX IF NOT EXISTS ux_sm_chat_idem
		    ON session_messages(chat_id, idempotency_key)
		    WHERE idempotency_key IS NOT NULL AND idempotency_key <> ''`); err != nil {
		return fmt.Errorf("create ux_sm_chat_idem: %w", err)
	}
	// 收件箱：未回答的 ask_user（kind, answered 前缀直接命中）。
	if _, err := db.Conn().Exec(`
		CREATE INDEX IF NOT EXISTS ix_sm_kind_answered
		    ON session_messages(kind, answered)`); err != nil {
		return fmt.Errorf("create ix_sm_kind_answered: %w", err)
	}
	// 线程视图：同一 correlation_id 的完整链路。
	if _, err := db.Conn().Exec(`
		CREATE INDEX IF NOT EXISTS ix_sm_correlation
		    ON session_messages(correlation_id)`); err != nil {
		return fmt.Errorf("create ix_sm_correlation: %w", err)
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
