package storage

import (
	"context"
	"time"

	"xbot/event"
	"xbot/llm"
	"xbot/storage/sqlite"
)

// Storage is the top-level entry point. One instance per process.
// SQLite impl: wraps main DB + per-session DB pool (v71).
// PG impl: wraps main connection + per-session databases (or shared pool).
type Storage interface {
	Session(tenantID int64) (SessionStore, error)
	Tenant() TenantStore
	Memory() MemoryStore
	Config() ConfigStore
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
	HasAssistantReplyAfterLastUser(tenantID int64) (bool, error)

	// Turns
	GetMaxTurnID(tenantID int64) (uint64, error)
	GetLastUserTurnID(tenantID int64) (uint64, error)
	GetMaxIterationForTurn(tenantID int64, turnID uint64) (int, error)

	// Iteration history
	AppendIterationHistory(tenantID int64, msgID int64, turnID uint64, rec sqlite.IterationRecord) error
	AppendIterationTool(tenantID int64, turnID uint64, iteration int, toolJSON string) (bool, error)
	GetIterationHistoryByTurn(tenantID int64, turnID uint64) ([]sqlite.IterationRecord, error)
	GetIterationHistoryByTurns(tenantID int64, turnIDs []uint64) (map[uint64][]sqlite.IterationRecord, error)

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
	AppendControl(tenantID int64, recordType sqlite.HistoryRecordType, targetHistoryID int64, data any) (int64, error)
	AppendContextSnapshot(tenantID int64, recordType sqlite.HistoryRecordType, messages []llm.ChatMessage) (int64, error)
	AppendAskQuestion(tenantID int64, metadata map[string]string) (int64, error)
	AppendAskAnswer(tenantID int64, answer string) (int64, error)
	AppendAskAnswerWithUserMessage(tenantID int64, answer string, answerMsg llm.ChatMessage) (int64, error)
	AppendMasks(tenantID int64, mutations []sqlite.MaskMutation) error

	// Iteration history (full set — the bounded/turn-scoped queries above).
	GetAllIterationHistory(tenantID int64) ([]sqlite.IterationRecord, error)

	// Raw history records (append-only, includes control records).
	GetFullHistory(tenantID int64) ([]sqlite.HistoryRecord, error)
	// GetHistoryBeforeForDisplay returns the display projection (pre-compression
	// messages included, `[Compacted context]` markers inserted) plus the total
	// display row count before beforeID.
	GetHistoryBeforeForDisplay(tenantID int64, beforeID int64, limit int) ([]llm.ChatMessage, int, error)

	// Replay reconstructs the LLM-visible context from the append-only history
	// (control records applied: compression snapshots, masks, context edits).
	Replay(tenantID int64) (*sqlite.ReplayResult, error)
	// RewindToHistoryID truncates history at (and including) a user message,
	// returning the target message, its 1-based turn index and the restored
	// prompt-token watermark.
	RewindToHistoryID(tenantID, historyID int64) (llm.ChatMessage, int, int64, error)

	// Usage / counts.
	GetUserMessageCount(tenantID int64) (int, error)
	GetTenantUsageStats(tenantID int64, recentLimit int) (*sqlite.TenantUsageStats, error)
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
	GetTenantDBInfo(tenantID int64) (sqlite.TenantDBInfo, error)

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
	ListUserChats(channel, senderID, currentChatID string, offset, limit int) ([]sqlite.UserChatWithPreview, bool, error)
	UpdateChatSortOrders(channel, senderID string, orders map[string]int) error
	GetSenderForChat(channel, chatID string) (string, error)

	// Tenant / channel discovery (used by the Web channel and its callbacks to
	// browse sessions without touching a backend-specific SQL handle).
	ListDistinctChannels() ([]string, error)
	ListTenants() ([]sqlite.TenantInfo, error)

	// Pending resumes
	AddPendingResume(channel, chatID, senderID string) error
	GetSessionSenderID(channel, chatID string) (string, error)
	ListPendingResumes() ([]sqlite.PendingResume, error)
	ClearPendingResume(channel, chatID string) error
}

// MemoryStore manages the memory domain: core memory blocks, long-term
// memory, and event history. Conceptually independent — could use its
// own backend (e.g., a dedicated memory DB or vector store).
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
	GetHistoryCount(ctx context.Context, tenantID int64) (int64, error)
	ClearHistory(ctx context.Context, tenantID int64) error
	ClearState(ctx context.Context, tenantID int64) error
	GetTokenState(ctx context.Context, tenantID int64) (promptTokens, completionTokens int64, err error)
	SetTokenState(ctx context.Context, tenantID int64, promptTokens, completionTokens int64) error
	SetPersonaIsolation(enabled bool)
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
	GetModel(subID, model string) (*sqlite.SubscriptionModel, error)
	GetModels(subID string) ([]*sqlite.SubscriptionModel, error)
	EnsureModel(subID, model string) error
	SetModelEnabled(subID, model string, enabled bool) error
	GetUserDefaultModel(senderID string) (*sqlite.UserDefaultModel, error)
	SetUserDefaultModel(senderID, subID, model string) error
	GetSubscription(id string) (*sqlite.LLMSubscription, error)
	GetDefault(senderID string) (*sqlite.LLMSubscription, error)
	ListAllSubscriptions() ([]*sqlite.LLMSubscription, error)
	UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error
	List(senderID string) ([]*sqlite.LLMSubscription, error)
	ListAll() ([]*sqlite.LLMSubscription, error)
	SetSubscriptionEnabled(id string, enabled bool) error
	Add(sub *sqlite.LLMSubscription) error
	Update(sub *sqlite.LLMSubscription) error
	Remove(id string) error
	Rename(id, name string) error
	SetDefault(id string) error
	SetModel(id, model string) error
	SetModelMaxContext(subID, model string, maxCtx int) error
	SetModelMaxOutput(subID, model string, maxOut int) error
	RemoveModel(subID, model string) error
	Get(id string) (*sqlite.LLMSubscription, error)
	ClearUserDefaultModel(senderID string) error
	SetModelVisionConfig(subID, model string, vision bool, visionDetail string) error
	UpdatePerModelConfigs(id string, configs map[string]sqlite.PerModelConfig) error
}

type ConfigStore interface {
	SubscriptionBackend
	// Web UI authentication (web_users)
	WebAuthStore

	// LLM subscriptions
	ListSubscriptions(senderID string) ([]*sqlite.LLMSubscription, error)
	GetSubscription(id string) (*sqlite.LLMSubscription, error)
	GetDefaultSubscription(senderID string) (*sqlite.LLMSubscription, error)
	AddSubscription(sub *sqlite.LLMSubscription) error
	UpdateSubscription(sub *sqlite.LLMSubscription) error
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
	GetUsage(senderID string) (*sqlite.UserTokenUsage, error)
	GetDailyUsage(senderID string, days int) ([]sqlite.DailyTokenUsage, error)

	// Cron
	AddCronJob(job *sqlite.CronJob) error
	RemoveCronJob(id string) error
	GetCronJob(id string) (*sqlite.CronJob, error)
	ListCronJobsBySender(senderID string) ([]*sqlite.CronJob, error)
	ListAllCronJobs() ([]*sqlite.CronJob, error)
	ListCronJobsByChannelChatID(channel, chatID string) ([]*sqlite.CronJob, error)
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
	CreateSharedArtifact(a *sqlite.SharedArtifact) error
	GetSharedArtifact(token string) (*sqlite.SharedArtifact, error)
	RevokeSharedArtifact(token string, createdBy int64) error
	ListSharedArtifactsByCreator(createdBy int64) ([]sqlite.SharedArtifact, error)
	GetAllUsage() ([]sqlite.UserTokenUsage, error)
	GetDailyUsageSummary(senderID string, days int) ([]sqlite.DailyTokenUsage, error)
}
