# Plan: Storage Backend Interfaces (v2 — Functional Contract)

## Principle

Interfaces define **what operations the upper layer needs**, not how they're implemented. SQLite and PG each implement them with their own internal patterns. The upper layer never touches `*sql.DB` or any backend-specific concept.

## 5 Interfaces

```go
package storage

// Storage is the top-level entry point. One instance per process.
// SQLite impl: wraps main DB + per-session DB pool (v71).
// PG impl: wraps main connection + per-session databases (or shared pool).
type Storage interface {
    Session(tenantID int64) (SessionStore, error)  // per-session data
    Tenant() TenantStore                           // registry + tenant lifecycle
    Memory() MemoryStore                           // core memory + long-term + events
    Config() ConfigStore                           // user/LLM/cron/trigger/share
    Close() error
}

// ─── SessionStore: per-session data (messages + iteration history) ───
// SQLite: bound to a per-session DB file.
// PG: per-session database or shared connection with tenant_id filtering.
type SessionStore interface {
    // Messages
    AddMessage(tenantID int64, msg llm.ChatMessage) error
    AddMessageWithID(tenantID int64, msg llm.ChatMessage) (int64, error)
    ReplaceToolMessage(tenantID int64, toolName, toolCallID, content string) error
    GetHistory(tenantID int64, limit int) ([]llm.ChatMessage, error)
    GetHistoryBefore(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, error)
    GetAllMessages(tenantID int64) ([]llm.ChatMessage, error)
    GetMessagesCount(tenantID int64) (int, error)
    Clear(tenantID int64) error
    UpdateMessageContent(tenantID int64, messageIndex int, content string) error
    UpdateUserMessageContextTokens(tenantID int64, promptTokens int64) error
    GetLastUserMessageContent(tenantID int64) (string, error)
    HasAssistantReplyAfterLastUser(tenantID int64) (bool, error)

    // Turns
    GetMaxTurnID(tenantID int64) (uint64, error)
    GetLastUserTurnID(tenantID int64) (uint64, error)
    GetMaxIterationForTurn(tenantID int64, turnID uint64) (int, error)

    // Iteration history
    AppendIterationHistory(tenantID int64, msgID int64, turnID uint64, rec IterationRecord) error
    AppendIterationTool(tenantID int64, turnID uint64, iteration int, toolJSON string) (bool, error)
    GetIterationHistoryByTurn(tenantID int64, turnID uint64) ([]IterationRecord, error)
    GetIterationHistoryByTurns(tenantID int64, turnIDs []uint64) (map[uint64][]IterationRecord, error)

    // Session DB lifecycle (v71 concepts — PG per-session DB also needs these)
    HasLegacyRows(tenantID int64) (bool, error)
    LatestPreview(tenantID int64) (string, error)

    // ── Added v2.1: the full TenantSession surface ──
    // TenantSession calls 19 further methods; without them the SQLite/PG
    // backends cannot both satisfy the interface and the PG path breaks at
    // runtime (it compiled only because TenantSession held a concrete
    // *sqlite.SessionService). Signatures are copied verbatim from
    // storage/sqlite/session.go + history.go.
    AppendMessage(tenantID int64, msg llm.ChatMessage) (int64, error)
    AppendMessages(tenantID int64, messages []llm.ChatMessage) ([]int64, error)
    AppendMessagesAndAskQuestion(tenantID int64, messages []llm.ChatMessage, metadata map[string]string) ([]int64, int64, error)
    AppendCommandMessage(tenantID int64, role, content string) (int64, error)
    AppendControl(tenantID int64, recordType HistoryRecordType, targetHistoryID int64, data any) (int64, error)
    AppendContextSnapshot(tenantID int64, recordType HistoryRecordType, messages []llm.ChatMessage) (int64, error)
    AppendAskQuestion(tenantID int64, metadata map[string]string) (int64, error)
    AppendAskAnswer(tenantID int64, answer string) (int64, error)
    AppendAskAnswerWithUserMessage(tenantID int64, answer string, answerMsg llm.ChatMessage) (int64, error)
    AppendMasks(tenantID int64, mutations []MaskMutation) error
    GetAllIterationHistory(tenantID int64) ([]IterationRecord, error)
    GetFullHistory(tenantID int64) ([]HistoryRecord, error)
    GetHistoryBeforeForDisplay(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, int, error)
    Replay(tenantID int64) (*ReplayResult, error)
    RewindToHistoryID(tenantID, historyID int64) (llm.ChatMessage, int, int64, error)
    GetUserMessageCount(tenantID int64) (int, error)
    GetTenantUsageStats(tenantID int64, recentLimit int) (*TenantUsageStats, error)
    GetLastUserMessageContextTokens(tenantID int64) (int64, error)
    UpdateMessageContentNonDisplayOnly(tenantID int64, messageIndex int, content string) error
}

// ─── TenantStore: registry (tenants + user_chats + pending_resumes) ───
// Ties sessions to their backends. Lives in the main DB.
type TenantStore interface {
    // Tenant CRUD
    GetOrCreateTenantID(channel, chatID string) (int64, error)
    GetTenantIDByChannelChatID(channel, chatID string) (int64, error)
    GetTenantInfo(tenantID int64) (channel, chatID string, err error)
    DeleteTenant(tenantID int64) error
    DeleteTenantHistory(tenantID int64) error
    TouchTenantID(channel, chatID string) error

    // Per-session DB path / migration state (v71)
    SetTenantDBPath(tenantID int64, dbPath string) error
    SetTenantMigrated(tenantID int64) error
    GetTenantDBInfo(tenantID int64) (TenantDBInfo, error)

    // Tenant config
    SetTenantSubscription(channel, chatID, subscriptionID, model string) error
    GetTenantSubscription(channel, chatID string) (subscriptionID, model string, err error)
    SetTenantRunner(channel, chatID, runnerID string) error
    GetTenantRunner(channel, chatID string) (string, error)
    ClearSubscriptionFromTenants(subID string) error
    SetTenantCWD(tenantID int64, cwd string) error
    GetTenantCWD(tenantID int64) (string, error)
    SetTenantPreview(tenantID int64, content string) error

    // Chat registry
    CreateChat(channel, senderID, label string) (string, error)
    DeleteChat(channel, senderID, chatID string) error
    RenameChat(channel, senderID, chatID, label string) error
    ListUserChats(channel, senderID, currentChatID string, offset, limit int) ([]UserChatWithPreview, bool, error)
    GetSenderForChat(channel, chatID string) (string, error)

    // Pending resumes
    AddPendingResume(channel, chatID, senderID string) error
    GetSessionSenderID(channel, chatID string) (string, error)
    ListPendingResumes() ([]PendingResume, error)
    ClearPendingResume(channel, chatID string) error
}

// ─── MemoryStore: memory domain (core_memory + long_term + event_history) ───
// Conceptually independent — could use its own backend.
type MemoryStore interface {
    // Core memory blocks
    InitBlocks(tenantID int64, userID string) error
    GetBlock(tenantID int64, blockName, userID string) (content string, charLimit int, err error)
    SetBlock(tenantID int64, blockName, content, userID string) error
    GetAllBlocks(tenantID int64, userID string) (map[string]string, error)
    ClearBlock(tenantID int64, blockName, userID string) error
    ClearAllBlocks(tenantID int64, userID string) error

    // Long-term memory + state
    ReadLongTerm(ctx context.Context, tenantID int64) (string, error)
    WriteLongTerm(ctx context.Context, tenantID int64, content string) error
    ClearLongTerm(ctx context.Context, tenantID int64) error
    GetState(ctx context.Context, tenantID int64) (lastConsolidated int, err error)
    SetState(ctx context.Context, tenantID int64, lastConsolidated int) error

    // Event history
    AppendHistory(ctx context.Context, tenantID int64, entry string) error
    GetHistoryEntries(ctx context.Context, tenantID int64, limit int) ([]string, error)
    ClearHistory(ctx context.Context, tenantID int64) error
}

// ─── ConfigStore: user/LLM config + scheduling + sharing ───
// All sender_id-keyed tables with no FK to tenants.
type ConfigStore interface {
    // Web UI authentication (web_users) — embedded: SQLite and PG each
    // implement it, so the web channel never touches *sql.DB for auth.
    WebAuthStore

    // LLM subscriptions
    ListSubscriptions(senderID string) ([]*LLMSubscription, error)
    GetSubscription(id string) (*LLMSubscription, error)
    GetDefaultSubscription(senderID string) (*LLMSubscription, error)
    AddSubscription(sub *LLMSubscription) error
    UpdateSubscription(sub *LLMSubscription) error
    RemoveSubscription(id string) error
    SetDefaultSubscription(id string) error
    RenameSubscription(id, name string) error
    SetSubscriptionEnabled(id string, enabled bool) error

    // User profile + settings
    GetUserProfile(senderID string) (name, profile string, err error)
    SaveUserProfile(senderID, name, profile string) error
    GetUserSettings(channel, senderID string) (map[string]string, error)
    SetUserSetting(channel, senderID, key, value string) error
    DeleteUserSetting(channel, senderID, key string) error

    // Token usage
    RecordUsage(senderID, model string, inputTokens, outputTokens, cachedTokens, convCount, llmCallCount int) error
    GetUsage(senderID string) (*UserTokenUsage, error)
    GetDailyUsage(senderID string, days int) ([]DailyTokenUsage, error)

    // Cron
    AddCronJob(job *CronJob) error
    RemoveCronJob(id string) error
    GetCronJob(id string) (*CronJob, error)
    ListCronJobsBySender(senderID string) ([]*CronJob, error)
    ListAllCronJobs() ([]*CronJob, error)
    ListCronJobsByChannelChatID(channel, chatID string) ([]*CronJob, error)
    UpdateCronNextRun(id string, nextRun time.Time) error

    // Triggers
    AddTrigger(t *event.Trigger) error
    RemoveTrigger(id string) error
    GetTrigger(id string) (*event.Trigger, error)
    ListTriggersByEventType(eventType string) ([]*event.Trigger, error)
    ListTriggersBySender(senderID string) ([]*event.Trigger, error)
    UpdateTriggerEnabled(id string, enabled bool) error
    RecordTriggerFire(id string, at time.Time) error

    // Sharing
    CreateSharedArtifact(a *SharedArtifact) error
    GetSharedArtifact(token string) (*SharedArtifact, error)
    RevokeSharedArtifact(token string, createdBy int64) error
    ListSharedArtifactsByCreator(createdBy int64) ([]SharedArtifact, error)
}
```

## What Changed vs v1

| v1 | v2 |
|---|---|
| `Store` interface with `Conn() *sql.DB` | **删了**——不暴露 `*sql.DB` |
| `RegistryStore`（60 方法合一） | 拆成 `TenantStore`（~25）+ `ConfigStore`（~35）——职责更清 |
| `RecordUsage(conn *sql.DB, ...)` | `RecordUsage(senderID, ...)`——删了 `*sql.DB` 参数 |
| `historyLock`/`writeMu` 在接口里 | **不在接口里**——实现内部细节 |

## PG 实现自由度

接口只定义**功能契约**。PG 实现可以：
- 用 `$N` 占位符（不需要 `?` 重写器）
- 用 `ON CONFLICT DO NOTHING`（不需要 `INSERT OR IGNORE`）
- 用 `RETURNING id`（不需要 `LastInsertId`）
- 用 `tsvector` + `ts_rank`（不需要 FTS5/bm25）
- 用 `now()` / `extract(epoch from ...)` / `ln()`（不需要 `datetime('now')`/`julianday`/`log`）
- 用 MVCC（不需要 `writeMu`/`historyLock`——这些方法在 PG 实现里是 no-op 或不存在）
- 用 PG 原生 schema（`BIGINT GENERATED ALWAYS AS IDENTITY`、`timestamptz`、`jsonb` 等）
- per-session DB 用 `CREATE DATABASE` 或 shared + `tenant_id` 过滤——接口不关心

## Definition of Done

- [x] `storage/interfaces.go`：5 个接口定义（纯接口，~220 行）
- [x] SQLite 现有类型满足接口（`var _ SessionStore = (*sqliteSessionStore)(nil)` 断言 —— `sqliteSessionStore` 嵌入 `*sqlite.SessionService`，新增方法由嵌入自动满足，adapter 零改动）
- [x] `go build ./...` 通过
- [x] PG 实现满足接口（`newPGSessionStore` 返回 `SessionStore` ⇒ 编译时强制）
- [x] `TenantSession` 完全不依赖 `*sqlite.SessionService` / `*sqlite.TenantService`（字段类型 = `storage.SessionStore` / `storage.TenantStore`）
- [x] PG 后端真实连接验证：`TestPGStorage_Smoke` + `TestPGStorage_SessionStoreSurface`（后者的「表面测试」覆盖新增 19 个方法 —— `$N` 占位符 / 参数类型推断 / 列名 / 控制行形状只能在真实连接上暴露）
- [x] PG 后端真实启动验证（server `serve` + `storage.type=pg` 无 panic；cron / subscription / settings 初始化按 nil 守卫跳过）

## PG 已知简化（明确不做 100% 对等 —— PG 路径为后端对齐/测试用）

| 能力 | SQLite | PG |
|---|---|---|
| 每会话写锁 / historyLock | 有 | 无（共享连接按语句串行） |
| 控制记录语义校验（mask/context_edit 目标存活、单 pending AskUser） | 有 | 无 |
| `AppendMasks` | 落 mask 控制记录 | no-op（PG replay 不折叠 mask；强制 flat provider） |
| `Replay` | 折叠 compress 快照 / mask / context_edit | 只过滤 control + display_only 行（快照不折叠） |
| `GetTenantUsageStats` | 聚合 + per-model + recent rows + tenant_state | 只聚合 `iteration_history` 主体数字 |
| `GetHistoryBeforeForDisplay` | display 投影（含 pre-compression + `[Compacted context]` 标记） | 原始行窗口 + COUNT |
| cron / subscription / settings 服务 | 经主库 `*sqlite.DB` | 不初始化（initServices nil 守卫跳过） |
| `SessionServiceForSQLite`（regions/detail 段查询、AskUser 控制记录读取） | 可用 | 返回明确错误（绝不静默降级） |
