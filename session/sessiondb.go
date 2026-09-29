package session

// sessiondb.go — 每会话一个 DB（one session, one DB）的会话库池与惰性迁移。
//
// 架构（2026-09-28 设计）：
//   - 主库（xbot.db）：tenants 注册表（db_path/migrated/preview 列）+ 全局/用户级
//     数据（subscriptions/settings/cron/usage/...）+ 旧 session_messages（迁移源，
//     迁移后只读保留）。
//   - 会话库（sessions/<channel>/<bucket>/<name>.db）：session_messages +
//     iteration_history（tenant_id 列保留、值恒定 —— 全部 WHERE tenant_id = ? 查询
//     代码零改动）。每库独立 writeMu + WAL —— 不同会话的写真正并行。
//
// 并发模型（用户硬性要求：支持任意数量并发会话）：
//   - **无 LRU 上限** —— 活跃会话的库永不关闭。池的生命周期 = TenantSession
//     缓存生命周期：TenantSession 驱逐（24h 空闲）→ 关库；DestroySession → 关库
//     + 删文件；停机 → CloseAll（逐库 checkpoint）。
//   - 孤儿清扫：不经 TenantSession 打开的库（SessionServiceFor 直连路径，如
//     usage 查询）由 5 分钟周期清扫关闭（无缓存 TenantSession 且 1h 无访问）。
//   - 打开+迁移在 sessionDBMu 下串行（打开是每会话一次的低频操作；GetOrCreateSession
//     的慢路径本就持有 m.mu 全局串行，不引入新的阻塞面）。
//
// 惰性迁移（migrated=0 → 1）：
//   ATTACH 主库到会话库连接 → 单事务 DELETE+INSERT（幂等：迁移中断重跑安全；
//   DELETE 先清空目标，崩溃在 COMMIT 前则事务回滚，崩溃在 COMMIT 后、置 migrated=1
//   前则重跑 = 再清空再复制，结果一致）→ 置 migrated=1（主库）。**显式列名**复制
//   （不用 SELECT *）—— 主库经 v1→v71 迁移链的物理列序与 createSchema 不同
//   （ALTER TABLE ADD COLUMN 追加到尾部），SELECT * 按列位复制会错位。
//
// preview 回填：迁移后从会话库算最新一条 user/assistant 消息（与 ListUserChats
// 旧子查询同语义）写主库 tenants.preview —— 拆库后主库没有消息数据，跨会话列表
// （侧栏）读这一列。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	log "xbot/logger"

	"xbot/storage/sqlite"
)

// sessionDBEntry 是池条目：打开的会话库 + 最后访问时间（孤儿清扫用）。
type sessionDBEntry struct {
	db         *sqlite.DB
	lastAccess time.Time
}

// sessionDBIdleSweep 是孤儿清扫阈值：不经 TenantSession 打开的库（SessionServiceFor
// 直连路径），超过该时长无访问且无缓存 TenantSession → 关闭。活跃会话不受影响
// （它们的库由 TenantSession 驱逐路径管理，且访问会刷新 lastAccess）。
const sessionDBIdleSweep = time.Hour

// sessionDB 返回 tenantID 的会话库（打开 + 惰性迁移 + 入池）。池命中直接返回；
// 未命中则解析注册表（tenants.db_path，空则派生并持久化）→ 打开 → 迁移（若
// migrated=0）→ 入池。整个打开+迁移在 sessionDBMu 下串行（无双重打开/双重迁移）。
//
// 锁序：m.mu → sessionDBMu（GetOrCreateSession 持 m.mu 调这里；DestroySession 同序）。
// 绝不允许反向（sessionDBMu 内获取 m.mu 会死锁）。
func (m *MultiTenantSession) sessionDB(tenantID int64) (*sqlite.DB, error) {
	m.sessionDBMu.Lock()
	defer m.sessionDBMu.Unlock()
	if entry, ok := m.sessionDBs[tenantID]; ok {
		entry.lastAccess = time.Now()
		return entry.db, nil
	}

	// 解析注册表：channel/chatID（派生路径用）+ db_path/migrated。
	channel, chatID, err := m.tenantSvc.GetTenantInfo(tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant %d: %w", tenantID, err)
	}
	info, err := m.tenantSvc.GetTenantDBInfo(tenantID)
	if err != nil {
		return nil, fmt.Errorf("read tenant %d db info: %w", tenantID, err)
	}
	dbPath := info.DBPath
	if dbPath == "" {
		// 首次分配：派生 + 持久化（注册表是唯一权威 —— 之后只读这一列）。
		dbPath = sqlite.SessionDBRelPath(channel, chatID)
		if err := m.tenantSvc.SetTenantDBPath(tenantID, dbPath); err != nil {
			return nil, fmt.Errorf("assign session db path for tenant %d: %w", tenantID, err)
		}
	}

	sdb, err := sqlite.OpenSessionDB(filepath.Join(m.sessionDBDir, dbPath))
	if err != nil {
		return nil, fmt.Errorf("open session db (tenant %d, path %s): %w", tenantID, dbPath, err)
	}

	if !info.Migrated {
		// 惰性迁移：主库 session_messages/iteration_history → 会话库（幂等）。
		// 此刻该库尚未入池（sessionDBMu 持有），无并发写者 —— 迁移事务经 pinned
		// 连接直写是安全的（无需会话库 writeMu；writeMu 纪律保护的是池内共享期）。
		if err := m.migrateSessionDB(sdb, tenantID); err != nil {
			_ = sdb.Close()
			return nil, fmt.Errorf("migrate session db (tenant %d): %w", tenantID, err)
		}
		// preview 回填：从会话库算最新一条 user/assistant 消息写主库 tenants.preview。
		m.recomputeSessionPreview(sdb, tenantID)
		// 置 migrated=1（最后一步 —— 它是「重跑迁移会清空会话库写入」的闸门：
		// migrated=1 后永不重跑；此前重跑 = DELETE+INSERT 幂等）。
		if err := m.tenantSvc.SetTenantMigrated(tenantID); err != nil {
			_ = sdb.Close()
			return nil, fmt.Errorf("mark tenant %d migrated: %w", tenantID, err)
		}
		log.WithFields(log.Fields{"tenant_id": tenantID, "db_path": dbPath}).Info("Session DB migrated from main DB")
	}

	m.sessionDBs[tenantID] = &sessionDBEntry{db: sdb, lastAccess: time.Now()}
	return sdb, nil
}

// migrateSessionDB 把主库中该 tenant 的 session_messages + iteration_history
// 复制进会话库（单事务，幂等）。ATTACH 主库到会话库的 pinned 连接上执行
// INSERT INTO ... SELECT（显式列名 —— 主库迁移链的物理列序与 createSchema 不同，
// SELECT * 按列位复制会错位）。
func (m *MultiTenantSession) migrateSessionDB(sdb *sqlite.DB, tenantID int64) error {
	ctx := context.Background()
	pinned, err := sdb.Conn().Conn(ctx)
	if err != nil {
		return fmt.Errorf("pin session db connection: %w", err)
	}
	defer pinned.Close()

	// ATTACH 主库（只读源）。路径转义：单引号翻倍（SQL 字符串字面量）。
	escaped := strings.ReplaceAll(m.dbPath, "'", "''")
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

	// 幂等：先清空目标（迁移中断重跑 = 再清空再复制，结果一致）。
	if _, err := tx.ExecContext(ctx, "DELETE FROM session_messages WHERE tenant_id = ?", tenantID); err != nil {
		return fmt.Errorf("clear session_messages: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM iteration_history WHERE tenant_id = ?", tenantID); err != nil {
		return fmt.Errorf("clear iteration_history: %w", err)
	}
	// 显式列名复制（列序契约：与 sqlite.sessionSchema 逐列一致）。
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
		// COMMIT 已成功 —— DETACH 失败只影响连接复用（该连接归还池时仍挂着
		// maindb；查询不带 maindb 前缀不受影响）。记录并继续。
		log.WithError(err).Warn("session db migration: DETACH main db failed (committed; connection returns to pool with stale attach)")
	}
	detached = true
	return nil
}

// recomputeSessionPreview 从会话库重算 preview（最新一条 user/assistant 非展示消息，
// substr 256 —— 与 ListUserChats 旧子查询同语义）写主库 tenants.preview。
// 用于：迁移回填、rewind（截断后最新消息变化）、clear（清空）。
func (m *MultiTenantSession) recomputeSessionPreview(sdb *sqlite.DB, tenantID int64) {
	var preview sql.NullString
	err := sdb.Conn().QueryRow(`
		SELECT substr(content, 1, 256) FROM session_messages
		WHERE tenant_id = ? AND role IN ('user', 'assistant') AND COALESCE(display_only, 0) = 0
		ORDER BY id DESC LIMIT 1`, tenantID).Scan(&preview)
	if err != nil && err != sql.ErrNoRows {
		log.WithError(err).WithField("tenant_id", tenantID).Warn("recompute session preview: query failed")
		return
	}
	value := ""
	if preview.Valid {
		value = preview.String
	}
	if err := m.tenantSvc.SetTenantPreview(tenantID, value); err != nil {
		log.WithError(err).WithField("tenant_id", tenantID).Warn("recompute session preview: update failed")
	}
}

// SessionServiceFor 返回绑定 tenantID 会话库的 SessionService（直接构造点收口：
// rpc contextUsage / usage stats / rewind 等只有 tenantID、不经 TenantSession 的路径）。
// 打开（含惰性迁移）经 sessionDB 池 —— 与 TenantSession 共享同一实例。
func (m *MultiTenantSession) SessionServiceFor(tenantID int64) (*sqlite.SessionService, error) {
	db, err := m.sessionDB(tenantID)
	if err != nil {
		return nil, err
	}
	return sqlite.NewSessionService(db), nil
}

// SessionDBFor 返回 tenantID 的会话库（测试与诊断用：往会话库注入触发器 /
// 断言会话库数据 —— v71 拆库后 mt.DB() 是主库，session_messages/iteration_history
// 断言必须走这里）。生产代码用 SessionServiceFor（消息操作）或 TenantSession。
func (m *MultiTenantSession) SessionDBFor(tenantID int64) (*sqlite.DB, error) {
	return m.sessionDB(tenantID)
}

// ResetSessionContextTokens 清零会话最新一条用户消息的 context_tokens（模型切换时
// 重置 token 基线 —— 不同模型的上下文大小不同，旧基线会误导 usage 显示与压缩触发）。
// v71（每会话一个 DB）：session_messages 在会话库 —— 经 SessionServiceFor 路由；
// tenant_state 的清零（主库）仍由 TenantService.SetTenantSubscription 完成（同一次
// 模型切换的两半，分别落在各自的库）。LLMFactory 的 sessionTokenResetter 钩子
// 在模型变更时调用本方法（见 llm_factory.go 的 SetSessionLLM / SelectModel）。
func (m *MultiTenantSession) ResetSessionContextTokens(channel, chatID string) {
	tenantID, err := m.tenantSvc.GetTenantIDByChannelChatID(channel, chatID)
	if err != nil || tenantID == 0 {
		return
	}
	svc, err := m.SessionServiceFor(tenantID)
	if err != nil {
		log.WithError(err).WithFields(log.Fields{"channel": channel, "chat_id": chatID}).
			Warn("reset session context tokens: open session db failed")
		return
	}
	if err := svc.UpdateUserMessageContextTokens(tenantID, 0); err != nil {
		log.WithError(err).WithFields(log.Fields{"channel": channel, "chat_id": chatID}).
			Warn("reset session context tokens: clear failed")
	}
}

// closeSessionDB 关闭并出池一个会话库（checkpoint + Close）。用于 TenantSession
// 缓存驱逐、DestroySession、孤儿清扫。幂等（不在池内 = no-op）。
func (m *MultiTenantSession) closeSessionDB(tenantID int64) {
	m.sessionDBMu.Lock()
	entry, ok := m.sessionDBs[tenantID]
	if ok {
		delete(m.sessionDBs, tenantID)
	}
	m.sessionDBMu.Unlock()
	if !ok {
		return
	}
	// checkpoint + close（停机纪律：WAL 已提交内容落主文件；gotcha「停机必须
	// checkpoint」对每个会话库同样成立）。
	_ = entry.db.CheckpointForShutdown()
	_ = entry.db.Close()
}

// closeAllSessionDBs 关闭全部会话库（停机路径）。
func (m *MultiTenantSession) closeAllSessionDBs() {
	m.sessionDBMu.Lock()
	entries := make([]*sessionDBEntry, 0, len(m.sessionDBs))
	for id, entry := range m.sessionDBs {
		entries = append(entries, entry)
		delete(m.sessionDBs, id)
	}
	m.sessionDBMu.Unlock()
	for _, entry := range entries {
		_ = entry.db.CheckpointForShutdown()
		_ = entry.db.Close()
	}
}

// sweepIdleSessionDBs 关闭孤儿会话库：无缓存 TenantSession 且超过
// sessionDBIdleSweep 无访问的池条目（SessionServiceFor 直连路径打开的库，
// 如 usage 查询）。由 5 分钟周期清理驱动（cleanupInactiveResources）。
// 活跃会话不受影响 —— 有缓存 TenantSession 的库由驱逐路径管理（24h 空闲），
// 且任何访问都刷新 lastAccess。
//
// ⛔ 锁序（死锁修复）：**绝不持 sessionDBMu 获取 m.mu**。GetOrCreateSession
// 慢路径的锁序是 m.mu → sessionDBMu；本函数若先拿 sessionDBMu 再查
// tenantCache（m.mu.RLock）会与之形成 AB-BA 死锁（sweep 持 sessionDBMu 等
// m.mu.RLock，GetOrCreateSession 持 m.mu 等 sessionDBMu —— 双方永久互等，
// 整个会话系统挂起；sweep 每 5 分钟跑一次，必然触发）。正解：先在 m.mu.RLock
// 下快照缓存的 tenantID（立即释放），再在 sessionDBMu 下扫描 —— 两段锁不重叠。
//
// 竞态安全性（快照与扫描之间创建的 TenantSession）：sessionDB() 的 map 命中会
// 刷新 lastAccess（sessionDBMu 下），扫描看到 fresh → 跳过；若 sessionDB() 在
// 扫描的 delete 之后跑 → map miss → 重开新库，被关的旧库已出池、无人引用。
func (m *MultiTenantSession) sweepIdleSessionDBs() {
	now := time.Now()
	// 快照缓存中的 tenantID（m.mu.RLock → 立即释放；绝不带进 sessionDBMu 段）。
	m.mu.RLock()
	cached := make(map[int64]bool, len(m.tenantCache))
	for _, sess := range m.tenantCache {
		cached[sess.TenantID()] = true
	}
	m.mu.RUnlock()

	// 扫描 + 出池（sessionDBMu 下）：idle 且无缓存 TenantSession 的孤儿库。
	m.sessionDBMu.Lock()
	type closedEntry struct {
		tenantID int64
		db       *sqlite.DB
	}
	var toClose []closedEntry
	for id, entry := range m.sessionDBs {
		if now.Sub(entry.lastAccess) <= sessionDBIdleSweep {
			continue
		}
		if cached[id] {
			continue // 有缓存 TenantSession —— 交给驱逐路径（24h 空闲）
		}
		delete(m.sessionDBs, id)
		toClose = append(toClose, closedEntry{tenantID: id, db: entry.db})
	}
	m.sessionDBMu.Unlock()

	// 关闭（锁外）：出池后无人引用（并发的 sessionDB() 看到 map miss → 重开新库）。
	for _, entry := range toClose {
		log.WithField("tenant_id", entry.tenantID).Debug("Closing idle orphan session DB (no cached TenantSession)")
		_ = entry.db.CheckpointForShutdown()
		_ = entry.db.Close()
	}
}

// deleteSessionDBFile 删除会话库文件（DestroySession 用：空间立即回收）。
// 先 closeSessionDB（checkpoint + close + 出池），再删文件 + -wal/-shm。
// db_path 从注册表读取 —— 调用方必须在删除 tenants 行**之前**调用本函数。
func (m *MultiTenantSession) deleteSessionDBFile(tenantID int64) {
	m.sessionDBMu.Lock()
	entry, pooled := m.sessionDBs[tenantID]
	if pooled {
		delete(m.sessionDBs, tenantID)
	}
	// 读 db_path（注册表）—— 必须在删除 tenants 行之前（DestroySession 的调用序）。
	dbPath := ""
	if info, err := m.tenantSvc.GetTenantDBInfo(tenantID); err == nil {
		dbPath = info.DBPath
	}
	m.sessionDBMu.Unlock()
	if pooled {
		_ = entry.db.CheckpointForShutdown()
		_ = entry.db.Close()
	}
	if dbPath == "" {
		return
	}
	abs := filepath.Join(m.sessionDBDir, dbPath)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(abs + suffix); err != nil && !os.IsNotExist(err) {
			log.WithError(err).WithField("path", abs+suffix).Warn("Failed to delete session db file")
		}
	}
}
