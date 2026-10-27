// Package storage is the stable public facade over the backend-split storage
// packages:
//
//	storage/common  — contract + backend-neutral value types
//	storage/sqlite  — SQLite implementation (+ its adapter)
//	storage/pg      — PostgreSQL implementation
//
// Callers keep importing xbot/storage; backend internals import common
// directly. New backend-neutral callers should prefer storage/common (the
// facade exists so the 50+ existing import sites do not have to change and so
// SQLite convenience constructors stay where callers expect them).
package storage

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/storage/common"
	"xbot/storage/pg"
	"xbot/storage/sqlite"
)

// ─── Contract aliases ───

type Storage = common.Storage
type SessionStore = common.SessionStore
type TenantBackend = common.TenantBackend
type TenantStore = common.TenantStore
type MemoryStore = common.MemoryStore
type AgentMemoryStore = common.AgentMemoryStore
type CronStore = common.CronStore
type WebUserInfo = common.WebUserInfo
type WebAuthStore = common.WebAuthStore
type SubscriptionBackend = common.SubscriptionBackend
type ConfigStore = common.ConfigStore
type PushTarget = common.PushTarget
type PushStore = common.PushStore
type OAuthToken = common.OAuthToken
type OAuthTokenStore = common.OAuthTokenStore
type InteractiveAgentStore = common.InteractiveAgentStore
type HistoryEntry = common.HistoryEntry

// ─── Shared value-type aliases ───

type HistoryRecordType = common.HistoryRecordType

const (
	HistoryRecordMessage     = common.HistoryRecordMessage
	HistoryRecordCommand     = common.HistoryRecordCommand
	HistoryRecordCompress    = common.HistoryRecordCompress
	HistoryRecordPrune       = common.HistoryRecordPrune
	HistoryRecordContextEdit = common.HistoryRecordContextEdit
	HistoryRecordAskQuestion = common.HistoryRecordAskQuestion
	HistoryRecordAskAnswer   = common.HistoryRecordAskAnswer
	HistoryRecordMask        = common.HistoryRecordMask
)

type HistoryRecord = common.HistoryRecord
type CompressionRange = common.CompressionRange
type ContextSnapshot = common.ContextSnapshot
type MessageMutation = common.MessageMutation
type MessageMutations = common.MessageMutations
type MaskMutation = common.MaskMutation
type MaskMutations = common.MaskMutations
type AskQuestionRecord = common.AskQuestionRecord
type AskAnswerRecord = common.AskAnswerRecord
type PendingAskUser = common.PendingAskUser
type ReplayResult = common.ReplayResult

type IterationRecord = common.IterationRecord
type UsageModelRow = common.UsageModelRow
type UsageIterationRow = common.UsageIterationRow
type TenantUsageStats = common.TenantUsageStats
type UsageBucket = common.UsageBucket

type CronJob = common.CronJob

type PerModelConfig = common.PerModelConfig
type LLMSubscription = common.LLMSubscription
type SubscriptionModel = common.SubscriptionModel
type UserDefaultModel = common.UserDefaultModel

type UserChatWithPreview = common.UserChatWithPreview
type TenantInfo = common.TenantInfo
type TenantDBInfo = common.TenantDBInfo
type PendingResume = common.PendingResume
type SharedArtifact = common.SharedArtifact

type UserTokenUsage = common.UserTokenUsage
type DailyTokenUsage = common.DailyTokenUsage

type InteractiveAgentRecord = common.InteractiveAgentRecord
type AgentActivity = common.AgentActivity

const (
	InteractiveAgentIdle        = common.InteractiveAgentIdle
	InteractiveAgentRunning     = common.InteractiveAgentRunning
	InteractiveAgentInterrupted = common.InteractiveAgentInterrupted
	InteractiveAgentUnloaded    = common.InteractiveAgentUnloaded
)

var ErrInteractiveAgentNotFound = common.ErrInteractiveAgentNotFound

// ─── SQLite convenience constructors ───

// NewSQLiteStorage creates a Storage backed by SQLite. See storage/sqlite.
func NewSQLiteStorage(
	mainDB *sqlite.DB,
	sessionFactory func(tenantID int64) (SessionStore, error),
	close func() error,
) Storage {
	return sqlite.NewSQLiteStorage(mainDB, sessionFactory, close)
}

func NewSQLiteConfigStore(db *sqlite.DB) ConfigStore { return sqlite.NewSQLiteConfigStore(db) }
func NewSQLiteTenantStore(db *sqlite.DB) TenantStore { return sqlite.NewSQLiteTenantStore(db) }
func NewSQLiteMemoryStore(db *sqlite.DB) MemoryStore { return sqlite.NewSQLiteMemoryStore(db) }
func NewSQLiteMemoryStoreFromConn(conn *sql.DB) MemoryStore {
	return sqlite.NewSQLiteMemoryStoreFromConn(conn)
}

const (
	CLITenantChannel = sqlite.CLITenantChannel
	CLITenantChatID  = sqlite.CLITenantChatID
)

func MigrateFromFileStorage(workDir, dbPath string) error {
	return sqlite.MigrateFromFileStorage(workDir, dbPath)
}
func MigrateIfNeeded(ctx context.Context, workDir, dbPath string) error {
	return sqlite.MigrateIfNeeded(ctx, workDir, dbPath)
}
func MigrateMemoryToFiles(dbPath string) { sqlite.MigrateMemoryToFiles(dbPath) }

// ─── PostgreSQL convenience constructors ───

// NewStorage creates a Storage backed by PostgreSQL.
func NewPGStorage(ctx context.Context, dsn string) (Storage, error) {
	return pg.NewStorage(ctx, dsn)
}

// NewStorageWithPool wires a PG Storage onto an existing pool (tests /
// embedding callers that own pool lifecycle).
func NewPGStorageWithPool(pool *pgxpool.Pool) (Storage, error) {
	return pg.NewStorageWithPool(pool)
}

// InitSchema applies the PG schema (idempotent). See storage/pg.
func InitPGSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return pg.InitSchema(ctx, pool)
}
