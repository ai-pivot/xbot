package storage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"xbot/event"
	log "xbot/logger"
	"xbot/storage/sqlite"
)

// ─── Compile-time assertions: existing SQLite types satisfy the interfaces ───

// SessionService satisfies SessionStore (all methods already match).
// The only gap: SessionService methods use the concrete *sqlite.DB for
// historyLock internally — that's an implementation detail, not on the
// interface.

// TenantStore is satisfied by a composition of TenantService + ChatService +
// pending_resume methods on *sqlite.DB.

// MemoryStore is satisfied by a composition of CoreMemoryService + MemoryService.

// ConfigStore is satisfied by a composition of the remaining 7 services.

// ─── SQLite adapter: wraps existing services to satisfy the interfaces ───

// sqliteSessionStore wraps *sqlite.SessionService to satisfy SessionStore.
// Zero overhead: the concrete type already has all methods; this just
// bridges the type system.
type sqliteSessionStore struct {
	*sqlite.SessionService
}

// Compile-time check.
var _ SessionStore = (*sqliteSessionStore)(nil)

// ─── TenantStore ───

type sqliteTenantStore struct {
	tenants *sqlite.TenantService
	chats   *sqlite.ChatService
	db      *sqlite.DB // for pending_resume methods (AddPendingResume etc.)
}

func NewSQLiteTenantStore(db *sqlite.DB) TenantStore {
	return &sqliteTenantStore{
		tenants: sqlite.NewTenantService(db),
		chats:   sqlite.NewChatService(db),
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
func (s *sqliteTenantStore) GetTenantDBInfo(tenantID int64) (sqlite.TenantDBInfo, error) {
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
func (s *sqliteTenantStore) ListUserChats(channel, senderID, currentChatID string, offset, limit int) ([]sqlite.UserChatWithPreview, bool, error) {
	return s.chats.ListUserChats(channel, senderID, currentChatID, offset, limit)
}
func (s *sqliteTenantStore) UpdateChatSortOrders(channel, senderID string, orders map[string]int) error {
	return s.chats.UpdateChatSortOrders(channel, senderID, orders)
}
func (s *sqliteTenantStore) GetSenderForChat(channel, chatID string) (string, error) {
	return s.chats.GetSenderForChat(channel, chatID)
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
func (s *sqliteTenantStore) ListTenants() ([]sqlite.TenantInfo, error) {
	return s.tenants.ListTenants()
}
func (s *sqliteTenantStore) AddPendingResume(channel, chatID, senderID string) error {
	return s.db.AddPendingResume(channel, chatID, senderID)
}
func (s *sqliteTenantStore) GetSessionSenderID(channel, chatID string) (string, error) {
	return s.db.GetSessionSenderID(channel, chatID)
}
func (s *sqliteTenantStore) ListPendingResumes() ([]sqlite.PendingResume, error) {
	return s.db.ListPendingResumes()
}
func (s *sqliteTenantStore) ClearPendingResume(channel, chatID string) error {
	return s.db.ClearPendingResume(channel, chatID)
}

// Compile-time check.
var _ TenantStore = (*sqliteTenantStore)(nil)

// ─── MemoryStore ───

type sqliteMemoryStore struct {
	core *sqlite.CoreMemoryService
	mem  *sqlite.MemoryService
}

func NewSQLiteMemoryStore(db *sqlite.DB) MemoryStore {
	return &sqliteMemoryStore{
		core: sqlite.NewCoreMemoryService(db),
		mem:  sqlite.NewMemoryService(db),
	}
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
	subs     *sqlite.LLMSubscriptionService
	profile  *sqlite.UserProfileService
	settings *sqlite.UserSettingsService
	tokens   *sqlite.UserTokenUsageService
	cron     *sqlite.CronService
	trigger  *sqlite.TriggerService
	share    *sqlite.ShareService
	db       *sqlite.DB // for RecordUsage which needs *sql.DB
	webAuth  WebAuthStore
}

func NewSQLiteConfigStore(db *sqlite.DB) ConfigStore {
	return &sqliteConfigStore{
		subs:     sqlite.NewLLMSubscriptionService(db),
		profile:  sqlite.NewUserProfileService(db),
		settings: sqlite.NewUserSettingsService(db),
		tokens:   sqlite.NewUserTokenUsageService(db),
		cron:     sqlite.NewCronService(db),
		trigger:  sqlite.NewTriggerService(db),
		share:    sqlite.NewShareService(db),
		db:       db,
		webAuth:  NewSQLiteWebAuthStoreFromConn(db.Conn()),
	}
}

// LLM subscriptions
func (s *sqliteConfigStore) ListSubscriptions(senderID string) ([]*sqlite.LLMSubscription, error) {
	return s.subs.List(senderID)
}
func (s *sqliteConfigStore) GetSubscription(id string) (*sqlite.LLMSubscription, error) {
	return s.subs.Get(id)
}
func (s *sqliteConfigStore) GetDefaultSubscription(senderID string) (*sqlite.LLMSubscription, error) {
	return s.subs.GetDefault(senderID)
}
func (s *sqliteConfigStore) AddSubscription(sub *sqlite.LLMSubscription) error {
	return s.subs.Add(sub)
}
func (s *sqliteConfigStore) UpdateSubscription(sub *sqlite.LLMSubscription) error {
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
func (s *sqliteConfigStore) GetUsage(senderID string) (*sqlite.UserTokenUsage, error) {
	return s.tokens.GetUsage(senderID)
}
func (s *sqliteConfigStore) GetDailyUsage(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return s.tokens.GetDailyUsage(senderID, days)
}
func (s *sqliteConfigStore) GetDailyUsageSummary(senderID string, days int) ([]sqlite.DailyTokenUsage, error) {
	return s.tokens.GetDailyUsageSummary(senderID, days)
}
func (s *sqliteConfigStore) GetAllUsage() ([]sqlite.UserTokenUsage, error) {
	return s.tokens.GetAllUsage()
}

// Cron
func (s *sqliteConfigStore) AddCronJob(job *sqlite.CronJob) error {
	return s.cron.AddJob(job)
}
func (s *sqliteConfigStore) RemoveCronJob(id string) error {
	return s.cron.RemoveJob(id)
}
func (s *sqliteConfigStore) GetCronJob(id string) (*sqlite.CronJob, error) {
	return s.cron.GetJob(id)
}
func (s *sqliteConfigStore) ListCronJobsBySender(senderID string) ([]*sqlite.CronJob, error) {
	return s.cron.ListJobsBySender(senderID)
}
func (s *sqliteConfigStore) ListAllCronJobs() ([]*sqlite.CronJob, error) {
	return s.cron.ListAllJobs()
}
func (s *sqliteConfigStore) ListCronJobsByChannelChatID(channel, chatID string) ([]*sqlite.CronJob, error) {
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
func (s *sqliteConfigStore) CreateSharedArtifact(a *sqlite.SharedArtifact) error {
	return s.share.Create(a)
}
func (s *sqliteConfigStore) GetSharedArtifact(token string) (*sqlite.SharedArtifact, error) {
	a, err := s.share.Get(token)
	if a == nil {
		return nil, nil // not-found/revoked → (nil, nil) — matches PG and the contract
	}
	return a, err
}
func (s *sqliteConfigStore) RevokeSharedArtifact(token string, createdBy int64) error {
	return s.share.Revoke(token, createdBy)
}
func (s *sqliteConfigStore) ListSharedArtifactsByCreator(createdBy int64) ([]sqlite.SharedArtifact, error) {
	return s.share.ListByCreator(createdBy)
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
func (s *sqliteConfigStore) GetModel(subID, model string) (*sqlite.SubscriptionModel, error) {
	return s.subs.GetModel(subID, model)
}
func (s *sqliteConfigStore) GetModels(subID string) ([]*sqlite.SubscriptionModel, error) {
	return s.subs.GetModels(subID)
}
func (s *sqliteConfigStore) EnsureModel(subID, model string) error {
	return s.subs.EnsureModel(subID, model)
}
func (s *sqliteConfigStore) SetModelEnabled(subID, model string, enabled bool) error {
	return s.subs.SetModelEnabled(subID, model, enabled)
}
func (s *sqliteConfigStore) GetUserDefaultModel(senderID string) (*sqlite.UserDefaultModel, error) {
	return s.subs.GetUserDefaultModel(senderID)
}
func (s *sqliteConfigStore) SetUserDefaultModel(senderID, subID, model string) error {
	return s.subs.SetUserDefaultModel(senderID, subID, model)
}
func (s *sqliteConfigStore) GetDefault(senderID string) (*sqlite.LLMSubscription, error) {
	return s.subs.GetDefault(senderID)
}
func (s *sqliteConfigStore) UpsertModel(subID, model string, maxCtx, maxOut int, thinking, apiType string) error {
	return s.subs.UpsertModel(subID, model, maxCtx, maxOut, thinking, apiType)
}
func (s *sqliteConfigStore) List(senderID string) ([]*sqlite.LLMSubscription, error) {
	return s.subs.List(senderID)
}
func (s *sqliteConfigStore) ListAll() ([]*sqlite.LLMSubscription, error) {
	return s.subs.ListAll()
}
func (s *sqliteConfigStore) ListAllSubscriptions() ([]*sqlite.LLMSubscription, error) {
	return s.subs.ListAll()
}

func (s *sqliteConfigStore) Add(sub *sqlite.LLMSubscription) error    { return s.subs.Add(sub) }
func (s *sqliteConfigStore) Update(sub *sqlite.LLMSubscription) error { return s.subs.Update(sub) }
func (s *sqliteConfigStore) Remove(id string) error                   { return s.subs.Remove(id) }
func (s *sqliteConfigStore) Rename(id, name string) error             { return s.subs.Rename(id, name) }
func (s *sqliteConfigStore) SetDefault(id string) error               { return s.subs.SetDefault(id) }
func (s *sqliteConfigStore) SetModel(id, model string) error          { return s.subs.SetModel(id, model) }
func (s *sqliteConfigStore) SetModelMaxContext(subID, model string, maxCtx int) error {
	return s.subs.SetModelMaxContext(subID, model, maxCtx)
}
func (s *sqliteConfigStore) SetModelMaxOutput(subID, model string, maxOut int) error {
	return s.subs.SetModelMaxOutput(subID, model, maxOut)
}
func (s *sqliteConfigStore) RemoveModel(subID, model string) error {
	return s.subs.RemoveModel(subID, model)
}
func (s *sqliteConfigStore) Get(id string) (*sqlite.LLMSubscription, error) { return s.subs.Get(id) }
func (s *sqliteConfigStore) ClearUserDefaultModel(senderID string) error {
	return s.subs.ClearUserDefaultModel(senderID)
}

func (s *sqliteConfigStore) SetModelVisionConfig(subID, model string, vision bool, visionDetail string) error {
	return s.subs.SetModelVisionConfig(subID, model, vision, visionDetail)
}
func (s *sqliteConfigStore) UpdatePerModelConfigs(id string, configs map[string]sqlite.PerModelConfig) error {
	return s.subs.UpdatePerModelConfigs(id, configs)
}

var _ ConfigStore = (*sqliteConfigStore)(nil)

// ─── Storage factory: ties everything together ───

// sqliteStorage implements Storage for the SQLite backend.
// It wraps a main DB + per-session DB pool (v71 one-session-one-DB).
type sqliteStorage struct {
	mainDB    *sqlite.DB
	tenant    TenantStore
	memory    MemoryStore
	config    ConfigStore
	sessionFn func(int64) (SessionStore, error)
	closeFn   func() error
}

// NewSQLiteStorage creates a Storage backed by SQLite.
// The caller provides the main DB handle and a function to open/create
// per-session stores (wired to the v71 per-session DB pool).
func NewSQLiteStorage(
	mainDB *sqlite.DB,
	sessionFactory func(tenantID int64) (SessionStore, error),
	close func() error,
) Storage {
	return &sqliteStorage{
		mainDB:    mainDB,
		tenant:    NewSQLiteTenantStore(mainDB),
		memory:    NewSQLiteMemoryStore(mainDB),
		config:    NewSQLiteConfigStore(mainDB),
		sessionFn: sessionFactory,
		closeFn:   close,
	}
}

func (s *sqliteStorage) Session(tenantID int64) (SessionStore, error) {
	return s.sessionFn(tenantID)
}
func (s *sqliteStorage) Tenant() TenantStore { return s.tenant }
func (s *sqliteStorage) Memory() MemoryStore { return s.memory }
func (s *sqliteStorage) Config() ConfigStore { return s.config }
func (s *sqliteStorage) Close() error        { return s.closeFn() }

// Compile-time check.
var _ Storage = (*sqliteStorage)(nil)
