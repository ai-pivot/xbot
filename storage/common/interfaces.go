package common

import (
	"context"
	"time"

	"xbot/event"
	"xbot/llm"
	"xbot/storage/agentmem"
)

// Storage is the top-level entry point. One instance per process.
// SQLite impl: wraps main DB + per-session DB pool (v71).
// PG impl: wraps main connection + per-session databases (or shared pool).
type Storage interface {
	Session(tenantID int64) (SessionStore, error)
	Tenant() TenantStore
	Memory() MemoryStore
	Config() ConfigStore
	// Cron is the scheduled-job store (cron_jobs). It lives on Storage — not on
	// ConfigStore — because the scheduler and the Cron tool both need it, and it
	// must be reachable on every backend (a concrete *CronService in those
	// layers is what made cron disappear on PG).
	Cron() CronStore
	// Trigger is the webhook event-trigger store (event_triggers), reached by the
	// event router. Same rationale as Cron: the router must not hold a SQLite
	// handle, or webhooks stop working on PG.
	Trigger() event.TriggerStore
	// OAuth is the OAuth credential store (oauth_tokens). On Storage so the OAuth
	// server can run on every backend — it previously required a SQLite file and
	// was silently disabled on PG.
	OAuth() OAuthTokenStore
	// InteractiveAgents is the interactive SubAgent registry (interactive_agents).
	// It lives on Storage because the agent must be able to rebuild the sub-agent
	// tree after a restart on EVERY backend — the previous code called
	// `multiSession.DB().Conn()` directly, which dereferences a nil handle on PG
	// and panics during startup reconcile.
	InteractiveAgents() InteractiveAgentStore
	// CopySessionFrom copies srcTenantID's conversation history (session_messages
	// + iteration_history) into dstTenantID, returning the message count. This
	// exists for session fork, which is a data copy — not an application-level
	// transformation. A backend that can express it as `INSERT … SELECT` must do
	// so, so the rows never leave the database; use it instead of read-then-write
	// whenever the caller does not need the messages in memory.
	CopySessionFrom(dstTenantID, srcTenantID int64) (int, error)
	Close() error
}

// SessionStore manages per-session data: messages and iteration history.
// SQLite: bound to a per-session DB file (v71 one-session-one-DB).
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
	// GetLastAssistantPreview returns the truncated text of the most recent
	// FINAL assistant reply (no tool calls), for push-notification previews.
	// Returns ("", nil) when the tenant has no final assistant reply.
	GetLastAssistantPreview(tenantID int64, maxRunes int) (string, error)
	HasAssistantReplyAfterLastUser(tenantID int64) (bool, error)

	// Turns
	GetMaxTurnID(tenantID int64) (uint64, error)
	GetLastUserTurnID(tenantID int64) (uint64, error)
	GetMaxIterationForTurn(tenantID int64, turnID uint64) (int, error)

	// Iteration history
	AppendIterationHistory(tenantID int64, msgID int64, turnID uint64, rec IterationRecord) error
	// AppendIterationHistories writes many records at once. Fork copies a whole
	// session's iteration history; a backend where each insert costs a network
	// round trip must pipeline them (see pgSessionStore).
	AppendIterationHistories(tenantID int64, recs []IterationRecord) error
	AppendIterationTool(tenantID int64, turnID uint64, iteration int, toolJSON string) (bool, error)
	GetIterationHistoryByTurn(tenantID int64, turnID uint64) ([]IterationRecord, error)
	GetIterationHistoryByTurns(tenantID int64, turnIDs []uint64) (map[uint64][]IterationRecord, error)
	// GetIterationHistoryBeforeRange returns the iterations of a turn that precede
	// beforeIter, ascending — the windowing endpoint's segment query.
	GetIterationHistoryBeforeRange(tenantID int64, turnID uint64, beforeIter int) ([]IterationRecord, error)
	// GetIterationHistoryByNumber locates ONE iteration by (turn, iteration).
	// Not-found is a normal business result: (zero, false, nil).
	GetIterationHistoryByNumber(tenantID int64, turnID uint64, iteration int) (IterationRecord, bool, error)
	// GetSyntheticPairRowsByTurn returns the turn's tool-pair candidate rows
	// (assistant rows carrying tool_calls + tool results, in id order) used to
	// repair histories written before synthetic tool pairs were persisted.
	GetSyntheticPairRowsByTurn(tenantID int64, turnID uint64) ([]llm.ChatMessage, error)
	// GetIterationAnchorsByTurn returns lightweight (iteration, created_at) anchors
	// for every iteration of a turn — the only basis for mapping a synthetic tool
	// pair's injection time onto an iteration number. Only those two fields are
	// populated; content/tools stay zero because anchors are never assembled as
	// full records.
	GetIterationAnchorsByTurn(tenantID int64, turnID uint64) ([]IterationRecord, error)
	// LatestAskControlRecord returns the newest ask_question / ask_answer control
	// record (id + type), or (0, "", nil) when the session has none. This is the
	// authority for "is a prompt still pending?" after a restart.
	LatestAskControlRecord(tenantID int64) (int64, HistoryRecordType, error)

	// Append-style writes (return the inserted row's stable history id).
	// AppendMessage differs from AddMessage: it is the append-only history
	// entry point (id is returned to the caller) whereas AddMessage is the
	// legacy fire-and-forget insert.
	AppendMessage(tenantID int64, msg llm.ChatMessage) (int64, error)
	AppendMessages(tenantID int64, messages []llm.ChatMessage) ([]int64, error)
	AppendMessagesAndAskQuestion(tenantID int64, messages []llm.ChatMessage, metadata map[string]string) ([]int64, int64, error)
	// AppendCommandMessage persists a `!cmd` / slash input-or-output row
	// (display-only, record_type='command' — never part of LLM context).
	AppendCommandMessage(tenantID int64, role, content string) (int64, error)

	// Control / snapshot records (compression, prune, context edit, AskUser).
	AppendControl(tenantID int64, recordType HistoryRecordType, targetHistoryID int64, data any) (int64, error)
	AppendContextSnapshot(tenantID int64, recordType HistoryRecordType, messages []llm.ChatMessage) (int64, error)
	AppendAskQuestion(tenantID int64, metadata map[string]string) (int64, error)
	AppendAskAnswer(tenantID int64, answer string) (int64, error)
	AppendAskAnswerWithUserMessage(tenantID int64, answer string, answerMsg llm.ChatMessage) (int64, error)
	AppendMasks(tenantID int64, mutations []MaskMutation) error

	// Iteration history (full set — the bounded/turn-scoped queries above).
	GetAllIterationHistory(tenantID int64) ([]IterationRecord, error)

	// Raw history records (append-only, includes control records).
	GetFullHistory(tenantID int64) ([]HistoryRecord, error)
	// GetHistoryBeforeForDisplay returns the display projection (pre-compression
	// messages included, `[Compacted context]` markers inserted) plus the total
	// display row count before beforeID.
	GetHistoryBeforeForDisplay(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, int, error)

	// Replay reconstructs the LLM-visible context from the append-only history
	// (control records applied: compression snapshots, masks, context edits).
	Replay(tenantID int64) (*ReplayResult, error)
	// RewindToHistoryID truncates history at (and including) a user message,
	// returning the target message, its 1-based turn index and the restored
	// prompt-token watermark.
	RewindToHistoryID(tenantID, historyID int64) (llm.ChatMessage, int, int64, error)

	// Usage / counts.
	GetUserMessageCount(tenantID int64) (int, error)
	// GetTenantUsageBuckets returns time-bucketed usage for the dashboard
	// (bucket width 60/3600/86400s, tzOffsetMinutes shifts the calendar).
	GetTenantUsageBuckets(tenantID int64, bucketSeconds int64, count int, tzOffsetMinutes int) ([]UsageBucket, error)
	GetTenantUsageStats(tenantID int64, recentLimit int) (*TenantUsageStats, error)
	GetLastUserMessageContextTokens(tenantID int64) (int64, error)
	// UpdateMessageContentNonDisplayOnly rewrites the Nth non-display-only
	// message (index aligned with GetAllMessages ordering).
	UpdateMessageContentNonDisplayOnly(tenantID int64, messageIndex int, content string) error

	// Session DB lifecycle (v71)
	HasLegacyRows(tenantID int64) (bool, error)
	LatestPreview(tenantID int64) (string, error)
}

// TenantStore manages the tenant registry: tenants, user_chats, pending_resumes,
// and per-session DB lifecycle metadata (v71). Lives in the main DB.

// TenantBackend manages per-session tenant model bindings.
// Embedded into TenantStore so both backends implement it.
type TenantBackend interface {
	GetTenantSubscription(channel, chatID string) (subscriptionID, model string, err error)
	SetTenantSubscription(channel, chatID, subscriptionID, model string) error
	ClearSubscriptionFromTenants(subID string) error
}

type TenantStore interface {
	TenantBackend
	// Tenant CRUD
	GetOrCreateTenantID(channel, chatID string) (int64, error)
	GetTenantIDByChannelChatID(channel, chatID string) (int64, error)
	GetTenantInfo(tenantID int64) (channel, chatID string, err error)
	DeleteTenant(tenantID int64) error
	DeleteTenantHistory(tenantID int64) error
	TouchTenantID(channel, chatID string) error

	// Per-session DB metadata (v71)
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
	UpdateChatSortOrders(channel, senderID string, orders map[string]int) error
	GetSenderForChat(channel, chatID string) (string, error)
	// GetChatLabel returns the user-assigned label of a session ("" when unset or
	// unknown). Backend-agnostic replacement for the direct user_chats SELECTs that
	// used to run against the main SQLite handle.
	GetChatLabel(channel, chatID string) (string, error)
	// GetChatOwner resolves the channel and sender that own a chat session,
	// searching across channels (chat_id is unique in practice). Returns empty
	// strings when the session is not registered — callers fall back to defaults.
	GetChatOwner(chatID string) (channel, senderID string, err error)

	// Tenant / channel discovery (used by the Web channel and its callbacks to
	// browse sessions without touching a backend-specific SQL handle).
	ListDistinctChannels() ([]string, error)
	ListTenants() ([]TenantInfo, error)

	// Pending resumes
	AddPendingResume(channel, chatID, senderID string) error
	GetSessionSenderID(channel, chatID string) (string, error)
	ListPendingResumes() ([]PendingResume, error)
	ClearPendingResume(channel, chatID string) error
}

// MemoryStore manages the memory domain: core memory blocks, long-term
// memory, and event history. Conceptually independent — could use its
// own backend (e.g., a dedicated memory DB or vector store).
type MemoryStore interface {
	// AgentMemoryStore is the official xbot provider's storage seam (its tables,
	// searches and decay/prune primitives). Embedded so one Storage handle serves
	// every memory provider without exposing a driver.
	AgentMemoryStore

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
	// GetHistoryEntriesInRange returns event_history rows within [start, end],
	// newest first (limit <= 0 means 20). This is the backend-agnostic replacement
	// for the former SQLite-only RecallTimeRangeFunc: both backends filter on
	// created_at and return the same ordering.
	GetHistoryEntriesInRange(ctx context.Context, tenantID int64, start, end time.Time, limit int) ([]HistoryEntry, error)
	GetHistoryCount(ctx context.Context, tenantID int64) (int64, error)
	ClearHistory(ctx context.Context, tenantID int64) error
	ClearState(ctx context.Context, tenantID int64) error
	GetTokenState(ctx context.Context, tenantID int64) (promptTokens, completionTokens int64, err error)
	SetTokenState(ctx context.Context, tenantID int64, promptTokens, completionTokens int64) error
	SetPersonaIsolation(enabled bool)
}

// HistoryEntry is one event_history row, as returned by the range query.
type HistoryEntry struct {
	Entry     string
	CreatedAt time.Time
}

// ─── Agent memory (the official "xbot" provider's storage) ───
//
// The xbot memory provider is DB-backed but must stay backend-agnostic: it owns
// the memory POLICY (what to extract, prompt text, heat/decay policy, Markdown
// mirrors) while every SQL statement lives in the storage layer. This interface
// is the seam; the shared types live in storage/agentmem so a provider can depend
// on them without importing a concrete backend.
//
// Design rules (see docs/plans/memory-providers-pg-parity.md):
//   - Search takes an ALREADY-TOKENIZED query (Terms) — the provider owns the
//     CJK/stopword tokenizer, the backend owns FTS syntax.
//   - Search returns ALREADY-ORDERED results — bm25 (lower = better) and
//     ts_rank (higher = better) never leak into the provider.
//   - No driver, DSN or dialect knob is exposed.

// AgentMemoryStore is the storage seam for the official xbot memory provider.
// It is embedded in MemoryStore, so a single Storage handle satisfies it.
type AgentMemoryStore interface {
	// EnsureAgentMemorySchema creates/migrates the agent-memory tables. Idempotent.
	EnsureAgentMemorySchema(ctx context.Context) error

	// Short-term memories.
	AddAgentShortTermMemory(ctx context.Context, mem agentmem.ShortTermMemory) (int64, error)
	SearchAgentShortTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.ShortTermMemory, error)
	EvictAgentShortTermMemories(ctx context.Context, userID, tenantID int64, keep int) error

	// Long-term memories.
	AddAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) (int64, error)
	SearchAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error)
	// FindAgentDuplicateLongTerm returns the id of an existing ACTIVE memory that
	// strongly matches every term (the dedup guard for auto-extraction), or 0.
	// The similarity threshold is a backend detail — it maps onto bm25 (SQLite)
	// or ts_rank (PG) internally, so no score scale leaks to the provider.
	FindAgentDuplicateLongTerm(ctx context.Context, q agentmem.Query) (int64, error)
	// AddAgentLongTermMemoryWithSupersede inserts the memory and, in ONE
	// transaction, marks older strongly-matching GLOBAL entries as superseded by
	// the new id (the supersede chain). Atomicity matters: a crash between the
	// two writes would leave contradictory facts both ACTIVE and both injected.
	// Superseding only happens for global scope (a task-local session fact must
	// never invalidate a user-level fact). Returns the new id and superseded ids.
	AddAgentLongTermMemoryWithSupersede(ctx context.Context, mem agentmem.LongTermMemory, terms []string) (int64, []int64, error)
	// SetAgentLongTermFilePath records the Markdown mirror path for a memory.
	SetAgentLongTermFilePath(ctx context.Context, id int64, filePath string) error
	ListAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error)
	GetAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) (agentmem.LongTermMemory, error)
	UpdateAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) error
	DeleteAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) error
	TouchAgentLongTermMemory(ctx context.Context, id int64) error
	// DecayAgentLongTerm applies heat decay and slow importance decay.
	DecayAgentLongTerm(ctx context.Context, userID, tenantID int64, halfLifeDays, forgetThreshold float64) error
	// PruneAgentLongTerm deletes the lowest-heat entries beyond maxEntries.
	PruneAgentLongTerm(ctx context.Context, userID, tenantID int64, maxEntries int) error
	// ListAgentLongTermMissingSearchText returns up to limit rows whose
	// search_text is empty (backfill driver — the transform is provider-side).
	ListAgentLongTermMissingSearchText(ctx context.Context, userID, tenantID int64, limit int) ([]agentmem.LongTermMemory, error)
	SetAgentLongTermSearchText(ctx context.Context, id int64, searchText string) error
}

// CronStore is the storage seam for scheduled jobs (cron_jobs).
//
// It exists so the scheduler and the Cron tool never hold a *CronService:
// a concrete SQLite type in those layers made cron silently unavailable on the PG
// backend (the whole wiring block was skipped when the main DB handle was nil).
type CronStore interface {
	AddJob(job *CronJob) error
	RemoveJob(id string) error
	// GetJob returns (nil, nil) when the job does not exist.
	GetJob(id string) (*CronJob, error)
	ListJobs() ([]*CronJob, error)
	ListAllJobs() ([]*CronJob, error)
	ListJobsByChannelChatID(channel, chatID string) ([]*CronJob, error)
	UpdateNextRun(id string, nextRun time.Time) error
	UpdateLastTrigger(id string, lastTrigger time.Time) error
	// MigrateFromJSON imports jobs from the legacy cron.json (idempotent: it is a
	// no-op once the table has rows). Backends without a legacy file ignore it.
	MigrateFromJSON(dataDir string) error
}

// InteractiveAgentStore is the storage seam for the interactive SubAgent
// registry (interactive_agents). The agent uses it to rebuild the sub-agent
// tree after a process restart, so it must work on every backend — a concrete
// *InteractiveAgentService built from `MultiTenantSession.DB()` panics
// on PG because there is no SQLite handle at all.
type InteractiveAgentStore interface {
	// Upsert inserts or updates one registry row (created_at is preserved).
	Upsert(rec InteractiveAgentRecord) error
	// Get returns ErrInteractiveAgentNotFound when the key is absent.
	Get(agentKey string) (*InteractiveAgentRecord, error)
	ListChildAgents(parentChannel, parentChatID string) ([]InteractiveAgentRecord, error)
	MarkStatus(agentKey, status string, turnID uint64) error
	// MarkAllRunningAsInterrupted flips every running row to interrupted and
	// returns the number of rows changed (startup reconcile step 1).
	MarkAllRunningAsInterrupted() (int64, error)
	MarkUnloadedCascade(agentKey string) error
	// MarkOrphanedIdleUnloaded flips idle rows whose session no longer exists
	// (and whose key is not live) to unloaded; returns rows changed.
	MarkOrphanedIdleUnloaded(liveKeys []string) (int64, error)
	ListActive(cutoff time.Time, includeUnloaded bool) ([]AgentActivity, error)
}

// WebUserInfo describes a web UI user account (for listing).
type WebUserInfo struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	CreatedAt string `json:"created_at"`
}

// WebAuthStore manages web UI user authentication (the web_users table).
// Both the SQLite and the PG backend implement it, so the web channel never
// touches *sql.DB or backend-specific SQL for authentication: on PG it talks
// to the PG-backed store (no SQLite file involved).
//
// It is embedded in ConfigStore so a single store handle satisfies both.
type WebAuthStore interface {
	// CreateWebUser inserts a user with an already-hashed password and returns
	// the new row id. Duplicate usernames surface the backend's UNIQUE
	// violation as an error (callers detect it by the UNIQUE constraint text).
	CreateWebUser(username, hashedPassword string) (int64, error)
	// GetWebUserForLogin returns the user id and stored password hash.
	GetWebUserForLogin(username string) (id int64, hashedPassword string, err error)
	// ListWebUsers returns every user ordered by id.
	ListWebUsers() ([]WebUserInfo, error)
	// DeleteWebUser removes a user; a missing username is an error.
	DeleteWebUser(username string) error
	// CountWebUsers returns the number of accounts (first-user bootstrap check).
	CountWebUsers() (int, error)
}

// ConfigStore manages user-level configuration: LLM subscriptions, user
// profiles/settings, token usage, cron jobs, event triggers, and shared
// artifacts. All sender_id-keyed, no FK to tenants.

// SubscriptionBackend manages LLM subscription models and defaults.
// Embedded into ConfigStore so both backends implement it.
type SubscriptionBackend interface {
	GetModel(subID, model string) (*SubscriptionModel, error)
	GetModels(subID string) ([]*SubscriptionModel, error)
	EnsureModel(subID, model string) error
	SetModelEnabled(subID, model string, enabled bool) error
	GetUserDefaultModel(senderID string) (*UserDefaultModel, error)
	SetUserDefaultModel(senderID, subID, model string) error
	GetSubscription(id string) (*LLMSubscription, error)
	GetDefault(senderID string) (*LLMSubscription, error)
	ListAllSubscriptions() ([]*LLMSubscription, error)
	UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error
	List(senderID string) ([]*LLMSubscription, error)
	ListAll() ([]*LLMSubscription, error)
	SetSubscriptionEnabled(id string, enabled bool) error
	Add(sub *LLMSubscription) error
	Update(sub *LLMSubscription) error
	Remove(id string) error
	Rename(id, name string) error
	SetDefault(id string) error
	SetModel(id, model string) error
	SetModelMaxContext(subID, model string, maxCtx int) error
	SetModelMaxOutput(subID, model string, maxOut int) error
	RemoveModel(subID, model string) error
	Get(id string) (*LLMSubscription, error)
	ClearUserDefaultModel(senderID string) error
	SetModelVisionConfig(subID, model string, vision bool, visionDetail string) error
	UpdatePerModelConfigs(id string, configs map[string]PerModelConfig) error
}

type ConfigStore interface {
	SubscriptionBackend
	// Web UI authentication (web_users)
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
	UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error

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
	GetAllUsage() ([]UserTokenUsage, error)
	GetDailyUsageSummary(senderID string, days int) ([]DailyTokenUsage, error)

	// Push delivery targets (push_targets)
	PushStore
}

// PushTarget is one registered delivery endpoint for the unified push layer.
// The provider decides what Address means:
//   - fcm:     FCM registration token
//   - apns:    APNs device token
//   - webpush: browser PushSubscription endpoint URL (Metadata holds keys)
//   - feishu:  Feishu chat_id / open_id
type PushTarget struct {
	ID         int64
	UserID     int64
	SenderID   string // web-<userID> (the session owner)
	Provider   string // fcm | apns | webpush | feishu
	Platform   string // android | ios | web | im
	Address    string
	Metadata   map[string]string // provider-specific (WebPush p256dh/auth, device info)
	DeviceName string
	AppVersion string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

// PushStore persists push delivery targets (the push_targets table).
// Keyed by sender_id (the session owner), not by tenant.
type PushStore interface {
	// UpsertPushTarget inserts or updates by (provider, address). Re-registering
	// the same address under a different sender re-binds its ownership.
	UpsertPushTarget(t *PushTarget) error
	// DeletePushTarget removes one of userID's targets. A missing row is not an
	// error (the client may unregister twice).
	DeletePushTarget(userID int64, provider, address string) error
	// DeletePushTargetByID removes a target the provider reported as dead.
	DeletePushTargetByID(id int64) error
	ListPushTargetsBySender(senderID string) ([]PushTarget, error)
	// TouchPushTargets updates last_seen_at for delivered targets (best effort).
	TouchPushTargets(ids []int64, at time.Time) error

	// Push settings are a single encrypted JSON blob (crypto.Encrypt at rest).
	// GetPushSettings returns "" when the table has no row yet (bootstrap from
	// config.push is then used and saved by the push service).
	GetPushSettings() (string, error)
	SavePushSettings(configBlob string) error
}

// OAuthToken is one stored OAuth credential, in backend-neutral form.
// Times are native (SQLite stores unix seconds, PG stores TIMESTAMPTZ — the
// backend converts, callers never see the difference).
type OAuthToken struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	Scopes       []string
	Raw          map[string]any
}

// OAuthTokenStore persists OAuth credentials (the oauth_tokens table).
//
// It lives in the storage layer so OAuth does not depend on a SQLite file: on the
// PG backend there is no SQLite main DB at all, and the previous wiring silently
// disabled the whole OAuth server in that case.
type OAuthTokenStore interface {
	// GetOAuthToken returns (nil, nil) when no token is stored for the key.
	GetOAuthToken(provider, channel, chatID string) (*OAuthToken, error)
	SetOAuthToken(provider, channel, chatID string, token *OAuthToken) error
	DeleteOAuthToken(provider, channel, chatID string) error
	// CleanupExpiredOAuthTokens removes tokens that expired before cutoff.
	CleanupExpiredOAuthTokens(cutoff time.Time) error
}
