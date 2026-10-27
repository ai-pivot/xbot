package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"xbot/crypto"
	"xbot/event"
	log "xbot/logger"
	"xbot/storage/agentmem"
	"xbot/storage/common"
)

// ─── Compile-time assertions: existing SQLite types satisfy the interfaces ───

// SessionService satisfies SessionStore (all methods already match).
// The only gap: SessionService methods use the concrete *DB for
// historyLock internally — that's an implementation detail, not on the
// interface.

// TenantStore is satisfied by a composition of TenantService + ChatService +
// pending_resume methods on *DB.

// MemoryStore is satisfied by a composition of CoreMemoryService + MemoryService.

// ConfigStore is satisfied by a composition of the remaining 7 services.

// ─── SQLite adapter: wraps existing services to satisfy the interfaces ───

// sqliteSessionStore wraps *SessionService to satisfy SessionStore.
// Zero overhead: the concrete type already has all methods; this just
// bridges the type system.
type sqliteSessionStore struct {
	*SessionService
}

// Compile-time check.
var _ SessionStore = (*sqliteSessionStore)(nil)

// ─── TenantStore ───

type sqliteTenantStore struct {
	tenants *TenantService
	chats   *ChatService
	db      *DB // for pending_resume methods (AddPendingResume etc.)
}

func NewSQLiteTenantStore(db *DB) TenantStore {
	return &sqliteTenantStore{
		tenants: NewTenantService(db),
		chats:   NewChatService(db),
		db:      db,
	}
}

func (s *sqliteTenantStore) GetOrCreateTenantID(channel, chatID string) (int64, error) {
	return s.tenants.GetOrCreateTenantID(channel, chatID)
}
func (s *sqliteTenantStore) GetTenantIDByChannelChatID(channel, chatID string) (int64, error) {
	return s.tenants.GetTenantIDByChannelChatID(channel, chatID)
}
func (s *sqliteTenantStore) GetTenantInfo(tenantID int64) (string, string, error) {
	return s.tenants.GetTenantInfo(tenantID)
}
func (s *sqliteTenantStore) DeleteTenant(tenantID int64) error {
	return s.tenants.DeleteTenant(tenantID)
}
func (s *sqliteTenantStore) DeleteTenantHistory(tenantID int64) error {
	return s.tenants.DeleteTenantHistory(tenantID)
}
func (s *sqliteTenantStore) TouchTenantID(channel, chatID string) error {
	return s.tenants.TouchTenantID(channel, chatID)
}
func (s *sqliteTenantStore) SetTenantDBPath(tenantID int64, dbPath string) error {
	return s.tenants.SetTenantDBPath(tenantID, dbPath)
}
func (s *sqliteTenantStore) SetTenantMigrated(tenantID int64) error {
	return s.tenants.SetTenantMigrated(tenantID)
}
func (s *sqliteTenantStore) GetTenantDBInfo(tenantID int64) (TenantDBInfo, error) {
	return s.tenants.GetTenantDBInfo(tenantID)
}
func (s *sqliteTenantStore) SetTenantSubscription(channel, chatID, subscriptionID, model string) error {
	return s.tenants.SetTenantSubscription(channel, chatID, subscriptionID, model)
}
func (s *sqliteTenantStore) GetTenantSubscription(channel, chatID string) (string, string, error) {
	return s.tenants.GetTenantSubscription(channel, chatID)
}
func (s *sqliteTenantStore) SetTenantRunner(channel, chatID, runnerID string) error {
	return s.tenants.SetTenantRunner(channel, chatID, runnerID)
}
func (s *sqliteTenantStore) GetTenantRunner(channel, chatID string) (string, error) {
	return s.tenants.GetTenantRunner(channel, chatID)
}
func (s *sqliteTenantStore) ClearSubscriptionFromTenants(subID string) error {
	return s.tenants.ClearSubscriptionFromTenants(subID)
}
func (s *sqliteTenantStore) SetTenantCWD(tenantID int64, cwd string) error {
	return s.tenants.SetTenantCWD(tenantID, cwd)
}
func (s *sqliteTenantStore) GetTenantCWD(tenantID int64) (string, error) {
	return s.tenants.GetTenantCWD(tenantID)
}
func (s *sqliteTenantStore) SetTenantPreview(tenantID int64, content string) error {
	return s.tenants.SetTenantPreview(tenantID, content)
}
func (s *sqliteTenantStore) CreateChat(channel, senderID, label string) (string, error) {
	return s.chats.CreateChat(channel, senderID, label)
}
func (s *sqliteTenantStore) DeleteChat(channel, senderID, chatID string) error {
	return s.chats.DeleteChat(channel, senderID, chatID)
}
func (s *sqliteTenantStore) RenameChat(channel, senderID, chatID, label string) error {
	return s.chats.RenameChat(channel, senderID, chatID, label)
}
func (s *sqliteTenantStore) ListUserChats(channel, senderID, currentChatID string, offset, limit int) ([]UserChatWithPreview, bool, error) {
	return s.chats.ListUserChats(channel, senderID, currentChatID, offset, limit)
}
func (s *sqliteTenantStore) UpdateChatSortOrders(channel, senderID string, orders map[string]int) error {
	return s.chats.UpdateChatSortOrders(channel, senderID, orders)
}
func (s *sqliteTenantStore) GetSenderForChat(channel, chatID string) (string, error) {
	return s.chats.GetSenderForChat(channel, chatID)
}
func (s *sqliteTenantStore) GetChatLabel(channel, chatID string) (string, error) {
	return s.chats.GetChatLabel(channel, chatID)
}
func (s *sqliteTenantStore) GetChatOwner(chatID string) (string, string, error) {
	return s.chats.GetChatOwner(chatID)
}
func (s *sqliteTenantStore) ListDistinctChannels() ([]string, error) {
	rows, err := s.db.Conn().Query(`SELECT DISTINCT channel FROM tenants WHERE chat_id != '_shared' ORDER BY channel`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var channels []string
	for rows.Next() {
		var ch string
		if err := rows.Scan(&ch); err != nil {
			return nil, err
		}
		channels = append(channels, ch)
	}
	return channels, rows.Err()
}
func (s *sqliteTenantStore) ListTenants() ([]TenantInfo, error) {
	return s.tenants.ListTenants()
}
func (s *sqliteTenantStore) AddPendingResume(channel, chatID, senderID string) error {
	return s.db.AddPendingResume(channel, chatID, senderID)
}
func (s *sqliteTenantStore) GetSessionSenderID(channel, chatID string) (string, error) {
	return s.db.GetSessionSenderID(channel, chatID)
}
func (s *sqliteTenantStore) ListPendingResumes() ([]PendingResume, error) {
	return s.db.ListPendingResumes()
}
func (s *sqliteTenantStore) ClearPendingResume(channel, chatID string) error {
	return s.db.ClearPendingResume(channel, chatID)
}

// Compile-time check.
var _ TenantStore = (*sqliteTenantStore)(nil)

// ─── MemoryStore ───

type sqliteMemoryStore struct {
	core  *CoreMemoryService
	mem   *MemoryService
	agent *AgentMemoryService
}

func NewSQLiteMemoryStore(db *DB) MemoryStore {
	return &sqliteMemoryStore{
		core:  NewCoreMemoryService(db),
		mem:   NewMemoryService(db),
		agent: NewAgentMemoryService(db),
	}
}

// NewSQLiteMemoryStoreFromConn builds a MemoryStore from a raw *sql.DB (tests and
// callers that hold only a connection). The core-memory and event-history
// services need the wrapped *DB, so those stay nil — only the
// agent-memory seam is guaranteed usable through this constructor.
func NewSQLiteMemoryStoreFromConn(conn *sql.DB) MemoryStore {
	return &sqliteMemoryStore{agent: NewAgentMemoryServiceFromConn(conn)}
}

// ── AgentMemoryStore (official xbot provider) — delegate to the sqlite service ──

func (s *sqliteMemoryStore) EnsureAgentMemorySchema(ctx context.Context) error {
	return s.agent.EnsureAgentMemorySchema(ctx)
}
func (s *sqliteMemoryStore) AddAgentShortTermMemory(ctx context.Context, mem agentmem.ShortTermMemory) (int64, error) {
	return s.agent.AddAgentShortTermMemory(ctx, mem)
}
func (s *sqliteMemoryStore) SearchAgentShortTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.ShortTermMemory, error) {
	return s.agent.SearchAgentShortTermMemories(ctx, q)
}
func (s *sqliteMemoryStore) EvictAgentShortTermMemories(ctx context.Context, userID, tenantID int64, keep int) error {
	return s.agent.EvictAgentShortTermMemories(ctx, userID, tenantID, keep)
}
func (s *sqliteMemoryStore) AddAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) (int64, error) {
	return s.agent.AddAgentLongTermMemory(ctx, mem)
}
func (s *sqliteMemoryStore) SearchAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error) {
	return s.agent.SearchAgentLongTermMemories(ctx, q)
}
func (s *sqliteMemoryStore) FindAgentDuplicateLongTerm(ctx context.Context, q agentmem.Query) (int64, error) {
	return s.agent.FindAgentDuplicateLongTerm(ctx, q)
}
func (s *sqliteMemoryStore) SupersedeAgentLongTerm(ctx context.Context, q agentmem.Query, newID int64) ([]int64, error) {
	return s.agent.SupersedeAgentLongTerm(ctx, q, newID)
}
func (s *sqliteMemoryStore) ListAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error) {
	return s.agent.ListAgentLongTermMemories(ctx, q)
}
func (s *sqliteMemoryStore) GetAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) (agentmem.LongTermMemory, error) {
	return s.agent.GetAgentLongTermMemory(ctx, userID, tenantID, id)
}
func (s *sqliteMemoryStore) UpdateAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) error {
	return s.agent.UpdateAgentLongTermMemory(ctx, mem)
}
func (s *sqliteMemoryStore) DeleteAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) error {
	return s.agent.DeleteAgentLongTermMemory(ctx, userID, tenantID, id)
}
func (s *sqliteMemoryStore) TouchAgentLongTermMemory(ctx context.Context, id int64) error {
	return s.agent.TouchAgentLongTermMemory(ctx, id)
}
func (s *sqliteMemoryStore) DecayAgentLongTerm(ctx context.Context, userID, tenantID int64, halfLifeDays, forgetThreshold float64) error {
	return s.agent.DecayAgentLongTerm(ctx, userID, tenantID, halfLifeDays, forgetThreshold)
}
func (s *sqliteMemoryStore) PruneAgentLongTerm(ctx context.Context, userID, tenantID int64, maxEntries int) error {
	return s.agent.PruneAgentLongTerm(ctx, userID, tenantID, maxEntries)
}
func (s *sqliteMemoryStore) ListAgentLongTermMissingSearchText(ctx context.Context, userID, tenantID int64, limit int) ([]agentmem.LongTermMemory, error) {
	return s.agent.ListAgentLongTermMissingSearchText(ctx, userID, tenantID, limit)
}
func (s *sqliteMemoryStore) SetAgentLongTermSearchText(ctx context.Context, id int64, searchText string) error {
	return s.agent.SetAgentLongTermSearchText(ctx, id, searchText)
}
func (s *sqliteMemoryStore) AddAgentLongTermMemoryWithSupersede(ctx context.Context, mem agentmem.LongTermMemory, terms []string) (int64, []int64, error) {
	return s.agent.AddAgentLongTermMemoryWithSupersede(ctx, mem, terms)
}
func (s *sqliteMemoryStore) SetAgentLongTermFilePath(ctx context.Context, id int64, filePath string) error {
	return s.agent.SetAgentLongTermFilePath(ctx, id, filePath)
}

func (s *sqliteMemoryStore) InitBlocks(tenantID int64, userID string) error {
	return s.core.InitBlocks(tenantID, userID)
}
func (s *sqliteMemoryStore) GetBlock(tenantID int64, blockName, userID string) (string, int, error) {
	return s.core.GetBlock(tenantID, blockName, userID)
}
func (s *sqliteMemoryStore) SetBlock(tenantID int64, blockName, content, userID string) error {
	return s.core.SetBlock(tenantID, blockName, content, userID)
}
func (s *sqliteMemoryStore) GetAllBlocks(tenantID int64, userID string) (map[string]string, error) {
	return s.core.GetAllBlocks(tenantID, userID)
}
func (s *sqliteMemoryStore) ClearBlock(tenantID int64, blockName, userID string) error {
	return s.core.ClearBlock(tenantID, blockName, userID)
}
func (s *sqliteMemoryStore) ClearAllBlocks(tenantID int64, userID string) error {
	return s.core.ClearAllBlocks(tenantID, userID)
}
func (s *sqliteMemoryStore) ReadLongTerm(ctx context.Context, tenantID int64) (string, error) {
	return s.mem.ReadLongTerm(ctx, tenantID)
}
func (s *sqliteMemoryStore) WriteLongTerm(ctx context.Context, tenantID int64, content string) error {
	return s.mem.WriteLongTerm(ctx, tenantID, content)
}
func (s *sqliteMemoryStore) ClearLongTerm(ctx context.Context, tenantID int64) error {
	return s.mem.ClearLongTerm(ctx, tenantID)
}
func (s *sqliteMemoryStore) GetState(ctx context.Context, tenantID int64) (int, error) {
	return s.mem.GetState(ctx, tenantID)
}
func (s *sqliteMemoryStore) SetState(ctx context.Context, tenantID int64, lastConsolidated int) error {
	return s.mem.SetState(ctx, tenantID, lastConsolidated)
}
func (s *sqliteMemoryStore) AppendHistory(ctx context.Context, tenantID int64, entry string) error {
	return s.mem.AppendHistory(ctx, tenantID, entry)
}
func (s *sqliteMemoryStore) GetHistoryEntries(ctx context.Context, tenantID int64, limit int) ([]string, error) {
	return s.mem.GetHistoryEntries(ctx, tenantID, limit)
}
func (s *sqliteMemoryStore) GetHistoryEntriesInRange(ctx context.Context, tenantID int64, start, end time.Time, limit int) ([]HistoryEntry, error) {
	rows, err := s.mem.GetHistoryEntriesInRange(ctx, tenantID, start, end, limit)
	if err != nil {
		return nil, err
	}
	out := make([]HistoryEntry, len(rows))
	for i, r := range rows {
		out[i] = HistoryEntry{Entry: r.Entry, CreatedAt: r.CreatedAt}
	}
	return out, nil
}
func (s *sqliteMemoryStore) ClearHistory(ctx context.Context, tenantID int64) error {
	return s.mem.ClearHistory(ctx, tenantID)
}
func (s *sqliteMemoryStore) GetHistoryCount(ctx context.Context, tenantID int64) (int64, error) {
	return s.mem.GetHistoryCount(ctx, tenantID)
}
func (s *sqliteMemoryStore) ClearState(ctx context.Context, tenantID int64) error {
	return s.mem.ClearState(ctx, tenantID)
}
func (s *sqliteMemoryStore) SetTokenState(ctx context.Context, tenantID int64, promptTokens, completionTokens int64) error {
	return s.mem.SetTokenState(ctx, tenantID, promptTokens, completionTokens)
}
func (s *sqliteMemoryStore) GetTokenState(ctx context.Context, tenantID int64) (int64, int64, error) {
	return s.mem.GetTokenState(ctx, tenantID)
}
func (s *sqliteMemoryStore) SetPersonaIsolation(enabled bool) {
	s.core.SetPersonaIsolation(enabled)
}

// Compile-time check.
var _ MemoryStore = (*sqliteMemoryStore)(nil)

// ─── ConfigStore ───

type sqliteConfigStore struct {
	subs     *LLMSubscriptionService
	profile  *UserProfileService
	settings *UserSettingsService
	tokens   *UserTokenUsageService
	cron     *CronService
	trigger  *TriggerService
	share    *ShareService
	db       *DB // for RecordUsage which needs *sql.DB
	webAuth  WebAuthStore
}

func NewSQLiteConfigStore(db *DB) ConfigStore {
	return &sqliteConfigStore{
		subs:     NewLLMSubscriptionService(db),
		profile:  NewUserProfileService(db),
		settings: NewUserSettingsService(db),
		tokens:   NewUserTokenUsageService(db),
		cron:     NewCronService(db),
		trigger:  NewTriggerService(db),
		share:    NewShareService(db),
		db:       db,
		webAuth:  NewSQLiteWebAuthStoreFromConn(db.Conn()),
	}
}

// LLM subscriptions
func (s *sqliteConfigStore) ListSubscriptions(senderID string) ([]*LLMSubscription, error) {
	return s.subs.List(senderID)
}
func (s *sqliteConfigStore) GetSubscription(id string) (*LLMSubscription, error) {
	return s.subs.Get(id)
}
func (s *sqliteConfigStore) GetDefaultSubscription(senderID string) (*LLMSubscription, error) {
	return s.subs.GetDefault(senderID)
}
func (s *sqliteConfigStore) AddSubscription(sub *LLMSubscription) error {
	return s.subs.Add(sub)
}
func (s *sqliteConfigStore) UpdateSubscription(sub *LLMSubscription) error {
	return s.subs.Update(sub)
}
func (s *sqliteConfigStore) RemoveSubscription(id string) error {
	return s.subs.Remove(id)
}
func (s *sqliteConfigStore) SetDefaultSubscription(id string) error {
	return s.subs.SetDefault(id)
}
func (s *sqliteConfigStore) RenameSubscription(id, name string) error {
	return s.subs.Rename(id, name)
}
func (s *sqliteConfigStore) SetSubscriptionEnabled(id string, enabled bool) error {
	return s.subs.SetSubscriptionEnabled(id, enabled)
}

// User profile + settings
func (s *sqliteConfigStore) GetUserProfile(senderID string) (string, string, error) {
	return s.profile.GetProfile(senderID)
}
func (s *sqliteConfigStore) SaveUserProfile(senderID, name, profile string) error {
	return s.profile.SaveProfile(senderID, name, profile)
}
func (s *sqliteConfigStore) GetUserSettings(channel, senderID string) (map[string]string, error) {
	return s.settings.Get(channel, senderID)
}
func (s *sqliteConfigStore) SetUserSetting(channel, senderID, key, value string) error {
	return s.settings.Set(channel, senderID, key, value)
}
func (s *sqliteConfigStore) DeleteUserSetting(channel, senderID, key string) error {
	return s.settings.Delete(channel, senderID, key)
}

// Token usage
func (s *sqliteConfigStore) RecordUsage(senderID, model string, inputTokens, outputTokens, cachedTokens, convCount, llmCallCount int) error {
	return s.tokens.RecordUsage(s.db.Conn(), senderID, model, inputTokens, outputTokens, cachedTokens, convCount, llmCallCount)
}
func (s *sqliteConfigStore) GetUsage(senderID string) (*UserTokenUsage, error) {
	return s.tokens.GetUsage(senderID)
}
func (s *sqliteConfigStore) GetDailyUsage(senderID string, days int) ([]DailyTokenUsage, error) {
	return s.tokens.GetDailyUsage(senderID, days)
}
func (s *sqliteConfigStore) GetDailyUsageSummary(senderID string, days int) ([]DailyTokenUsage, error) {
	return s.tokens.GetDailyUsageSummary(senderID, days)
}
func (s *sqliteConfigStore) GetAllUsage() ([]UserTokenUsage, error) {
	return s.tokens.GetAllUsage()
}

// Cron
func (s *sqliteConfigStore) AddCronJob(job *CronJob) error {
	return s.cron.AddJob(job)
}
func (s *sqliteConfigStore) RemoveCronJob(id string) error {
	return s.cron.RemoveJob(id)
}
func (s *sqliteConfigStore) GetCronJob(id string) (*CronJob, error) {
	return s.cron.GetJob(id)
}
func (s *sqliteConfigStore) ListCronJobsBySender(senderID string) ([]*CronJob, error) {
	return s.cron.ListJobsBySender(senderID)
}
func (s *sqliteConfigStore) ListAllCronJobs() ([]*CronJob, error) {
	return s.cron.ListAllJobs()
}
func (s *sqliteConfigStore) ListCronJobsByChannelChatID(channel, chatID string) ([]*CronJob, error) {
	return s.cron.ListJobsByChannelChatID(channel, chatID)
}
func (s *sqliteConfigStore) UpdateCronNextRun(id string, nextRun time.Time) error {
	return s.cron.UpdateNextRun(id, nextRun)
}

// Triggers
func (s *sqliteConfigStore) AddTrigger(t *event.Trigger) error {
	return s.trigger.AddTrigger(t)
}
func (s *sqliteConfigStore) RemoveTrigger(id string) error {
	return s.trigger.RemoveTrigger(id)
}
func (s *sqliteConfigStore) GetTrigger(id string) (*event.Trigger, error) {
	return s.trigger.GetTrigger(id)
}
func (s *sqliteConfigStore) ListTriggersByEventType(eventType string) ([]*event.Trigger, error) {
	return s.trigger.ListByEventType(eventType)
}
func (s *sqliteConfigStore) ListTriggersBySender(senderID string) ([]*event.Trigger, error) {
	return s.trigger.ListBySender(senderID)
}
func (s *sqliteConfigStore) UpdateTriggerEnabled(id string, enabled bool) error {
	return s.trigger.UpdateEnabled(id, enabled)
}
func (s *sqliteConfigStore) RecordTriggerFire(id string, at time.Time) error {
	return s.trigger.RecordFire(id, at)
}

// Sharing
func (s *sqliteConfigStore) CreateSharedArtifact(a *SharedArtifact) error {
	return s.share.Create(a)
}
func (s *sqliteConfigStore) GetSharedArtifact(token string) (*SharedArtifact, error) {
	a, err := s.share.Get(token)
	if a == nil {
		return nil, nil // not-found/revoked → (nil, nil) — matches PG and the contract
	}
	return a, err
}
func (s *sqliteConfigStore) RevokeSharedArtifact(token string, createdBy int64) error {
	return s.share.Revoke(token, createdBy)
}
func (s *sqliteConfigStore) ListSharedArtifactsByCreator(createdBy int64) ([]SharedArtifact, error) {
	return s.share.ListByCreator(createdBy)
}

// ─── Push targets (push_targets) ───

func (s *sqliteConfigStore) UpsertPushTarget(t *PushTarget) error {
	if t == nil {
		return fmt.Errorf("nil push target")
	}
	meta, err := json.Marshal(t.Metadata)
	if err != nil {
		return fmt.Errorf("marshal push metadata: %w", err)
	}
	if len(t.Metadata) == 0 {
		meta = []byte("{}")
	}
	now := time.Now().Unix()
	_, err = s.db.Conn().Exec(`
	INSERT INTO push_targets
    (user_id, sender_id, provider, platform, address, metadata, device_name, app_version, created_at, last_seen_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(provider, address) DO UPDATE SET
    user_id = excluded.user_id,
    sender_id = excluded.sender_id,
    platform = excluded.platform,
    metadata = excluded.metadata,
    device_name = excluded.device_name,
    app_version = excluded.app_version,
    last_seen_at = excluded.last_seen_at`,
		t.UserID, t.SenderID, t.Provider, t.Platform, t.Address, string(meta),
		t.DeviceName, t.AppVersion, now, now)
	if err != nil {
		return fmt.Errorf("upsert push target: %w", err)
	}
	return nil
}

func (s *sqliteConfigStore) DeletePushTarget(userID int64, provider, address string) error {
	_, err := s.db.Conn().Exec(
		`DELETE FROM push_targets WHERE user_id = ? AND provider = ? AND address = ?`,
		userID, provider, address)
	if err != nil {
		return fmt.Errorf("delete push target: %w", err)
	}
	return nil
}

func (s *sqliteConfigStore) DeletePushTargetByID(id int64) error {
	_, err := s.db.Conn().Exec(`DELETE FROM push_targets WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete push target by id: %w", err)
	}
	return nil
}

func (s *sqliteConfigStore) ListPushTargetsBySender(senderID string) ([]PushTarget, error) {
	rows, err := s.db.Conn().Query(`
	SELECT id, user_id, sender_id, provider, platform, address, metadata,
       device_name, app_version, created_at, last_seen_at
	FROM push_targets WHERE sender_id = ? ORDER BY id`, senderID)
	if err != nil {
		return nil, fmt.Errorf("list push targets: %w", err)
	}
	defer rows.Close()

	out := make([]PushTarget, 0, 4)
	for rows.Next() {
		var t PushTarget
		var meta string
		var created, seen int64
		if err := rows.Scan(&t.ID, &t.UserID, &t.SenderID, &t.Provider, &t.Platform,
			&t.Address, &meta, &t.DeviceName, &t.AppVersion, &created, &seen); err != nil {
			return nil, fmt.Errorf("scan push target: %w", err)
		}
		t.Metadata = map[string]string{}
		if meta != "" {
			if err := json.Unmarshal([]byte(meta), &t.Metadata); err != nil {
				return nil, fmt.Errorf("unmarshal push metadata: %w", err)
			}
		}
		t.CreatedAt = time.Unix(created, 0)
		t.LastSeenAt = time.Unix(seen, 0)
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate push targets: %w", err)
	}
	return out, nil
}

func (s *sqliteConfigStore) TouchPushTargets(ids []int64, at time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := ""
	for i := range ids {
		if i > 0 {
			placeholders += ","
		}
		placeholders += "?"
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, at.Unix())
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := s.db.Conn().Exec(
		`UPDATE push_targets SET last_seen_at = ? WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		return fmt.Errorf("touch push targets: %w", err)
	}
	return nil
}

// ─── Push settings (push_settings) ───

func (s *sqliteConfigStore) GetPushSettings() (string, error) {
	var raw string
	err := s.db.Conn().QueryRow(`SELECT config FROM push_settings WHERE id = 1`).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get push settings: %w", err)
	}
	return raw, nil
}

func (s *sqliteConfigStore) SavePushSettings(blob string) error {
	_, err := s.db.Conn().Exec(`
INSERT INTO push_settings (id, config, updated_at) VALUES (1, ?, ?)
ON CONFLICT(id) DO UPDATE SET config = excluded.config, updated_at = excluded.updated_at`,
		blob, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("save push settings: %w", err)
	}
	return nil
}

// ─── Web UI authentication (web_users) ───
//
// Backend-specific SQL lives here (not in channel/web): the web channel only
// talks to storage.WebAuthStore, so the same code path serves SQLite and PG.
// The SQL is byte-for-byte the one previously inlined in channel/web/web_auth.go
// (? placeholders, LastInsertId) — SQLite behaviour is unchanged.

// sqliteWebAuthStore implements WebAuthStore on a raw SQLite connection.
type sqliteWebAuthStore struct {
	conn *sql.DB
}

// NewSQLiteWebAuthStoreFromConn wraps an already-open SQLite connection as a
// WebAuthStore. The SQLite storage adapter builds one from its main DB; tests
// that already hold a connection use it directly.
func NewSQLiteWebAuthStoreFromConn(conn *sql.DB) WebAuthStore {
	return &sqliteWebAuthStore{conn: conn}
}

func (s *sqliteWebAuthStore) CreateWebUser(username, hashedPassword string) (int64, error) {
	res, err := s.conn.Exec(
		"INSERT INTO web_users (username, password) VALUES (?, ?)",
		username, hashedPassword,
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func (s *sqliteWebAuthStore) GetWebUserForLogin(username string) (int64, string, error) {
	var id int64
	var hashed string
	err := s.conn.QueryRow(
		"SELECT id, password FROM web_users WHERE username = ?", username,
	).Scan(&id, &hashed)
	if err != nil {
		return 0, "", err
	}
	return id, hashed, nil
}

func (s *sqliteWebAuthStore) ListWebUsers() ([]WebUserInfo, error) {
	rows, err := s.conn.Query("SELECT id, username, created_at FROM web_users ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var users []WebUserInfo
	for rows.Next() {
		var u WebUserInfo
		if err := rows.Scan(&u.ID, &u.Username, &u.CreatedAt); err != nil {
			log.WithError(err).Warn("web_users row scan failed")
			continue
		}
		users = append(users, u)
	}
	if users == nil {
		users = []WebUserInfo{}
	}
	return users, nil
}

func (s *sqliteWebAuthStore) DeleteWebUser(username string) error {
	res, err := s.conn.Exec("DELETE FROM web_users WHERE username = ?", username)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("user %q not found", username)
	}
	return nil
}

func (s *sqliteWebAuthStore) CountWebUsers() (int, error) {
	var n int
	if err := s.conn.QueryRow("SELECT COUNT(*) FROM web_users").Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// Compile-time check.
var _ WebAuthStore = (*sqliteWebAuthStore)(nil)

// sqliteConfigStore forwards the WebAuthStore methods to webAuth.
func (s *sqliteConfigStore) CreateWebUser(username, hashedPassword string) (int64, error) {
	return s.webAuth.CreateWebUser(username, hashedPassword)
}
func (s *sqliteConfigStore) GetWebUserForLogin(username string) (int64, string, error) {
	return s.webAuth.GetWebUserForLogin(username)
}
func (s *sqliteConfigStore) ListWebUsers() ([]WebUserInfo, error) {
	return s.webAuth.ListWebUsers()
}
func (s *sqliteConfigStore) DeleteWebUser(username string) error {
	return s.webAuth.DeleteWebUser(username)
}
func (s *sqliteConfigStore) CountWebUsers() (int, error) {
	return s.webAuth.CountWebUsers()
}

// Compile-time check.

// SubscriptionBackend wrappers (delegate to sub service)
func (s *sqliteConfigStore) GetModel(subID, model string) (*SubscriptionModel, error) {
	return s.subs.GetModel(subID, model)
}
func (s *sqliteConfigStore) GetModels(subID string) ([]*SubscriptionModel, error) {
	return s.subs.GetModels(subID)
}
func (s *sqliteConfigStore) EnsureModel(subID, model string) error {
	return s.subs.EnsureModel(subID, model)
}
func (s *sqliteConfigStore) SetModelEnabled(subID, model string, enabled bool) error {
	return s.subs.SetModelEnabled(subID, model, enabled)
}
func (s *sqliteConfigStore) GetUserDefaultModel(senderID string) (*UserDefaultModel, error) {
	return s.subs.GetUserDefaultModel(senderID)
}
func (s *sqliteConfigStore) SetUserDefaultModel(senderID, subID, model string) error {
	return s.subs.SetUserDefaultModel(senderID, subID, model)
}
func (s *sqliteConfigStore) GetDefault(senderID string) (*LLMSubscription, error) {
	return s.subs.GetDefault(senderID)
}
func (s *sqliteConfigStore) UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error {
	return s.subs.UpsertModel(subID, model, maxCtx, maxOut, thinking, apiType)
}
func (s *sqliteConfigStore) List(senderID string) ([]*LLMSubscription, error) {
	return s.subs.List(senderID)
}
func (s *sqliteConfigStore) ListAll() ([]*LLMSubscription, error) {
	return s.subs.ListAll()
}
func (s *sqliteConfigStore) ListAllSubscriptions() ([]*LLMSubscription, error) {
	return s.subs.ListAll()
}

func (s *sqliteConfigStore) Add(sub *LLMSubscription) error    { return s.subs.Add(sub) }
func (s *sqliteConfigStore) Update(sub *LLMSubscription) error { return s.subs.Update(sub) }
func (s *sqliteConfigStore) Remove(id string) error            { return s.subs.Remove(id) }
func (s *sqliteConfigStore) Rename(id, name string) error      { return s.subs.Rename(id, name) }
func (s *sqliteConfigStore) SetDefault(id string) error        { return s.subs.SetDefault(id) }
func (s *sqliteConfigStore) SetModel(id, model string) error   { return s.subs.SetModel(id, model) }
func (s *sqliteConfigStore) SetModelMaxContext(subID, model string, maxCtx int) error {
	return s.subs.SetModelMaxContext(subID, model, maxCtx)
}
func (s *sqliteConfigStore) SetModelMaxOutput(subID, model string, maxOut int) error {
	return s.subs.SetModelMaxOutput(subID, model, maxOut)
}
func (s *sqliteConfigStore) RemoveModel(subID, model string) error {
	return s.subs.RemoveModel(subID, model)
}
func (s *sqliteConfigStore) Get(id string) (*LLMSubscription, error) { return s.subs.Get(id) }
func (s *sqliteConfigStore) ClearUserDefaultModel(senderID string) error {
	return s.subs.ClearUserDefaultModel(senderID)
}

func (s *sqliteConfigStore) SetModelVisionConfig(subID, model string, vision bool, visionDetail string) error {
	return s.subs.SetModelVisionConfig(subID, model, vision, visionDetail)
}
func (s *sqliteConfigStore) UpdatePerModelConfigs(id string, configs map[string]PerModelConfig) error {
	return s.subs.UpdatePerModelConfigs(id, configs)
}

var _ ConfigStore = (*sqliteConfigStore)(nil)

// ─── Storage factory: ties everything together ───

// sqliteStorage implements Storage for the SQLite backend.
// It wraps a main DB + per-session DB pool (v71 one-session-one-DB).
type sqliteStorage struct {
	mainDB    *DB
	tenant    TenantStore
	memory    MemoryStore
	config    ConfigStore
	cron      CronStore
	trigger   event.TriggerStore
	oauth     OAuthTokenStore
	agents    InteractiveAgentStore
	sessionFn func(int64) (SessionStore, error)
	closeFn   func() error
}

// NewSQLiteStorage creates a Storage backed by SQLite.
// The caller provides the main DB handle and a function to open/create
// per-session stores (wired to the v71 per-session DB pool).
func NewSQLiteStorage(
	mainDB *DB,
	sessionFactory func(tenantID int64) (SessionStore, error),
	close func() error,
) Storage {
	return &sqliteStorage{
		mainDB:    mainDB,
		tenant:    NewSQLiteTenantStore(mainDB),
		memory:    NewSQLiteMemoryStore(mainDB),
		config:    NewSQLiteConfigStore(mainDB),
		cron:      NewCronService(mainDB),
		trigger:   NewTriggerService(mainDB),
		oauth:     &sqliteOAuthTokenStore{db: mainDB},
		agents:    newSQLiteInteractiveAgentStore(mainDB),
		sessionFn: sessionFactory,
		closeFn:   close,
	}
}

func (s *sqliteStorage) Session(tenantID int64) (SessionStore, error) {
	return s.sessionFn(tenantID)
}
func (s *sqliteStorage) Tenant() TenantStore                      { return s.tenant }
func (s *sqliteStorage) Memory() MemoryStore                      { return s.memory }
func (s *sqliteStorage) Config() ConfigStore                      { return s.config }
func (s *sqliteStorage) Cron() CronStore                          { return s.cron }
func (s *sqliteStorage) Trigger() event.TriggerStore              { return s.trigger }
func (s *sqliteStorage) OAuth() OAuthTokenStore                   { return s.oauth }
func (s *sqliteStorage) InteractiveAgents() InteractiveAgentStore { return s.agents }
func (s *sqliteStorage) Close() error                             { return s.closeFn() }

// CopySessionFrom copies a session's conversation into another session.
//
// Under v71 every tenant is a separate database file, so nothing can be expressed
// as one `INSERT … SELECT` across the two — the rows pass through the process
// either way. It still uses the shared helpers so the semantics are identical to
// PG: messages come from the *replayed* view (display-only rows such as cron
// notifications are not conversation and must not be inherited), iteration
// history is a pure copy.
func (s *sqliteStorage) CopySessionFrom(dstTenantID, srcTenantID int64) (int, error) {
	src, err := s.sessionFn(srcTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session: open source: %w", err)
	}
	dst, err := s.sessionFn(dstTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session: open destination: %w", err)
	}
	msgs, err := common.CopySessionMessages(src, dst, srcTenantID, dstTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session: %w", err)
	}
	if err := common.CopySessionIterations(src, dst, srcTenantID, dstTenantID); err != nil {
		log.WithError(err).Warn("copy session: iteration history copy failed (skipped)")
	}
	return len(msgs), nil
}

// Compile-time check.
var _ Storage = (*sqliteStorage)(nil)

// sqliteInteractiveAgentStore adapts the SQLite interactive_agents table to
// the backend-neutral InteractiveAgentStore. The concrete service already has
// the exact method set; this wrapper only bridges the type system.
type sqliteInteractiveAgentStore struct {
	*InteractiveAgentService
}

func newSQLiteInteractiveAgentStore(db *DB) InteractiveAgentStore {
	return &sqliteInteractiveAgentStore{NewInteractiveAgentService(db.Conn())}
}

var _ InteractiveAgentStore = (*sqliteInteractiveAgentStore)(nil)

// sqliteOAuthTokenStore adapts the SQLite oauth_tokens table to the
// backend-neutral OAuthTokenStore. It keeps the credential semantics of the
// original oauth.SQLiteStorage (encrypt when a key is configured, decrypt on read,
// unix-second expiry) so both backends behave identically.
type sqliteOAuthTokenStore struct {
	db *DB
}

func (s *sqliteOAuthTokenStore) GetOAuthToken(provider, channel, chatID string) (*OAuthToken, error) {
	var accessToken, refreshToken, scopesJSON, rawJSON string
	var expiresAt int64
	err := s.db.Conn().QueryRow(`
		SELECT access_token, COALESCE(refresh_token, ''), expires_at,
		       COALESCE(scopes, ''), COALESCE(raw, '')
		FROM oauth_tokens WHERE provider = ? AND channel = ? AND chat_id = ?`,
		provider, channel, chatID).
		Scan(&accessToken, &refreshToken, &expiresAt, &scopesJSON, &rawJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("query token: %w", err)
	}
	if accessToken != "" {
		if decrypted, derr := crypto.Decrypt(accessToken); derr == nil {
			accessToken = decrypted
		}
	}
	if refreshToken != "" {
		if decrypted, derr := crypto.Decrypt(refreshToken); derr == nil {
			refreshToken = decrypted
		}
	}
	var scopes []string
	if scopesJSON != "" {
		if jerr := json.Unmarshal([]byte(scopesJSON), &scopes); jerr != nil {
			log.WithError(jerr).Warn("Failed to parse scopes JSON")
		}
	}
	var raw map[string]any
	if rawJSON != "" {
		if jerr := json.Unmarshal([]byte(rawJSON), &raw); jerr != nil {
			log.WithError(jerr).Warn("Failed to parse raw JSON")
		}
	}
	return &OAuthToken{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    time.Unix(expiresAt, 0),
		Scopes:       scopes,
		Raw:          raw,
	}, nil
}

func (s *sqliteOAuthTokenStore) SetOAuthToken(provider, channel, chatID string, token *OAuthToken) error {
	scopesJSON, _ := json.Marshal(token.Scopes)
	rawJSON, _ := json.Marshal(token.Raw)
	accessToken := token.AccessToken
	refreshToken := token.RefreshToken
	if accessToken != "" {
		if encrypted, err := crypto.Encrypt(accessToken); err == nil {
			accessToken = encrypted
		}
	}
	if refreshToken != "" {
		if encrypted, err := crypto.Encrypt(refreshToken); err == nil {
			refreshToken = encrypted
		}
	}
	_, err := s.db.Conn().Exec(`
		REPLACE INTO oauth_tokens (provider, channel, chat_id, access_token, refresh_token,
			expires_at, scopes, raw, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		provider, channel, chatID, accessToken, refreshToken,
		token.ExpiresAt.Unix(), string(scopesJSON), string(rawJSON), time.Now().Unix())
	if err != nil {
		return fmt.Errorf("store token: %w", err)
	}
	return nil
}

func (s *sqliteOAuthTokenStore) DeleteOAuthToken(provider, channel, chatID string) error {
	_, err := s.db.Conn().Exec(
		`DELETE FROM oauth_tokens WHERE provider = ? AND channel = ? AND chat_id = ?`,
		provider, channel, chatID)
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	return nil
}

func (s *sqliteOAuthTokenStore) CleanupExpiredOAuthTokens(cutoff time.Time) error {
	res, err := s.db.Conn().Exec(`DELETE FROM oauth_tokens WHERE expires_at < ?`, cutoff.Unix())
	if err != nil {
		return fmt.Errorf("cleanup expired tokens: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		log.WithField("count", n).Info("Cleaned up expired OAuth tokens")
	}
	return nil
}
