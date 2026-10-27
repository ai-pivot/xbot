package sqlite

// Backend-neutral value types live in storage/common so the PG backend and the
// SQLite adapter can share one contract without importing each other. These
// aliases keep the existing `sqlite.X` call sites working (the concrete SQLite
// package is still the natural place to find DTOs for SQLite callers).
//
// Only the types named by storage/common.Storage / its sub-interfaces are
// aliased here. Backend-internal types (services, ActivityItem, group rows,
// structured messages) stay defined in this package.

import "xbot/storage/common"

// Contract/type aliases. Keeping the unqualified names means the adapter and
// backend files read the same as before while the canonical definitions live in
// storage/common (no import cycle between common and the backends).
type Storage = common.Storage
type SessionStore = common.SessionStore
type TenantBackend = common.TenantBackend
type TenantStore = common.TenantStore
type MemoryStore = common.MemoryStore
type AgentMemoryStore = common.AgentMemoryStore
type CronStore = common.CronStore
type WebAuthStore = common.WebAuthStore
type WebUserInfo = common.WebUserInfo
type SubscriptionBackend = common.SubscriptionBackend
type ConfigStore = common.ConfigStore
type PushTarget = common.PushTarget
type PushStore = common.PushStore
type OAuthToken = common.OAuthToken
type OAuthTokenStore = common.OAuthTokenStore
type InteractiveAgentStore = common.InteractiveAgentStore

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
type HistoryEntry = common.HistoryEntry

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
