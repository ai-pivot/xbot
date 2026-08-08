package memory

import (
	"context"
	"database/sql"
	"sync"

	"xbot/llm"
)

// MemoryProvider 可插拔记忆系统的核心接口。
// 所有记忆实现（flat/letta/xbot）必须满足此接口。
type MemoryProvider interface {
	// Name 返回 provider 的唯一标识符（如 "flat", "letta", "xbot"）。
	// 用于日志、SubAgent 记忆构建等场景的 provider 类型识别。
	Name() string

	// Recall 为当前对话检索相关记忆，返回注入 system prompt 的文本。
	// query 为用户当前消息，用于按需检索（flat 实现忽略此参数）。
	Recall(ctx context.Context, query string) (string, error)

	// Memorize 对话结束后处理记忆（压缩、存储、进化等）。
	Memorize(ctx context.Context, input MemorizeInput) (MemorizeResult, error)

	// Close 释放资源。
	Close() error
}

// ToolIndexer 提供工具语义搜索能力
type ToolIndexer interface {
	// IndexTools 将工具索引到向量存储（启动时调用）
	IndexTools(ctx context.Context, tools []ToolIndexEntry) error

	// SearchTools 语义搜索工具
	SearchTools(ctx context.Context, query string, topK int) ([]ToolIndexEntry, error)
}

// ToolIndexEntry 工具索引条目
type ToolIndexEntry struct {
	Name        string   // 工具名称 (如 mcp_server_tool)
	ServerName  string   // MCP服务器名 (如 feishu, global)
	Source      string   // 来源: "global" 或 "personal"
	Description string   // 工具描述
	Channels    []string // 支持的渠道列表（空=所有渠道）
}

// MemorizeInput 记忆写入的输入参数。
type MemorizeInput struct {
	Messages         []llm.ChatMessage // 需要处理的对话消息
	LastConsolidated int               // 上次合并的偏移量
	LLMClient        llm.LLM           // 用于压缩/分析的 LLM
	Model            string            // 模型名称
	ArchiveAll       bool              // true=归档所有消息（/new 命令）
}

// MemorizeResult 记忆写入的结果。
type MemorizeResult struct {
	NewLastConsolidated int  // 新的合并偏移量
	OK                  bool // 是否成功
}

// --- 可选能力接口（Phase 2+ 使用，此处预定义） ---

// Manageable 支持手动记忆管理（pin/unpin/delete）。
type Manageable interface {
	Pin(ctx context.Context, noteID string) error
	Unpin(ctx context.Context, noteID string) error
	Delete(ctx context.Context, noteID string) error
}

// Evolvable 支持记忆进化（A-Mem 风格）。
type Evolvable interface {
	Evolve(ctx context.Context, content string) ([]Evolution, error)
}

// Evolution 记忆进化操作记录。
type Evolution struct {
	Action string // "created" | "merged" | "updated" | "strengthened" | "discarded"
	NoteID string
	Detail string
}

// CompressionAware 允许记忆系统干涉上下文压缩流程。
// 实现此接口的 MemoryProvider 可以在压缩前保存即将丢失的消息，
// 在压缩后执行记忆后处理，以及向压缩 LLM 提供额外上下文。
//
// 这是可选接口——flat/letta provider 不实现它，压缩行为完全不变。
// 只有 xbot provider 实现此接口以解决压缩失忆问题。
type CompressionAware interface {
	// PreCompress 在压缩执行前调用。
	// 接收即将被压缩的消息列表，将其中的关键信息保存到长期记忆。
	// 返回的 PreCompressResult 可以影响压缩行为。
	PreCompress(ctx context.Context, input PreCompressInput) (*PreCompressResult, error)

	// PostCompress 在压缩完成后调用。
	// 接收压缩后的消息列表和压缩摘要，执行记忆后处理。
	PostCompress(ctx context.Context, input PostCompressInput) error

	// CompressContext 注入到压缩 LLM 的 system prompt 中。
	// 允许记忆系统向压缩 LLM 提供额外上下文（如"这些信息很重要，务必保留"）。
	CompressContext(ctx context.Context) (string, error)
}

// PreCompressInput 压缩前输入。
type PreCompressInput struct {
	// MessagesToCompress 即将被压缩的消息（不含 system 和 tail）。
	MessagesToCompress []llm.ChatMessage
	// TailMessages 压缩后保留的尾部消息。
	TailMessages []llm.ChatMessage
	// SessionID 当前会话 ID。
	SessionID string
	// LLMClient 用于记忆提取的 LLM。
	LLMClient llm.LLM
	// Model LLM 模型名。
	Model string
}

// PreCompressResult 压缩前处理结果。
type PreCompressResult struct {
	// SavedCount 保存到长期记忆的条目数。
	SavedCount int
	// PreserveHints 需要压缩 LLM 务必保留的关键信息提示。
	// 这些提示会被注入到压缩 prompt 中。
	PreserveHints []string
	// SkipCompress 如果为 true，表示记忆系统已处理所有信息，
	// 可以跳过压缩（极端情况：记忆系统已保存全部信息，直接清空上下文）。
	SkipCompress bool
}

// PostCompressInput 压缩后输入。
type PostCompressInput struct {
	// CompressedMessages 压缩后的完整消息列表（含摘要 + tail）。
	CompressedMessages []llm.ChatMessage
	// CompactionSummary LLM 生成的压缩摘要文本。
	CompactionSummary string
	// RemovedMessageCount 被压缩移除的消息数。
	RemovedMessageCount int
	// SessionID 当前会话 ID。
	SessionID string
}

// --- Provider Registry (decoupled from agent/session code) ---

// PromptParts holds the system prompt fragments for a memory provider.
// Registered by each provider package in init(), retrieved by name.
type PromptParts struct {
	ToolsPrompt  string // system prompt "## Tools" section
	MemoryPrompt string // system prompt "## Memory" section (empty = no injection)
	UserGuide    string // user message guide text
}

// ProviderDeps holds dependencies for creating a MemoryProvider instance.
// Each provider type-asserts the fields it needs; unused fields are nil.
type ProviderDeps struct {
	TenantID int64
	BaseDir  string
	DB      *sql.DB
	// LettaDeps is provider-specific deps for letta (nil for non-letta).
	// Type: *letta.Deps — stored as any to avoid import cycle.
	LettaDeps any
}

// ProviderFactory creates a MemoryProvider from ProviderDeps.
type ProviderFactory func(deps ProviderDeps) MemoryProvider

var (
	providerFactories = map[string]ProviderFactory{}
	promptPartsMap    = map[string]PromptParts{}
	registryMu        sync.RWMutex
)

// RegisterProviderFactory registers a provider factory by name.
// Called by each provider package in init().
func RegisterProviderFactory(name string, factory ProviderFactory) {
	registryMu.Lock()
	defer registryMu.Unlock()
	providerFactories[name] = factory
}

// CreateProvider creates a MemoryProvider by name.
// Returns nil if no factory is registered for the name (including "none").
func CreateProvider(name string, deps ProviderDeps) MemoryProvider {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if f, ok := providerFactories[name]; ok {
		return f(deps)
	}
	return nil
}

// RegisterPromptParts registers prompt parts for a provider name.
// Called by each provider package in init().
func RegisterPromptParts(name string, parts PromptParts) {
	registryMu.Lock()
	defer registryMu.Unlock()
	promptPartsMap[name] = parts
}

// GetPromptParts returns the prompt parts for a provider name.
// Returns empty PromptParts if not registered.
func GetPromptParts(name string) PromptParts {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return promptPartsMap[name]
}

// IsProviderRegistered returns true if a provider factory is registered for the name.
func IsProviderRegistered(name string) bool {
	registryMu.RLock()
	defer registryMu.RUnlock()
	_, ok := providerFactories[name]
	return ok
}
