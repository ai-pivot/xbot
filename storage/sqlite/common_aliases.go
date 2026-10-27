package sqlite

// Backend-neutral value types live in storage/common so the PG backend and the
// SQLite adapter can share one contract without importing each other. These
// aliases keep the existing `sqlite.X` call sites working (the concrete SQLite
// package is still the natural place to find DTOs for SQLite callers).
//
// Only the types named by storage/common.Storage / its sub-interfaces are
// aliased here. Backend-internal types (services, ActivityItem, group rows,
// structured messages) stay defined in this package.

import (
	"time"

	"xbot/storage/common"
)

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
type GroupStore = common.GroupStore
type ActivityStore = common.ActivityStore
type RunnerStore = common.RunnerStore

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
type RunnerInfo = common.RunnerInfo
type RunnerLLMSettings = common.RunnerLLMSettings

const (
	RunnerModeNative = common.RunnerModeNative
	RunnerModeDocker = common.RunnerModeDocker
)

func NormalizeRunnerMode(mode string) (string, error) { return common.NormalizeRunnerMode(mode) }

// Structured messages / groups / activity (backend-neutral types live in common;
// these aliases keep SQLite call sites and the SQLite service methods unchanged).
type StructuredMessage = common.StructuredMessage
type GroupMessageRow = common.GroupMessageRow
type GroupMessageEnvelope = common.GroupMessageEnvelope
type GroupRow = common.GroupRow
type GroupMemberRow = common.GroupMemberRow
type ActivityItem = common.ActivityItem

const (
	StructuredKindText     = common.StructuredKindText
	StructuredKindAskUser  = common.StructuredKindAskUser
	StructuredKindAnswer   = common.StructuredKindAnswer
	StructuredKindReport   = common.StructuredKindReport
	StructuredKindDecision = common.StructuredKindDecision
	StructuredKindCommand  = common.StructuredKindCommand
	StructuredKindEvent    = common.StructuredKindEvent

	ActivitySummaryMaxRunes = common.ActivitySummaryMaxRunes
	ActivityLimitDefault    = common.ActivityLimitDefault
	ActivityLimitMax        = common.ActivityLimitMax
	ActivityWindowDefault   = common.ActivityWindowDefault
)

var (
	StructuredMessageKinds = common.StructuredMessageKinds
	ActivityIndexedKinds   = common.ActivityIndexedKinds

	ErrStructuredNotFound         = common.ErrStructuredNotFound
	ErrAskUserAnswered            = common.ErrAskUserAnswered
	ErrAskUserCancelled           = common.ErrAskUserCancelled
	ErrDecisionTargetNotDecidable = common.ErrDecisionTargetNotDecidable
	ErrDecisionSelfApproval       = common.ErrDecisionSelfApproval
	ErrIdempotencyConflict        = common.ErrIdempotencyConflict

	ErrGroupNotFound  = common.ErrGroupNotFound
	ErrGroupExists    = common.ErrGroupExists
	ErrNotGroupMember = common.ErrNotGroupMember
)

func FormatActivityTime(t time.Time) string                 { return common.FormatActivityTime(t) }
func ParseActivityTime(s string) (time.Time, error)         { return common.ParseActivityTime(s) }
func ActivityKindIndexed(kind string) bool                  { return common.ActivityKindIndexed(kind) }
func TruncateActivitySummary(s string) string               { return common.TruncateActivitySummary(s) }
func NormalizeActivityLimit(limit int) int                  { return common.NormalizeActivityLimit(limit) }
func NormalizeActivityWindow(d time.Duration) time.Duration { return common.NormalizeActivityWindow(d) }

const (
	InteractiveAgentIdle        = common.InteractiveAgentIdle
	InteractiveAgentRunning     = common.InteractiveAgentRunning
	InteractiveAgentInterrupted = common.InteractiveAgentInterrupted
	InteractiveAgentUnloaded    = common.InteractiveAgentUnloaded
)

var ErrInteractiveAgentNotFound = common.ErrInteractiveAgentNotFound
