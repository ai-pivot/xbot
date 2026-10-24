package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	log "xbot/logger"

	"xbot/config"
	"xbot/llm"
	"xbot/memory"
	"xbot/memory/flat"
	"xbot/memory/letta"
	"xbot/storage"
	"xbot/storage/sqlite"
	"xbot/storage/vectordb"
	"xbot/tools"
)

// sessKey builds the canonical session key from channel and chatID.
func sessKey(channel, chatID string) string {
	return channel + ":" + chatID
}

// MultiTenantOption 配置选项
type MultiTenantOption func(*MultiTenantSession)

// WithMCPTimeout 设置 MCP 不活跃超时时间
func WithMCPTimeout(timeout time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.mcpInactivityTimeout = timeout
	}
}

// WithCleanupInterval 设置清理扫描间隔
func WithCleanupInterval(interval time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.mcpCleanupInterval = interval
	}
}

// WithSessionCacheTimeout 设置会话缓存超时时间
func WithSessionCacheTimeout(timeout time.Duration) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.sessionCacheTimeout = timeout
	}
}

// WithMemoryProvider 设置记忆提供者 ("flat" 或 "letta")
func WithMemoryProvider(provider string) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.memoryProvider = provider
	}
}

// WithPersonaIsolation enables per-tenant persona isolation (no fallback to tenantID=0).
// The flag is applied through the backend-agnostic memory store, so it works on every
// storage backend.
func WithPersonaIsolation(enabled bool) MultiTenantOption {
	return func(m *MultiTenantSession) {
		if m.store != nil {
			m.store.Memory().SetPersonaIsolation(enabled)
		}
	}
}

// WithArchivalService 设置向量归档服务（Letta 模式下使用）
// 如果不设置，会在 NewMultiTenant 中根据 EmbeddingConfig 自动创建
func WithArchivalService(svc *vectordb.ArchivalService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.archivalSvc = svc
	}
}

// EmbeddingConfig 嵌入向量配置（用于自动创建归档服务）
type EmbeddingConfig struct {
	Provider   string // Embedding 提供者: "openai"(默认) 或 "ollama"
	BaseURL    string
	APIKey     string
	Model      string
	MaxTokens  int     // Maximum tokens for embedding model (default 2048)
	LLMClient  llm.LLM // LLM client for content compression (optional)
	LLMModel   string  // Model name for LLM compression (optional)
	TokenModel string  // Model name for token counting (default "gpt-4")
}

// WithEmbeddingConfig 设置嵌入向量配置，NewMultiTenant 将自动创建 chromem-go 归档服务
func WithEmbeddingConfig(cfg EmbeddingConfig) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.embeddingConfig = &cfg
	}
}

// WithToolIndexService 设置工具索引服务
func WithToolIndexService(svc *vectordb.ToolIndexService) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.toolIndexSvc = svc
	}
}

// WithDataDir 设置与存储后端无关的数据目录（chromem 归档库 / 工具索引落在此处）。
// 默认 config.XbotHome()。它存在的意义是：这些路径**绝不能**从 dbPath 推导 ——
// PG 后端下 dbPath 存的是 DSN，不是文件路径。
func WithDataDir(dir string) MultiTenantOption {
	return func(m *MultiTenantSession) {
		m.dataDir = dir
	}
}

// MultiTenantSession manages multiple tenant sessions with SQLite backing
type MultiTenantSession struct {
	// store is the backend-agnostic storage layer (SQLite adapter or PG). All
	// tenant/memory/config/session access funnels through it. The concrete
	// *sqlite.* fields below are SQLite-internal (letta deps, session DB pool,
	// legacy migration) and are nil on the PG path.
	store                storage.Storage
	db                   *sqlite.DB
	tenantSvc            *sqlite.TenantService
	sessionSvc           *sqlite.SessionService
	memorySvc            *sqlite.MemoryService
	userProfileSvc       *sqlite.UserProfileService
	tokenUsageSvc        *sqlite.UserTokenUsageService
	archivalSvc          *vectordb.ArchivalService
	toolIndexSvc         *vectordb.ToolIndexService
	recallTimeRangeFn    vectordb.RecallTimeRangeFunc // 时间范围会话历史搜索
	embeddingConfig      *EmbeddingConfig             // for auto-creating archival service
	memoryProvider       string                       // "flat" or "letta"
	mu                   sync.RWMutex
	tenantCache          map[string]*TenantSession // key: "channel:chat_id"
	dbPath               string
	dataDir              string          // backend-independent data dir (chromem stores); default config.XbotHome()
	mcpConfigPath        string          // MCP 配置文件路径
	mcpInactivityTimeout time.Duration   // MCP 不活跃超时配置
	mcpCleanupInterval   time.Duration   // MCP 清理扫描间隔
	sessionCacheTimeout  time.Duration   // 会话缓存超时配置
	cleanupStopCh        chan struct{}   // 清理协程停止信号
	cleanupWg            sync.WaitGroup  // 清理协程等待组
	cleanupStopOnce      sync.Once       // 确保 StopCleanupRoutine 只执行一次
	shutdownCtx          context.Context // cancelled on StopCleanupRoutine; used as parent for background goroutines
	shutdownCancel       context.CancelFunc
	// sessionCreateGuard decides whether a NOT-YET-EXISTING (channel, chatID)
	// may be materialized. Every read path (SSE, REST, RPC, internal) funnels
	// through GetOrCreateSession, so this single choke point is what stops a
	// client holding a DELETED chatID from resurrecting a phantom session.
	// nil = allow (backward compatible).
	sessionCreateGuard    func(channel, chatID string) bool
	toolIndexFingerprints map[int64]string          // per-tenant catalog fingerprint (guarded by mu)
	toolIndexPrevNames    map[int64]map[string]bool // per-tenant previous tool name set (guarded by mu)
	onSessionEvict        func(sessionKey string)   // 会话被清理时的回调

	// ── 每会话一个 DB（one session, one DB，v71）──────────────────────────────
	// sessionDBs 是打开的会话库池（tenantID → 会话库）。**无 LRU 上限** —— 活跃
	// 会话的库永不关闭（用户要求支持任意数量并发会话）；生命周期 = TenantSession
	// 缓存生命周期（驱逐/销毁/停机关库）+ 孤儿清扫（无缓存 TenantSession 且 1h
	// 无访问的直连打开）。见 sessiondb.go。
	sessionDBs   map[int64]*sessionDBEntry // tenantID → open session DB
	sessionDBMu  sync.Mutex                // guards sessionDBs（锁序：m.mu → sessionDBMu，绝不反向）
	sessionDBDir string                    // 会话库根目录（= 主库所在目录；db_path 相对此解析）

	// destroyedTenants 是**已销毁租户的墓碑**（tenantID 集合，独立锁，绝不与
	// sessionDBMu / m.mu 嵌套获取）。
	//
	// 为什么需要（2026-10-22 05:26 生产 panic 的铁证日志）：
	//   DestroySession → deleteSessionDBFile 关掉会话库、删掉 <db>.db/-wal/-shm，
	//   同一秒里并发路径（消息投递 / restore / 通知）又 GetOrCreateSession 把**同一个
	//   文件路径**重新 open 出来 —— 日志签名 "SQLite database closed" 紧跟
	//   "WAL state ... where=open"。那一代会话库已经在 close 语义之后被复用
	//   （use-after-close），最终把进程打成 exit status 2。
	//
	// 墓碑把这条路堵死：租户一旦销毁就**永不复用**（tenantID 是 AUTOINCREMENT，
	// 不会回收），sessionDB() 对墓碑租户直接返回明确错误，绝不重新 open 文件。
	// stale 引用因此拿到 "tenant ... was destroyed" 而不是一个半开的库。
	destroyedMu      sync.RWMutex
	destroyedTenants map[int64]struct{}
	destroyedOrder   []int64 // FIFO 淘汰顺序（墓碑只防"秒级并发"，不需要永久保留）
}

// maxDestroyedTenants 是墓碑集合的软上限。超出后按 FIFO 淘汰最旧的 id ——
// 正确性论证：能观察到某个 tenantID 的引用只存在于销毁瞬间前后（秒级），而
// 淘汰发生在其后又积累了 65536 次销毁之后，实际不可能再命中。
const maxDestroyedTenants = 65536

// errSessionDestroyed 表示目标租户的会话库已被销毁（或正在销毁）。
var errSessionDestroyed = errors.New("session was destroyed")

// markTenantDestroyed 记录"该租户已销毁"。必须在关闭/删除其会话库**之前**调用。
func (m *MultiTenantSession) markTenantDestroyed(tenantID int64) {
	if m == nil || tenantID == 0 {
		return
	}
	m.destroyedMu.Lock()
	defer m.destroyedMu.Unlock()
	if m.destroyedTenants == nil {
		m.destroyedTenants = make(map[int64]struct{})
	}
	if _, ok := m.destroyedTenants[tenantID]; ok {
		return
	}
	m.destroyedTenants[tenantID] = struct{}{}
	m.destroyedOrder = append(m.destroyedOrder, tenantID)
	if len(m.destroyedOrder) > maxDestroyedTenants {
		drop := m.destroyedOrder[0]
		m.destroyedOrder = m.destroyedOrder[1:]
		delete(m.destroyedTenants, drop)
	}
}

// isTenantDestroyed 报告该租户的会话库是否已被销毁。
func (m *MultiTenantSession) isTenantDestroyed(tenantID int64) bool {
	if m == nil {
		return false
	}
	m.destroyedMu.RLock()
	defer m.destroyedMu.RUnlock()
	_, ok := m.destroyedTenants[tenantID]
	return ok
}

// NewMultiTenant creates a new multi-tenant session manager
func NewMultiTenant(dbPath string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	db, err := sqlite.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	m := &MultiTenantSession{
		db:             db,
		tenantSvc:      sqlite.NewTenantService(db),
		sessionSvc:     sqlite.NewSessionService(db),
		memorySvc:      sqlite.NewMemoryService(db),
		userProfileSvc: sqlite.NewUserProfileService(db),
		tokenUsageSvc:  sqlite.NewUserTokenUsageService(db),

		memoryProvider:        "flat",
		tenantCache:           make(map[string]*TenantSession),
		toolIndexFingerprints: make(map[int64]string),
		toolIndexPrevNames:    make(map[int64]map[string]bool),
		dbPath:                dbPath,
		mcpConfigPath:         "mcp.json", // 默认在工作目录下
		mcpInactivityTimeout:  30 * time.Minute,
		mcpCleanupInterval:    5 * time.Minute,
		sessionCacheTimeout:   24 * time.Hour,
		cleanupStopCh:         make(chan struct{}),
		shutdownCtx:           shutdownCtx,
		shutdownCancel:        shutdownCancel,
		// 每会话一个 DB（v71）：会话库池 + 根目录（db_path 相对主库目录解析）。
		// sessionSvc 仍指向主库 —— 它只服务**未拆分**的遗留路径（storage/migrate.go
		// 的旧文件迁移工具）；会话数据的读写全部经 TenantSession.sessionSvc（会话库，
		// GetOrCreateSession 里绑定）与 SessionServiceFor（直接构造点收口）。
		sessionDBs:   make(map[int64]*sessionDBEntry),
		sessionDBDir: filepath.Dir(dbPath),
	}

	// 存储后端抽象（SQLite）：tenant/memory/config/session 访问经此收口，与 PG
	// 路径（NewMultiTenantPG）共用同一 storage.Storage 契约。必须在应用选项之前
	// 设置 —— WithPersonaIsolation 等选项经 m.store 生效。
	m.store = storage.NewSQLiteStorage(db, m.sessionDBAsSessionStore, db.Close)

	// 应用配置选项
	for _, opt := range opts {
		opt(m)
	}

	// Memory-provider support services (chromem archival + tool index, and the
	// recall time-range function) are built through the SAME method as the PG
	// constructor — see initMemoryServices. Their directories come from an
	// explicit data dir, never from dbPath.
	m.initMemoryServices()

	return m, nil
}

// initMemoryServices builds the services the memory providers need: the chromem
// archival store, the tool index, and the recall time-range function.
//
// It is called by BOTH constructors, so this creation logic no longer lives on
// the SQLite-only path. Paths come from dataDir (an explicit, backend-independent
// data directory; defaults to config.XbotHome()) and must NEVER be derived from
// dbPath — on the PG path dbPath holds a DSN, not a file path.
func (m *MultiTenantSession) initMemoryServices() {
	if m.dataDir == "" {
		m.dataDir = config.XbotHome()
	}

	// Providers that need no vector store or recall helper (flat/none) stop here.
	if m.memoryProvider != "letta" {
		return
	}

	// Recall reads through the storage interface, so it works on every backend and
	// needs NO embedding config (it used to be set unconditionally in letta mode).
	m.recallTimeRangeFn = m.storeRecallTimeRange()

	if m.embeddingConfig == nil {
		return
	}

	// Shared embedding limit options (used by both archival and tool index).
	var embOpts []vectordb.EmbeddingLimitOption
	if m.embeddingConfig.MaxTokens > 0 {
		embOpts = append(embOpts, vectordb.WithMaxTokens(m.embeddingConfig.MaxTokens))
	}
	if m.embeddingConfig.TokenModel != "" {
		embOpts = append(embOpts, vectordb.WithTokenModel(m.embeddingConfig.TokenModel))
	}
	if m.embeddingConfig.LLMClient != nil && m.embeddingConfig.LLMModel != "" {
		embOpts = append(embOpts, vectordb.WithCompressor(vectordb.LLMContentCompressor(m.embeddingConfig.LLMClient, m.embeddingConfig.LLMModel)))
	}
	embFunc := vectordb.NewEmbeddingFunc(m.embeddingConfig.BaseURL, m.embeddingConfig.APIKey, m.embeddingConfig.Model, m.embeddingConfig.Provider, m.embeddingConfig.MaxTokens)

	// chromem-go archival store (unless injected via WithArchivalService).
	if m.archivalSvc == nil {
		archivalDir := filepath.Join(m.dataDir, "archival")
		archSvc, err := vectordb.NewArchivalService(archivalDir, embFunc, embOpts...)
		if err != nil {
			log.WithError(err).Error("Failed to initialize archival memory (chromem-go), archival tools will be unavailable")
		} else {
			m.archivalSvc = archSvc
		}
	}

	// chromem-go tool index (unless injected via WithToolIndexService).
	if m.toolIndexSvc == nil {
		toolIndexDir := filepath.Join(m.dataDir, "tool_index")
		toolIdxSvc, err := vectordb.NewToolIndexService(toolIndexDir, embFunc, embOpts...)
		if err != nil {
			log.WithError(err).Warn("Tool index DB corrupted, removing and recreating")
			if removeErr := os.RemoveAll(toolIndexDir); removeErr != nil {
				log.WithError(removeErr).Error("Failed to remove corrupted tool index directory")
			} else {
				toolIdxSvc, err = vectordb.NewToolIndexService(toolIndexDir, embFunc, embOpts...)
				if err != nil {
					log.WithError(err).Error("Failed to initialize tool index service after recreation, tool search will be unavailable")
				}
			}
		}
		if toolIdxSvc != nil {
			m.toolIndexSvc = toolIdxSvc
		}
	}

	// Recall reads through the storage interface, so it works on every backend.
	m.recallTimeRangeFn = m.storeRecallTimeRange()
}

// storeRecallTimeRange adapts the backend-agnostic MemoryStore range query to the
// vectordb RecallTimeRangeFunc shape (keeping storage/vectordb free of any storage
// dependency). The unbounded-end defaults mirror the SQLite implementation that
// used to back this function.
func (m *MultiTenantSession) storeRecallTimeRange() vectordb.RecallTimeRangeFunc {
	return func(tenantID int64, start, end time.Time, limit int) ([]vectordb.RecallEntry, error) {
		if start.IsZero() {
			start = time.Date(2000, 1, 1, 0, 0, 0, 0, time.Local)
		}
		if end.IsZero() {
			end = time.Now().Add(24 * time.Hour)
		}
		if limit <= 0 {
			limit = 20
		}
		entries, err := m.store.Memory().GetHistoryEntriesInRange(context.Background(), tenantID, start, end, limit)
		if err != nil {
			return nil, err
		}
		out := make([]vectordb.RecallEntry, len(entries))
		for i, e := range entries {
			out[i] = vectordb.RecallEntry{Entry: e.Entry, CreatedAt: e.CreatedAt}
		}
		return out, nil
	}
}

// NewMultiTenantPG creates a MultiTenantSession backed by PostgreSQL.
//
// Differences from NewMultiTenant (SQLite):
//   - No main *sqlite.DB handle and no per-session SQLite DB pool — sessions
//     come from storage.Storage.Session(tenantID) (shared PG pool, tenant_id
//     filtered).
//   - Memory provider is forced to "flat": the letta/xbot provider depends on
//     SQLite-only services (CoreMemoryService/archival) and is not PG-ready.
//   - dbPath holds the DSN (diagnostics only; there is no file path).
func NewMultiTenantPG(dsn string, opts ...MultiTenantOption) (*MultiTenantSession, error) {
	store, err := storage.NewPGStorage(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open pg storage: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	m := &MultiTenantSession{
		store:                 store,
		memoryProvider:        "flat", // xbot/letta 记忆 provider 暂不支持 PG
		tenantCache:           make(map[string]*TenantSession),
		toolIndexFingerprints: make(map[int64]string),
		toolIndexPrevNames:    make(map[int64]map[string]bool),
		dbPath:                dsn,
		mcpConfigPath:         "mcp.json",
		mcpInactivityTimeout:  30 * time.Minute,
		mcpCleanupInterval:    5 * time.Minute,
		sessionCacheTimeout:   24 * time.Hour,
		cleanupStopCh:         make(chan struct{}),
		shutdownCtx:           shutdownCtx,
		shutdownCancel:        shutdownCancel,
		sessionDBs:            make(map[int64]*sessionDBEntry),
	}

	// 应用配置选项
	for _, opt := range opts {
		opt(m)
	}

	// Memory services (archival store / tool index / recall) are created through
	// the SAME path as the SQLite constructor — see initMemoryServices. No provider
	// is forced here: whatever WithMemoryProvider asked for is honoured, and a
	// missing dependency surfaces as an explicit error rather than a silent
	// downgrade to flat.
	m.initMemoryServices()

	return m, nil
}

// SetMCPConfigPath 设置 MCP 配置文件路径
func (m *MultiTenantSession) SetMCPConfigPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mcpConfigPath = path
}

// RecordUserTokenUsage records token usage for a user (upsert).
func (m *MultiTenantSession) RecordUserTokenUsage(senderID, model string, inputTokens, outputTokens, cachedTokens int, conversationCount, llmCallCount int) error {
	// SQLite 停机期 m.db.Conn() 可能为 nil（PG 路径 m.db 恒为 nil）—— usage 是
	// 展示性冗余，明确拒绝优于让底层对 nil 连接 panic。
	if m.db != nil && m.db.Conn() == nil {
		return fmt.Errorf("database connection is nil (agent may be shutting down)")
	}
	return m.store.Config().RecordUsage(senderID, model, inputTokens, outputTokens, cachedTokens, conversationCount, llmCallCount)
}

// GetUserTokenUsage retrieves cumulative token usage for a user.
func (m *MultiTenantSession) GetUserTokenUsage(senderID string) (*sqlite.UserTokenUsage, error) {
	return m.store.Config().GetUsage(senderID)
}

// GetDailyTokenUsage retrieves daily token usage for a user.
func (m *MultiTenantSession) GetDailyTokenUsage(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return m.store.Config().GetDailyUsage(senderID, days)
}

// GetDailyTokenUsageSummary retrieves aggregated per-day usage for a user.
//
// ⚠️ SQLite-only：GetDailyUsageSummary 未纳入 storage.ConfigStore 契约（PG 实现
// 存在，但不经后端无关接口暴露）。PG 路径下明确报错，绝不静默返回空数据。
func (m *MultiTenantSession) GetDailyTokenUsageSummary(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	if m.tokenUsageSvc == nil {
		return nil, fmt.Errorf("daily usage summary is unavailable on the PG backend")
	}
	return m.tokenUsageSvc.GetDailyUsageSummary(senderID, days)
}

// GetAllUserTokenUsage retrieves token usage for all users, sorted by total desc.
//
// ⚠️ SQLite-only（同 GetDailyTokenUsageSummary 的理由：GetAllUsage 不在
// storage.ConfigStore 契约内）。
func (m *MultiTenantSession) GetAllUserTokenUsage() ([]sqlite.UserTokenUsage, error) {
	if m.tokenUsageSvc == nil {
		return nil, fmt.Errorf("all-user token usage is unavailable on the PG backend")
	}
	return m.tokenUsageSvc.GetAllUsage()
}

// SetSessionCreateGuard installs the phantom-session gate (see the field docs).
func (m *MultiTenantSession) SetSessionCreateGuard(fn func(channel, chatID string) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessionCreateGuard = fn
}

// GetOrCreateSession retrieves or creates a tenant session for the given channel and chatID.
// senderID is passed via context (letta.WithUserID) at call time, not here.
func (m *MultiTenantSession) GetOrCreateSession(channel, chatID string) (*TenantSession, error) {
	key := sessKey(channel, chatID)

	// Fast path: check cache with read lock
	m.mu.RLock()
	sess, ok := m.tenantCache[key]
	m.mu.RUnlock()

	if ok {
		// 标记会话为活跃
		sess.MarkActive()
		return sess, nil
	}

	// Slow path: acquire write lock and create session
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock
	if sess, ok := m.tenantCache[key]; ok {
		sess.MarkActive()
		return sess, nil
	}

	// Phantom-session gate: the single choke point every read path funnels
	// through. If the guard rejects a (channel, chatID) that does not exist yet,
	// refuse instead of creating a tenant — a stale client holding a DELETED
	// chatID must never resurrect it (sessions the user deleted kept coming
	// back via SSE/REST/RPC).
	if m.sessionCreateGuard != nil && !m.sessionCreateGuard(channel, chatID) {
		return nil, fmt.Errorf("unknown session %s:%s", channel, chatID)
	}

	// Get or create tenant ID
	tenantID, err := m.store.Tenant().GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get/create tenant: %w", err)
	}

	// 创建会话 MCP 管理器（用户作用域由 ConfigureSessionMCP 在消息处理时注入）
	sessionKey := sessKey(channel, chatID)
	mcpManager := tools.NewSessionMCPManager(sessionKey, "", m.mcpConfigPath, "", "", m.mcpInactivityTimeout)

	// 根据配置选择记忆提供者（通过注册表创建，无硬编码 provider 名称）
	var memProvider memory.MemoryProvider
	if m.memoryProvider == "none" {
		// No memory provider — tools and archiving are disabled.
		memProvider = nil
	} else {
		memDir := filepath.Join(config.XbotHome(), "memory", fmt.Sprintf("%d", tenantID))
		deps := memory.ProviderDeps{
			TenantID: tenantID,
			// Memories are scoped by the single operator (cross-session
			// shared, user_id=1). The multi-user system was removed — there
			// is exactly one owner for every session.
			UserID:  1,
			BaseDir: memDir,
		}
		// DB-backed memory reaches the provider ONLY through the storage seam —
		// never a driver handle. On every backend this is store.Memory().
		deps.AgentMemory = m.getMemoryStore()
		// Letta-specific deps
		if m.memoryProvider == "letta" {
			deps.LettaDeps = &letta.Deps{
				CoreSvc:      m.getMemoryStore(),
				ArchivalSvc:  m.archivalSvc,
				MemorySvc:    m.getMemoryStore(),
				ToolIndexSvc: m.toolIndexSvc,
			}
			// 前向兼容：一次性迁移 user_profiles → core memory blocks
			m.migrateProfileToCoreMemory(tenantID)
		}
		memProvider = memory.CreateProvider(m.memoryProvider, deps)
		// Fallback to flat if provider not registered
		if memProvider == nil {
			memProvider = flat.New(tenantID, memDir)
		}
	}
	// 会话数据入口：后端无关（storage.SessionStore）。SQLite 下 m.store.Session
	// 打开（含惰性迁移）该会话的独立库 —— TenantSession 的消息读写全部落到会话库，
	// 与其他会话的写真正并行（独立 writeMu + WAL）；PG 下返回共享连接（tenant_id
	// 过滤）。打开+迁移在 sessionDBMu 下串行（每会话一次的低频操作；本慢路径本就
	// 持有 m.mu 全局串行，不引入新的阻塞面）。锁序：m.mu → sessionDBMu
	// （DestroySession 同序，绝不反向）。
	sessionStore, err := m.store.Session(tenantID)
	if err != nil {
		return nil, fmt.Errorf("open session store for tenant %d: %w", tenantID, err)
	}
	// tenants 注册表（主库）：CWD / preview。后端无关（SQLite 主库 / PG 共享库）。
	tenantStore := m.store.Tenant()

	// Create tenant session
	sess = &TenantSession{
		tenantID:     tenantID,
		channel:      channel,
		chatID:       chatID,
		sessionStore: sessionStore, // 会话数据（SQLite 会话库 / PG 共享连接）
		tenantStore:  tenantStore,  // 主库（tenants 注册表：CWD/preview）
		memorySvc:    m.getMemoryStore(),
		memory:       memProvider,
		mcpManager:   mcpManager,
		lastActive:   time.Now(),
		cwd:          loadPersistedCWD(tenantStore, tenantID),
	}

	m.tenantCache[key] = sess
	return sess, nil
}

// GetSession returns a cached tenant session without creating one.
func (m *MultiTenantSession) GetSession(channel, chatID string) (*TenantSession, bool) {
	key := sessKey(channel, chatID)
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.tenantCache[key]
	return sess, ok
}

// ConfigureSessionMCP 根据当前用户更新会话 MCP 作用域。
// 返回新注册的个人 MCP 工具名列表（用于立即激活），catalog 未变化时返回 nil。
func (m *MultiTenantSession) ConfigureSessionMCP(channel, chatID, senderID, workDir string) ([]string, error) {
	sess, err := m.GetOrCreateSession(channel, chatID)
	if err != nil {
		return nil, err
	}

	mgr := sess.GetMCPManager()
	if mgr == nil {
		return nil, nil
	}

	userConfigPath := tools.UserMCPConfigPath(workDir, senderID)
	workspaceRoot := tools.UserWorkspaceRoot(workDir, senderID)
	mgr.UpdateScope(senderID, userConfigPath, workspaceRoot)

	newTools := m.indexPersonalMCPTools(sess.TenantID(), mgr)
	return newTools, nil
}

// catalogFingerprint computes a stable hash of the MCP catalog tool names.
func catalogFingerprint(catalog []tools.MCPServerCatalogEntry) string {
	var keys []string
	for _, entry := range catalog {
		for _, toolName := range entry.ToolNames {
			keys = append(keys, entry.Name+":"+toolName)
		}
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

// indexPersonalMCPTools indexes personal MCP tools for a tenant.
// Returns only truly NEW tool names (added since last catalog snapshot) for immediate activation.
// On first load or when catalog is unchanged, returns nil.
func (m *MultiTenantSession) indexPersonalMCPTools(tenantID int64, mgr *tools.SessionMCPManager) []string {
	if mgr == nil {
		return nil
	}

	catalog := mgr.GetCatalog()
	if len(catalog) == 0 {
		return nil
	}

	// Build tool entries and names
	var entries []memory.ToolIndexEntry
	var toolNames []string
	for _, entry := range catalog {
		for _, toolName := range entry.ToolNames {
			fullName := fmt.Sprintf("mcp_%s_%s", entry.Name, toolName)
			desc := fmt.Sprintf("MCP server: %s. Tool: %s", entry.Name, toolName)
			if entry.Instructions != "" {
				desc = fmt.Sprintf("%s. %s", desc, entry.Instructions)
			}
			entries = append(entries, memory.ToolIndexEntry{
				Name:        fullName,
				ServerName:  entry.Name,
				Source:      "personal",
				Description: desc,
			})
			toolNames = append(toolNames, fullName)
		}
	}

	// Fingerprint check: skip if catalog unchanged
	// Check in-memory first, fall back to disk (survives restart)
	fp := catalogFingerprint(catalog)
	m.mu.RLock()
	prev := m.toolIndexFingerprints[tenantID]
	prevNames := m.toolIndexPrevNames[tenantID]
	m.mu.RUnlock()
	if prev == "" && m.toolIndexSvc != nil {
		prev = m.toolIndexSvc.GetFingerprint(tenantID)
		if prev != "" {
			m.mu.Lock()
			m.toolIndexFingerprints[tenantID] = prev
			m.mu.Unlock()
		}
	}
	if fp == prev {
		return nil
	}

	// Catalog changed — kick off async indexing (if tool index is available)
	if m.toolIndexSvc != nil && len(entries) > 0 {
		entriesCopy := make([]memory.ToolIndexEntry, len(entries))
		copy(entriesCopy, entries)
		fpCopy := fp
		go func() {
			ctx, cancel := context.WithTimeout(m.shutdownCtx, 10*time.Minute)
			defer cancel()
			if err := m.IndexToolsForTenant(ctx, tenantID, entriesCopy); err != nil {
				if m.shutdownCtx.Err() != nil {
					log.Debugf("Index personal MCP tools for tenant %d cancelled by shutdown", tenantID)
				} else {
					log.WithError(err).Warnf("Failed to index personal MCP tools for tenant %d", tenantID)
				}
				return
			}
			log.Infof("Indexed %d personal MCP tools for tenant %d", len(entriesCopy), tenantID)
			m.toolIndexSvc.SetFingerprint(tenantID, fpCopy)
			m.mu.Lock()
			m.toolIndexFingerprints[tenantID] = fpCopy
			m.mu.Unlock()
		}()
	}

	// Update tool name snapshot (fingerprint updated in goroutine after success)
	currentNames := make(map[string]bool, len(toolNames))
	for _, name := range toolNames {
		currentNames[name] = true
	}
	m.mu.Lock()
	m.toolIndexPrevNames[tenantID] = currentNames
	m.mu.Unlock()

	// First load: no previous snapshot → all tools are pre-existing, not "new"
	if prevNames == nil {
		return nil
	}

	// Subsequent change: only return tools not in previous catalog
	var newTools []string
	for _, name := range toolNames {
		if !prevNames[name] {
			newTools = append(newTools, name)
		}
	}
	return newTools
}

// migrateProfileToCoreMemory performs a one-time forward-compatible migration
// of legacy user_profiles data into Letta core memory blocks.
// - __me__ profile → persona block (bot identity, global per-tenant)
// Only writes if the target block is currently empty to avoid overwriting user edits.
func (m *MultiTenantSession) migrateProfileToCoreMemory(tenantID int64) {
	// Check if persona block is already populated (persona is global, use "" for userID)
	persona, _, err := m.store.Memory().GetBlock(tenantID, "persona", "")
	if err != nil {
		log.WithError(err).Warn("Profile migration: failed to read persona block")
		return
	}
	if persona != "" {
		return // Already has content, skip migration
	}

	// Read self profile (__me__)
	_, selfProfile, err := m.store.Config().GetUserProfile("__me__")
	if err != nil {
		log.WithError(err).Warn("Profile migration: failed to read __me__ profile")
		return
	}
	if selfProfile == "" {
		return // No profile to migrate
	}

	// Write to persona block (global, use "" for userID)
	if err := m.store.Memory().SetBlock(tenantID, "persona", selfProfile, ""); err != nil {
		log.WithError(err).Warn("Profile migration: failed to write persona block")
		return
	}
	log.WithField("tenant_id", tenantID).Info("Migrated __me__ profile to persona core memory block")
}

// RecallTimeRangeFunc returns the time-range recall search function (nil if not in Letta mode).
func (m *MultiTenantSession) RecallTimeRangeFunc() vectordb.RecallTimeRangeFunc {
	return m.recallTimeRangeFn
}

// IndexToolsForTenant indexes MCP tools for a specific tenant.
func (m *MultiTenantSession) IndexToolsForTenant(ctx context.Context, tenantID int64, tools []memory.ToolIndexEntry) error {
	if m.toolIndexSvc == nil {
		return nil // Tool index not available (flat mode or no embedding config)
	}
	// Convert memory.ToolIndexEntry to vectordb.ToolIndexEntry
	entries := make([]vectordb.ToolIndexEntry, len(tools))
	for i, t := range tools {
		entries[i] = vectordb.ToolIndexEntry{
			Name:        t.Name,
			ServerName:  t.ServerName,
			Source:      t.Source,
			Description: t.Description,
		}
	}
	return m.toolIndexSvc.IndexTools(ctx, tenantID, entries)
}

// Close closes the database connection
func (m *MultiTenantSession) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// 每会话一个 DB（v71）：停机先关全部会话库（checkpoint WAL 已提交内容进
	// 主文件 —— gotcha「停机必须 checkpoint」对每个会话库同样成立），再关主库。
	// closeAllSessionDBs 自带 sessionDBMu（锁序 m.mu → sessionDBMu，与
	// GetOrCreateSession/DestroySession 一致，无反向获取）。
	m.closeAllSessionDBs()
	// 存储后端收口：SQLite 的 closeFn 即主库 Close（与原 m.db.Close() 等价）；
	// PG 路径关闭连接池。
	if m.store != nil {
		return m.store.Close()
	}
	if m.db != nil {
		return m.db.Close()
	}
	return nil
}

// DBPath returns the database path (useful for migration checks)
func (m *MultiTenantSession) DBPath() string {
	return m.dbPath
}

// DB returns the underlying SQLite database connection
func (m *MultiTenantSession) DB() *sqlite.DB {
	return m.db
}

// Store returns the backend-agnostic storage layer (SQLite adapter or PG).
// Callers use it to reach Config()/Tenant()/Session() without depending on a
// concrete backend — e.g. the web channel's auth store comes from
// Store().Config() so the same wiring serves SQLite and PG.
func (m *MultiTenantSession) getMemoryStore() storage.MemoryStore {
	if m.store != nil {
		return m.store.Memory()
	}
	return nil
}

func (m *MultiTenantSession) Store() storage.Storage {
	return m.store
}

// CoreMemoryService returns the shared core memory service.
func (m *MultiTenantSession) CoreMemoryService() storage.MemoryStore {
	return m.getMemoryStore()
}

// ArchivalService returns the shared archival memory service.
func (m *MultiTenantSession) ArchivalService() *vectordb.ArchivalService {
	return m.archivalSvc
}

// MemoryService returns the shared memory (recall) service.
func (m *MultiTenantSession) MemoryService() storage.MemoryStore {
	return m.getMemoryStore()
}

// ToolIndexService returns the shared tool index service.
func (m *MultiTenantSession) ToolIndexService() *vectordb.ToolIndexService {
	return m.toolIndexSvc
}

// RecallTimeRange returns the recall time-range search function.
func (m *MultiTenantSession) RecallTimeRange() vectordb.RecallTimeRangeFunc {
	return m.recallTimeRangeFn
}

// NewLettaMemory creates a new LettaMemory instance with an independent tenantID.
// The service instances (memory store, archivalSvc, etc.) are shared — data isolation
// is provided by the tenantID parameter.
func (m *MultiTenantSession) NewLettaMemory(tenantID int64) *letta.LettaMemory {
	return letta.New(tenantID, m.getMemoryStore(), m.archivalSvc, m.getMemoryStore(), m.toolIndexSvc)
}

// GetSessionMCPManager 实现 SessionMCPManagerProvider 接口
func (m *MultiTenantSession) GetSessionMCPManager(sessionKey string) *tools.SessionMCPManager {
	m.mu.RLock()
	sess, ok := m.tenantCache[sessionKey]
	m.mu.RUnlock()

	if ok {
		return sess.GetMCPManager()
	}
	return nil
}

// StartCleanupRoutine 启动后台清理协程
func (m *MultiTenantSession) StartCleanupRoutine() {
	m.cleanupWg.Add(1)
	go func() {
		defer m.cleanupWg.Done()
		ticker := time.NewTicker(m.mcpCleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				m.cleanupInactiveResources()
			case <-m.cleanupStopCh:
				return
			}
		}
	}()
	log.WithFields(log.Fields{
		"mcpCleanupInterval":  m.mcpCleanupInterval,
		"sessionCacheTimeout": m.sessionCacheTimeout,
	}).Info("MCP cleanup routine started")
}

// StopCleanupRoutine 停止清理协程并取消后台任务（可安全重复调用）
func (m *MultiTenantSession) StopCleanupRoutine() {
	m.cleanupStopOnce.Do(func() {
		m.shutdownCancel()
		close(m.cleanupStopCh)
		m.cleanupWg.Wait()
	})
}

// SetOnSessionEvict 设置会话被清理时的回调（用于 Registry 清理 sessionActivated/sessionRound）
func (m *MultiTenantSession) SetOnSessionEvict(cb func(sessionKey string)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.onSessionEvict = cb
}

// cleanupInactiveResources 清理不活跃的资源（MCP 连接和会话缓存）
func (m *MultiTenantSession) cleanupInactiveResources() {
	m.mu.Lock()

	now := time.Now()
	var sessionsToDelete []string

	// 收集需要清理的会话（持锁期间只做轻量操作，不执行 I/O）
	for key, sess := range m.tenantCache {
		// 使用 lastActive 字段判断是否超时（避免在锁内调用 CleanupInactiveMCPs 的 I/O）
		lastActive := sess.LastActive()
		if now.Sub(lastActive) > m.sessionCacheTimeout {
			sessionsToDelete = append(sessionsToDelete, key)
		}
	}

	// 收集要关闭的会话并从缓存中移除（持锁期间只做轻量操作）
	type sessionToClose struct {
		key  string
		sess *TenantSession
	}
	var toClose []sessionToClose
	for _, key := range sessionsToDelete {
		if sess, ok := m.tenantCache[key]; ok {
			toClose = append(toClose, sessionToClose{key: key, sess: sess})
			delete(m.tenantCache, key)
		}
	}

	// 获取回调并释放锁
	onEvict := m.onSessionEvict
	m.mu.Unlock()

	// 释放锁后执行所有 I/O 操作（关闭 MCP 连接）和回调
	for _, item := range toClose {
		// CleanupInactiveMCPs 和 Close 都包含 I/O，在锁外执行
		item.sess.CleanupInactiveMCPs()
		item.sess.Close()
		// 每会话一个 DB（v71）：TenantSession 驱逐 = 其会话库关闭（checkpoint +
		// close + 出池）。活跃会话的库永不关闭（无 LRU 上限 —— 用户要求支持任意
		// 数量并发会话）；只有 24h 空闲驱逐 / 显式销毁 / 停机才关库。下次
		// GetOrCreateSession / SessionServiceFor 会重新打开（惰性迁移幂等，migrated=1
		// 直接跳过复制）。
		m.evictSessionDB(item.key, item.sess.TenantID())
		log.WithField("session", item.key).Info("Removed session from cache due to inactivity")
		// 通知 Registry 清理该会话的激活状态
		if onEvict != nil {
			onEvict(item.key)
		}
	}

	// 孤儿清扫：不经 TenantSession 打开的会话库（SessionServiceFor 直连路径，如
	// usage 查询）超过 1h 无访问且无缓存 TenantSession → 关闭（fd 卫生；活跃
	// 会话不受影响 —— 它们的库由上面的驱逐路径管理，且任何访问都刷新 lastAccess）。
	// ⛔ 锁序：sweepIdleSessionDBs 内部绝不持 sessionDBMu 获取 m.mu（AB-BA 死锁，
	// 见其注释）。
	m.sweepIdleSessionDBs()
}

// evictSessionDB 在 TenantSession 驱逐后关闭其会话库，带「重建守卫」。
//
// ⛔ 竞态修复：缓存移除（cleanupInactiveResources 的 m.mu.Lock 段）与本调用
// 之间，GetOrCreateSession 可能重建该会话（慢路径持 m.mu 全程 → sessionDB()
// map 命中 → 新 TenantSession 复用池里的库）—— 无守卫地关库 = 新会话永久绑在
// 已关的库上（sessionSvc 持有已 close 的 *sqlite.DB，直到下次驱逐才恢复）。
// 守卫：持 m.mu.RLock 跨「重查 + 关库」，与 GetOrCreateSession 慢路径（m.mu.Lock
// 全程）互斥 —— 窗口关闭。锁序 m.mu → sessionDBMu（与 GetOrCreateSession 同序，
// 绝不反向）。
func (m *MultiTenantSession) evictSessionDB(sessionKey string, tenantID int64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, recreated := m.tenantCache[sessionKey]; recreated {
		// 驱逐与关库之间被 GetOrCreateSession 重建 —— 新会话拥有池里的库，
		// 跳过关库（它自己的驱逐周期会再关）。
		return
	}
	m.closeSessionDB(tenantID)
}

// DestroySession completely removes a tenant session: cache eviction, DB deletion
// (with CASCADE to messages), and MCP cleanup. Used when SubAgent sessions end
// their lifecycle to prevent stale data leaking into future sessions with the
// same role/instance key.
//
// v71（每会话一个 DB）：先删会话库文件（checkpoint + close + 删文件 + -wal/-shm，
// 空间立即回收 —— db_path 从注册表读，必须在删除 tenants 行**之前**），再删
// 主库 tenants 行（CASCADE 清主库遗留的 session_messages/iteration_history 旧数据）。
func (m *MultiTenantSession) DestroySession(channel, chatID string) error {
	key := sessKey(channel, chatID)

	m.mu.Lock()
	sess, ok := m.tenantCache[key]
	if ok {
		delete(m.tenantCache, key)
	}
	onEvict := m.onSessionEvict
	m.mu.Unlock()

	if !ok {
		// Not in cache — still try to delete from DB in case it exists there.
		tenantID, err := m.store.Tenant().GetTenantIDByChannelChatID(channel, chatID)
		if err != nil || tenantID == 0 {
			return nil // doesn't exist, nothing to do
		}
		// 删会话库文件（读 db_path 必须在删 tenants 行之前 —— 注册表是路径唯一权威）。
		m.deleteSessionDBFile(tenantID)
		_ = m.store.Tenant().DeleteTenant(tenantID)
		return nil
	}

	// Close MCP connections outside lock
	sess.Close()

	// 删会话库文件（checkpoint + close + 删文件；db_path 读注册表，先于删行）。
	m.deleteSessionDBFile(sess.TenantID())

	// Delete from DB (CASCADE removes all messages)
	_ = m.store.Tenant().DeleteTenant(sess.TenantID())

	// Notify Registry to clean up session-scoped activations
	if onEvict != nil {
		onEvict(key)
	}

	log.WithFields(log.Fields{
		"session":   key,
		"tenant_id": sess.TenantID(),
	}).Info("Tenant session destroyed")

	return nil
}

// InvalidateAll 使所有缓存会话的 MCP 连接失效，强制下次使用时重新加载
func (m *MultiTenantSession) InvalidateAll() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, sess := range m.tenantCache {
		sess.InvalidateMCP()
		log.WithField("session", key).Debug("Invalidated session MCP")
	}

	log.Info("All session MCP connections invalidated, will reload on next use")
}

// InvalidateSessionMCP 使特定会话的 MCP 连接失效
// 用于 token 刷新等场景，需要重新建立特定 MCP 服务器的连接
func (m *MultiTenantSession) InvalidateSessionMCP(sessionKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if sess, ok := m.tenantCache[sessionKey]; ok {
		sess.InvalidateMCP()
		log.WithField("session", sessionKey).Info("Session MCP invalidated")
	}
}

// ClearMemory clears the specified memory type(s) for a tenant identified by (channel, chatID).
// targetType is one of: "session", "core_persona", "core_human", "core_working",
// "core_all", "long_term", "event_history", "archival", "reset_all".
func (m *MultiTenantSession) ClearMemory(ctx context.Context, channel, chatID, targetType, userID string) error {
	tenantID, err := m.store.Tenant().GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return fmt.Errorf("resolve tenant: %w", err)
	}

	var errs []string
	appendErr := func(name string, err error) {
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", name, err))
		}
	}

	switch targetType {
	case "session":
		// 每会话一个 DB（v71）：session_messages/iteration_history 在会话库。
		svc, err := m.SessionServiceFor(tenantID)
		if err != nil {
			appendErr("session", fmt.Errorf("open session db: %w", err))
		} else {
			appendErr("session", svc.Clear(tenantID))
			// 清空后 preview 也清（主库 tenants.preview —— 跨会话列表读这一列）。
			if sdb, dbErr := m.sessionDB(tenantID); dbErr == nil {
				m.recomputeSessionPreview(sdb, tenantID)
			}
		}
		// Evict cached session so next request loads fresh state
		sessionKey := sessKey(channel, chatID)
		m.mu.Lock()
		if sess, ok := m.tenantCache[sessionKey]; ok {
			sess.Close()
			delete(m.tenantCache, sessionKey)
		}
		m.mu.Unlock()
	case "core_persona":
		appendErr("persona", m.store.Memory().ClearBlock(tenantID, "persona", ""))
	case "core_human":
		appendErr("human", m.store.Memory().ClearBlock(tenantID, "human", userID))
	case "core_working":
		appendErr("working_context", m.store.Memory().ClearBlock(tenantID, "working_context", ""))
	case "core_all":
		appendErr("core_all", m.store.Memory().ClearAllBlocks(tenantID, userID))
		// Evict cached session to reset in-memory core memory references
		sessionKey := sessKey(channel, chatID)
		m.mu.Lock()
		if sess, ok := m.tenantCache[sessionKey]; ok {
			sess.Close()
			delete(m.tenantCache, sessionKey)
		}
		m.mu.Unlock()
	case "long_term":
		appendErr("long_term", m.store.Memory().ClearLongTerm(ctx, tenantID))
	case "event_history":
		appendErr("event_history", m.store.Memory().ClearHistory(ctx, tenantID))
	case "archival":
		if m.archivalSvc != nil {
			appendErr("archival", m.archivalSvc.ClearAll(ctx, tenantID))
		}
	case "reset_all":
		// 每会话一个 DB（v71）：session_messages/iteration_history 在会话库。
		if svc, err := m.SessionServiceFor(tenantID); err != nil {
			appendErr("session", fmt.Errorf("open session db: %w", err))
		} else {
			appendErr("session", svc.Clear(tenantID))
			if sdb, dbErr := m.sessionDB(tenantID); dbErr == nil {
				m.recomputeSessionPreview(sdb, tenantID)
			}
		}
		appendErr("core_all", m.store.Memory().ClearAllBlocks(tenantID, userID))
		appendErr("long_term", m.store.Memory().ClearLongTerm(ctx, tenantID))
		appendErr("event_history", m.store.Memory().ClearHistory(ctx, tenantID))
		appendErr("state", m.store.Memory().ClearState(ctx, tenantID))
		if m.archivalSvc != nil {
			appendErr("archival", m.archivalSvc.ClearAll(ctx, tenantID))
		}
		// Evict session from cache to reset in-memory state
		sessionKey := sessKey(channel, chatID)
		m.mu.Lock()
		if sess, ok := m.tenantCache[sessionKey]; ok {
			sess.Close()
			delete(m.tenantCache, sessionKey)
		}
		m.mu.Unlock()
	default:
		return fmt.Errorf("unknown target type: %s", targetType)
	}

	if len(errs) > 0 {
		return fmt.Errorf("部分清空失败: %s", strings.Join(errs, "; "))
	}

	log.WithFields(log.Fields{
		"tenant_id":   tenantID,
		"target_type": targetType,
	}).Info("Memory cleared")
	return nil
}

// GetMemoryStats returns statistics for all memory types of a tenant.
func (m *MultiTenantSession) GetMemoryStats(ctx context.Context, channel, chatID, userID string) map[string]string {
	stats := map[string]string{}

	tenantID, err := m.store.Tenant().GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return stats
	}

	// Session message count（会话库 —— v71 每会话一个 DB）
	if svc, err := m.SessionServiceFor(tenantID); err == nil {
		if count, err := svc.GetMessagesCount(tenantID); err == nil {
			stats["session"] = fmt.Sprintf("%d 条消息", count)
		}
	}

	// Core memory blocks
	if blocks, err := m.store.Memory().GetAllBlocks(tenantID, userID); err == nil {
		for _, name := range []string{"persona", "human", "working_context"} {
			if content, ok := blocks[name]; ok && content != "" {
				stats[name] = fmt.Sprintf("%d chars", len(content))
			}
		}
	}

	// Archival memory count
	if m.archivalSvc != nil {
		if count, err := m.archivalSvc.Count(tenantID); err == nil && count > 0 {
			stats["archival"] = fmt.Sprintf("%d 条", count)
		}
	}

	// Long-term memory
	if content, err := m.store.Memory().ReadLongTerm(ctx, tenantID); err == nil && content != "" {
		stats["long_term"] = "有内容"
	}

	// Event history count
	if count, err := m.store.Memory().GetHistoryCount(ctx, tenantID); err == nil && count > 0 {
		stats["event_history"] = fmt.Sprintf("%d 条", count)
	}

	return stats
}

// GetSessionUsageStats aggregates a session's usage & performance from
// iteration_history (v59: per-iteration input/cached tokens + model).
// Read-only: resolves the tenant via GetTenantIDByChannelChatID (no tenant
// creation side effect). Returns (nil, nil) when the session doesn't exist.
//
// v71（每会话一个 DB）：iteration_history 在会话库；tenant_state 水位 +
// tenants 元数据（CurrentModel/SessionCreatedAt/SessionLastActive）从主库补齐
// （GetTenantUsageStats 只查会话库的 iteration_history）。
func (m *MultiTenantSession) GetSessionUsageStats(channel, chatID string, recentLimit int) (*sqlite.TenantUsageStats, error) {
	tenantID, err := m.store.Tenant().GetTenantIDByChannelChatID(channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	if tenantID == 0 {
		return nil, nil
	}
	svc, err := m.SessionServiceFor(tenantID)
	if err != nil {
		return nil, fmt.Errorf("open session store for usage stats: %w", err)
	}
	stats, err := svc.GetTenantUsageStats(tenantID, recentLimit)
	if err != nil {
		return nil, err
	}
	if stats != nil {
		m.fillUsageStatsFromMainDB(tenantID, stats)
	}
	return stats, nil
}

// fillUsageStatsFromMainDB 补齐 GetTenantUsageStats 不再从会话库读的字段
// （tenant_state 水位 + tenants 元数据 —— 主库表）。失败静默（这些是展示性
// 字段，缺省零值可接受）。
// PG 路径无主库 *sqlite.DB（SQLite 专属 ? 占位符查询）—— 直接跳过。
func (m *MultiTenantSession) fillUsageStatsFromMainDB(tenantID int64, stats *sqlite.TenantUsageStats) {
	if m.db == nil {
		return
	}
	conn := m.db.Conn()
	if conn == nil {
		return
	}
	_ = conn.QueryRow(`SELECT COALESCE(last_prompt_tokens, 0), COALESCE(last_completion_tokens, 0) FROM tenant_state WHERE tenant_id = ?`, tenantID).
		Scan(&stats.LastPromptTokens, &stats.LastCompletionTokens)
	_ = conn.QueryRow(`SELECT COALESCE(model, ''), COALESCE(created_at, ''), COALESCE(last_active_at, '') FROM tenants WHERE id = ?`, tenantID).
		Scan(&stats.CurrentModel, &stats.SessionCreatedAt, &stats.SessionLastActive)
}

// GetSessionUsageBuckets aggregates a session's usage into fixed-width time
// buckets, entirely in SQL over the FULL iteration_history table (unlike
// GetSessionUsageStats' RecentIterations, which is capped at ≤500 detail rows —
// that cap is why the trend chart could only cover the newest calls).
// Read-only mirror of GetSessionUsageStats: resolves the tenant via
// GetTenantIDByChannelChatID (no tenant creation side effect). Returns
// (nil, nil) when the session doesn't exist.
func (m *MultiTenantSession) GetSessionUsageBuckets(channel, chatID string, bucketSeconds int64, count int, tzOffsetMinutes int) ([]sqlite.UsageBucket, error) {
	tenantID, err := m.store.Tenant().GetTenantIDByChannelChatID(channel, chatID)
	if err != nil {
		return nil, fmt.Errorf("get tenant: %w", err)
	}
	if tenantID == 0 {
		return nil, nil
	}
	svc, err := m.SessionServiceFor(tenantID)
	if err != nil {
		return nil, fmt.Errorf("open session store for usage buckets: %w", err)
	}
	return svc.GetTenantUsageBuckets(tenantID, bucketSeconds, count, tzOffsetMinutes)
}

// RewindHistory truncates a session at a stable user history node.
func (m *MultiTenantSession) RewindHistory(channel, chatID string, historyID int64) (llm.ChatMessage, int, error) {
	tenantID, err := m.store.Tenant().GetOrCreateTenantID(channel, chatID)
	if err != nil {
		return llm.ChatMessage{}, 0, fmt.Errorf("get tenant: %w", err)
	}
	// 每会话一个 DB（v71）：rewind 操作会话库（session_messages/iteration_history
	// 截断）。tenant_state（主库）的 token 水位恢复由 TenantSession 层完成（跨库
	// 无法原子 —— token 水位是派生缓存，下一条用户消息的 SaveContextTokens 自愈）。
	svc, err := m.SessionServiceFor(tenantID)
	if err != nil {
		return llm.ChatMessage{}, 0, fmt.Errorf("open session store for rewind: %w", err)
	}
	target, turnIdx, promptTokens, err := svc.RewindToHistoryID(tenantID, historyID)
	if err != nil {
		return llm.ChatMessage{}, 0, err
	}
	// tenant_state 恢复（主库，尽力而为）：截断后剩余历史的最后一条用户消息的
	// context_tokens。失败只 Warn —— 水位是派生缓存（GetLastContextTokens 从会话库
	// session_messages.context_tokens 读，截断已一致），不阻塞 rewind。
	if err := m.store.Memory().SetTokenState(context.Background(), tenantID, promptTokens, 0); err != nil {
		log.WithError(err).WithField("tenant_id", tenantID).Warn("rewind: restore tenant_state token watermark failed (derived cache; self-heals on next message)")
	}
	// preview 重算（主库 tenants.preview）：截断后最新一条 user/assistant 消息变化。
	if sdb, dbErr := m.sessionDB(tenantID); dbErr == nil {
		m.recomputeSessionPreview(sdb, tenantID)
	}
	return target, turnIdx, nil
}
