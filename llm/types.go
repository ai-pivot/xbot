package llm

import (
	"regexp"
	"strings"
	"time"
)

// Pre-compiled regex patterns for stripping think blocks
var (
	thinkBlockRegex     = regexp.MustCompile(`(?s)<think>.*?</think>`)
	reasoningBlockRegex = regexp.MustCompile(`(?s)<reasoning>.*?</reasoning>`)
	thinkingBlockRegex  = regexp.MustCompile(`(?s)<thinking>.*?</thinking>`)
)

// Thinking mode constants for reasoning models.
const (
	ThinkingEnabled  = "enabled"
	ThinkingDisabled = "disabled"
)

// IsThinkingActive returns true if the thinking mode string indicates that
// extended thinking/reasoning should be active (non-empty and not "disabled").
func IsThinkingActive(mode string) bool {
	return mode != "" && mode != ThinkingDisabled
}

// ChatMessage is the business-layer message type, decoupled from specific LLM implementations
type ChatMessage struct {
	Role             string     `json:"role"` // "system", "user", "assistant", "tool"
	Content          string     `json:"content"`
	ReasoningContent string     `json:"reasoning_content,omitempty"` // Chain-of-thought content for DeepSeek/OpenAI reasoning models
	ToolCallID       string     `json:"tool_call_id,omitempty"`      // For tool messages: the tool call ID
	ToolName         string     `json:"tool_name,omitempty"`         // For tool messages: the tool name
	ToolArguments    string     `json:"tool_arguments,omitempty"`    // For tool messages: the tool call arguments
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`        // For assistant messages with tool calls
	Detail           string     `json:"-"`                           // Tool result details (e.g. diff); not sent to LLM, only used for persistence and frontend display
	Timestamp        time.Time  `json:"-"`                           // Message timestamp; not sent to LLM
	DisplayOnly      bool       `json:"-"`                           // Display-only message (e.g. cron results); not sent to LLM

	// CacheHint indicates the caching behavior of this message to the LLM layer.
	// "static" — content unchanged across requests (e.g. base system prompt template)
	// "" (default) — dynamic content, no cache annotation
	CacheHint string `json:"cache_hint,omitempty"`
}

// NewSystemMessage creates a system message
func NewSystemMessage(content string) ChatMessage {
	return ChatMessage{Role: "system", Content: content, Timestamp: time.Now()}
}

// FixupTrailingToolCalls strips trailing unpaired tool_call messages from the
// message list. An assistant message with non-empty ToolCalls is considered
// unpaired if it is not followed by tool-result messages for every call.
// This can happen when Ctrl+C cancels a Run between recordAssistantMsg and
// executeToolCalls/processToolResults. Both Anthropic and OpenAI APIs reject
// requests with unpaired tool_calls, so this must be called before sending.
func FixupTrailingToolCalls(messages []ChatMessage) []ChatMessage {
	for len(messages) > 0 {
		last := messages[len(messages)-1]

		// Trailing assistant with tool_calls → unpaired, strip it
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			messages = messages[:len(messages)-1]
			continue
		}

		// Trailing tool message without a preceding assistant that has matching
		// tool_calls (orphaned tool result) → also strip
		if last.Role == "tool" {
			// Check if the message before this is an assistant with tool_calls
			if len(messages) >= 2 {
				prev := messages[len(messages)-2]
				if prev.Role == "assistant" && len(prev.ToolCalls) > 0 {
					break // paired, we're done
				}
			}
			// Orphaned tool result, strip it
			messages = messages[:len(messages)-1]
			continue
		}

		break
	}
	return messages
}

// NewUserMessage creates a user message
func NewUserMessage(content string) ChatMessage {
	return ChatMessage{Role: "user", Content: content, Timestamp: time.Now()}
}

// NewAssistantMessage creates an assistant message
func NewAssistantMessage(content string) ChatMessage {
	return ChatMessage{Role: "assistant", Content: content, Timestamp: time.Now()}
}

// NewToolMessage creates a tool message
func NewToolMessage(toolName, toolCallID, arguments, content string) ChatMessage {
	return ChatMessage{
		Role:          "tool",
		Content:       content,
		ToolName:      toolName,
		ToolCallID:    toolCallID,
		ToolArguments: arguments,
		Timestamp:     time.Now(),
	}
}

// ToolCall is the business-layer tool call type
type ToolCall struct {
	ID        string `json:"id"`        // Tool call ID, used to correlate with subsequent results
	Name      string `json:"name"`      // Tool name
	Arguments string `json:"arguments"` // Tool arguments (JSON string)
}

// FinishReason is the LLM finish reason
type FinishReason string

const (
	FinishReasonStop                  FinishReason = "stop"                          // Normal completion
	FinishReasonLength                FinishReason = "length"                        // Reached max length
	FinishReasonToolCalls             FinishReason = "tool_calls"                    // Tool call requested
	FinishReasonContentFilter         FinishReason = "content_filter"                // Content filtered
	FinishReasonContextWindowExceeded FinishReason = "model_context_window_exceeded" // Context window exceeded
)

// TokenUsage holds token usage statistics
type TokenUsage struct {
	PromptTokens        int64 `json:"prompt_tokens"`         // Input token count
	CompletionTokens    int64 `json:"completion_tokens"`     // Output token count
	TotalTokens         int64 `json:"total_tokens"`          // Total token count
	CacheHitTokens      int64 `json:"cache_hit_tokens"`      // Cache-hit input tokens (OpenAI: prompt_tokens_details.cached_tokens, Anthropic: cache_read_input_tokens)
	CacheCreationTokens int64 `json:"cache_creation_tokens"` // Cache-creation input tokens (Anthropic: cache_creation_input_tokens)
}

func (u TokenUsage) Add(u1 TokenUsage) TokenUsage {
	u.CompletionTokens += u1.CompletionTokens
	u.PromptTokens += u1.PromptTokens
	u.TotalTokens += u1.TotalTokens
	u.CacheHitTokens += u1.CacheHitTokens
	u.CacheCreationTokens += u1.CacheCreationTokens
	return u
}

// LLMResponse is the business-layer LLM response type
type LLMResponse struct {
	Content          string       `json:"content"`                     // Text content
	ReasoningContent string       `json:"reasoning_content,omitempty"` // Chain-of-thought content (DeepSeek/OpenAI reasoning models)
	ToolCalls        []ToolCall   `json:"tool_calls,omitempty"`        // Tool call requested列表（可能为空）
	FinishReason     FinishReason `json:"finish_reason"`               // Finish reason
	Usage            TokenUsage   `json:"usage"`                       // Token usage statistics
}

// HasToolCalls checks whether the response contains tool calls.
// Decision is based on actual tool_calls data (not finish_reason).
// Some providers (DeepSeek, Zhipu) return finish_reason "stop" instead of "tool_calls" when tool_calls are present;
// therefore finish_reason alone is unreliable.
func (r *LLMResponse) HasToolCalls() bool {
	return len(r.ToolCalls) > 0
}

// StreamEventType is a streaming event type
type StreamEventType string

const (
	EventContent          StreamEventType = "content"           // Text content增量
	EventReasoningContent StreamEventType = "reasoning_content" // Chain-of-thought content delta (DeepSeek/OpenAI reasoning models)
	EventToolCall         StreamEventType = "tool_call"         // Tool call requested增量
	EventUsage            StreamEventType = "usage"             // Token statistics
	EventDone             StreamEventType = "done"              // Stream complete
	EventError            StreamEventType = "error"             // Error
)

// ToolCallDelta is an incremental tool call update
type ToolCallDelta struct {
	Index     int    `json:"index"`               // Tool call requested索引
	ID        string `json:"id,omitempty"`        // Tool call requested ID（首次出现）
	Name      string `json:"name,omitempty"`      // Tool name（首次出现）
	Arguments string `json:"arguments,omitempty"` // Argument delta
}

// StreamEvent is a streaming event
type StreamEvent struct {
	Type             StreamEventType `json:"type"`
	Content          string          `json:"content,omitempty"`           // Text delta
	ReasoningContent string          `json:"reasoning_content,omitempty"` // Chain-of-thought delta (DeepSeek/OpenAI reasoning models)
	ToolCall         *ToolCallDelta  `json:"tool_call,omitempty"`         // Tool call requested增量
	Usage            *TokenUsage     `json:"usage,omitempty"`             // Token statistics
	FinishReason     FinishReason    `json:"finish_reason,omitempty"`     // Finish reason
	Error            string          `json:"error,omitempty"`             // Error信息
}

// ToolParam defines a tool parameter
type ToolParam struct {
	Name        string          `json:"name"`
	Type        string          `json:"type"`
	Description string          `json:"description"`
	Required    bool            `json:"required"`
	Items       *ToolParamItems `json:"items,omitempty"` // For array types
}

// ToolParamItems defines the element type for array parameters (supports full JSON Schema sub-structures)
type ToolParamItems struct {
	Type                 string          `json:"type"`
	Properties           map[string]any  `json:"properties,omitempty"`
	Required             []string        `json:"required,omitempty"`
	Items                *ToolParamItems `json:"items,omitempty"`
	Description          string          `json:"description,omitempty"`
	AdditionalProperties any             `json:"additionalProperties,omitempty"`
}

// ToolDefinition is the tool definition interface (used for LLM calls)
type ToolDefinition interface {
	Name() string
	Description() string
	Parameters() []ToolParam
}

// StripThinkBlocks removes thinking/reasoning blocks from content.
// Models like DeepSeek return thinking content in formats like:
// - <think>...</think>
// - <reasoning>...</reasoning>
// This content should not be included in context or shown to users.
func StripThinkBlocks(content string) string {
	if content == "" {
		return ""
	}
	// Remove <think>...</think> blocks
	content = thinkBlockRegex.ReplaceAllString(content, "")
	// Remove <reasoning>...</reasoning> blocks
	content = reasoningBlockRegex.ReplaceAllString(content, "")
	// Remove <thinking>...</thinking> blocks
	content = thinkingBlockRegex.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}
