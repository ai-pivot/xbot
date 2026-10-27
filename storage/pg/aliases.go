package pg

// Contract/type aliases: the canonical definitions live in storage/common, so
// the PG implementation can keep using the same short names it always did
// without importing a sibling backend.

import common "xbot/storage/common"

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
type HistoryEntry = common.HistoryEntry

type HistoryRecordType = common.HistoryRecordType
type HistoryRecord = common.HistoryRecord
type ReplayResult = common.ReplayResult
type IterationRecord = common.IterationRecord
type UsageBucket = common.UsageBucket
type CronJob = common.CronJob
type LLMSubscription = common.LLMSubscription
type SubscriptionModel = common.SubscriptionModel
type UserDefaultModel = common.UserDefaultModel
type PerModelConfig = common.PerModelConfig
type TenantInfo = common.TenantInfo
type TenantDBInfo = common.TenantDBInfo
type UserChatWithPreview = common.UserChatWithPreview
type PendingResume = common.PendingResume
type SharedArtifact = common.SharedArtifact
type UserTokenUsage = common.UserTokenUsage
type DailyTokenUsage = common.DailyTokenUsage
type InteractiveAgentRecord = common.InteractiveAgentRecord
type AgentActivity = common.AgentActivity

const (
	HistoryRecordMessage     = common.HistoryRecordMessage
	HistoryRecordCommand     = common.HistoryRecordCommand
	HistoryRecordCompress    = common.HistoryRecordCompress
	HistoryRecordPrune       = common.HistoryRecordPrune
	HistoryRecordContextEdit = common.HistoryRecordContextEdit
	HistoryRecordAskQuestion = common.HistoryRecordAskQuestion
	HistoryRecordAskAnswer   = common.HistoryRecordAskAnswer
	HistoryRecordMask        = common.HistoryRecordMask

	InteractiveAgentIdle        = common.InteractiveAgentIdle
	InteractiveAgentRunning     = common.InteractiveAgentRunning
	InteractiveAgentInterrupted = common.InteractiveAgentInterrupted
	InteractiveAgentUnloaded    = common.InteractiveAgentUnloaded
)

var ErrInteractiveAgentNotFound = common.ErrInteractiveAgentNotFound
