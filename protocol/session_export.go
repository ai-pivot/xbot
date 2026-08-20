package protocol

import (
	"encoding/json"
	"time"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// Session export/import format — xbot's own format, functionally aligned
// with the OpenAI Codex CLI session file (id / model / system_instructions /
// messages / usage / timestamps) so exported files are interoperable.
//
// The JSON message shape follows the OpenAI Chat Completions API:
//   - content can be a string OR an array of content parts (multimodal)
//   - assistant tool_calls use the nested {type, function:{name, arguments}} form
//   - tool messages carry tool_call_id + name
//
// xbot-specific extensions (ignored by Codex when reading the file):
//   - reasoning: the model's chain-of-thought (ReasoningContent)
//   - detail: tool result detail (diff etc.) — UI only, not sent to the LLM
//   - records: the FULL append-only history (all session_messages rows
//     including control records: compress / mask / context_edit / ask_*),
//     enabling lossless round-trip restore of the original session state.
// ---------------------------------------------------------------------------

// ExportedSession is the top-level exported session structure.
type ExportedSession struct {
	ID                 string            `json:"id"`
	Model              string            `json:"model,omitempty"`
	SystemInstructions string            `json:"system_instructions,omitempty"`
	Messages           []ExportedMessage `json:"messages"`
	Usage              *ExportedUsage    `json:"usage,omitempty"`
	CreatedAt          time.Time         `json:"created_at,omitempty"`
	UpdatedAt          time.Time         `json:"updated_at,omitempty"`
	// Records is the complete append-only history (xbot extension).
	// Empty when the export only contains the active (replayed) message view.
	Records []ExportedRecord `json:"records,omitempty"`
	// Iterations is the per-iteration record list (iteration_history table +
	// the in-flight iteration's partial stream content on graceful shutdown).
	// Each entry carries per-iteration TTFT/TPOT/tokens/timing. Empty for
	// exports that predate this field.
	Iterations []ExportedIteration `json:"iterations,omitempty"`
}

// ExportedIteration is one per-iteration record. It mirrors the
// iteration_history table (completed iterations) plus an optional in-flight
// entry (InFlight=true) carrying the partial stream content that had arrived
// when the export happened — used to preserve a mid-iteration result on
// graceful shutdown / benchmark timeout.
type ExportedIteration struct {
	TurnID       uint64 `json:"turn_id,omitempty"`
	Iteration    int    `json:"iteration"`
	Content      string `json:"content,omitempty"`
	Reasoning    string `json:"reasoning,omitempty"`
	Tools        string `json:"tools,omitempty"` // JSON array of tool snapshots
	Tokens       int64  `json:"tokens,omitempty"`
	TTFTMs       int64  `json:"ttft_ms,omitempty"`
	TPOTMs       int64  `json:"tpot_ms,omitempty"`
	TokensPerSec int64  `json:"tokens_per_sec,omitempty"`
	TotalMs      int64  `json:"total_ms,omitempty"`
	// InFlight indicates this iteration was still streaming when the export
	// happened (graceful shutdown mid-iteration) — Content/Reasoning carry the
	// partial stream content reached so far.
	InFlight bool `json:"in_flight,omitempty"`
}

// ExportedUsage holds token usage statistics.
type ExportedUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	TotalTokens  int `json:"total_tokens,omitempty"`
}

// ExportedMessage follows the OpenAI Chat Completions message format.
// Content is json.RawMessage to accept both the string and array forms.
type ExportedMessage struct {
	Role       string             `json:"role"`
	Content    json.RawMessage    `json:"content"`             // string OR []ExportedContentPart
	Reasoning  string             `json:"reasoning,omitempty"` // xbot extension: reasoning_content
	Detail     string             `json:"detail,omitempty"`    // xbot extension: tool result detail
	ToolCalls  []ExportedToolCall `json:"tool_calls,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	Name       string             `json:"name,omitempty"` // tool role: function name
}

// ExportedContentPart represents a multimodal content part.
type ExportedContentPart struct {
	Type     string            `json:"type"`
	Text     string            `json:"text,omitempty"`
	ImageURL *ExportedImageURL `json:"image_url,omitempty"`
}

// ExportedImageURL is the image URL for multimodal content.
type ExportedImageURL struct {
	URL string `json:"url"`
}

// ExportedToolCall follows the OpenAI function-calling format.
type ExportedToolCall struct {
	ID       string               `json:"id"`
	Type     string               `json:"type"` // always "function"
	Function ExportedToolFunction `json:"function"`
}

// ExportedToolFunction is the function payload inside a tool call.
type ExportedToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// ExportedRecord is one raw row of the append-only history log
// (session_messages table). Fields mirror the DB columns.
type ExportedRecord struct {
	HistoryID       int64           `json:"history_id"`
	RecordType      string          `json:"record_type"` // message|compress|mask|context_edit|ask_question|ask_answer|prune
	TargetHistoryID int64           `json:"target_history_id,omitempty"`
	RecordData      json.RawMessage `json:"record_data,omitempty"`
	Role            string          `json:"role,omitempty"`
	Content         string          `json:"content,omitempty"`
	ToolCallID      string          `json:"tool_call_id,omitempty"`
	ToolName        string          `json:"tool_name,omitempty"`
	ToolArguments   string          `json:"tool_arguments,omitempty"`
	ToolCalls       json.RawMessage `json:"tool_calls,omitempty"`
	Detail          string          `json:"detail,omitempty"`
	Reasoning       string          `json:"reasoning,omitempty"`
	DisplayOnly     bool            `json:"display_only,omitempty"`
	TurnID          uint64          `json:"turn_id,omitempty"`
	CreatedAt       time.Time       `json:"created_at,omitempty"`
}

// ---------------------------------------------------------------------------
// Conversion
// ---------------------------------------------------------------------------

// ContentToString extracts the text from a content field (string or array).
// Multimodal content (images) is silently dropped — xbot only supports text.
func (m ExportedMessage) ContentToString() string {
	if len(m.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(m.Content, &s); err == nil {
		return s
	}
	var parts []ExportedContentPart
	if err := json.Unmarshal(m.Content, &parts); err == nil {
		var text string
		for _, p := range parts {
			if p.Type == "text" {
				text += p.Text
			}
		}
		return text
	}
	return string(m.Content)
}

// ToChatMessage converts an ExportedMessage to an llm.ChatMessage.
func (m ExportedMessage) ToChatMessage() llm.ChatMessage {
	msg := llm.ChatMessage{
		Role:             m.Role,
		Content:          m.ContentToString(),
		ReasoningContent: m.Reasoning,
		ToolCallID:       m.ToolCallID,
		ToolName:         m.Name,
		Timestamp:        time.Now(),
	}
	for _, tc := range m.ToolCalls {
		msg.ToolCalls = append(msg.ToolCalls, llm.ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	if m.Detail != "" {
		msg.Detail = m.Detail
	}
	return msg
}

// FromChatMessage converts an llm.ChatMessage to an ExportedMessage.
func FromChatMessage(msg llm.ChatMessage) ExportedMessage {
	cm := ExportedMessage{
		Role:       msg.Role,
		Reasoning:  msg.ReasoningContent,
		Detail:     msg.Detail,
		ToolCallID: msg.ToolCallID,
		Name:       msg.ToolName,
	}
	// Content is always a string in xbot.
	cm.Content, _ = json.Marshal(msg.Content)
	for _, tc := range msg.ToolCalls {
		cm.ToolCalls = append(cm.ToolCalls, ExportedToolCall{
			ID:       tc.ID,
			Type:     "function",
			Function: ExportedToolFunction{Name: tc.Name, Arguments: tc.Arguments},
		})
	}
	return cm
}

// ExportSession converts xbot's internal ChatMessages to an ExportedSession.
// DisplayOnly messages are filtered out. The first system message is extracted
// into SystemInstructions rather than kept in the messages array.
func ExportSession(chatID, model string, msgs []llm.ChatMessage) (*ExportedSession, error) {
	session := &ExportedSession{ID: chatID, Model: model}
	var filtered []ExportedMessage
	for _, msg := range msgs {
		if msg.DisplayOnly {
			continue
		}
		if msg.Role == "system" && session.SystemInstructions == "" {
			session.SystemInstructions = msg.Content
			continue
		}
		filtered = append(filtered, FromChatMessage(msg))
		if !msg.Timestamp.IsZero() {
			if session.CreatedAt.IsZero() || msg.Timestamp.Before(session.CreatedAt) {
				session.CreatedAt = msg.Timestamp
			}
			if msg.Timestamp.After(session.UpdatedAt) {
				session.UpdatedAt = msg.Timestamp
			}
		}
	}
	session.Messages = filtered
	// The last assistant message's Detail holds the aggregated iteration
	// history JSON, which duplicates content already present in the message
	// stream — strip it from exports to keep the file clean. Tool-message
	// detail (diff etc.) is kept: it is UI-only and not part of the reply.
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == "assistant" {
			session.Messages[i].Detail = ""
			break
		}
	}
	return session, nil
}

// ImportSession converts an ExportedSession to xbot's internal ChatMessages,
// ready for AppendMessages. SystemInstructions (if present) is prepended as a
// system message.
func ImportSession(session *ExportedSession) []llm.ChatMessage {
	var msgs []llm.ChatMessage
	if session.SystemInstructions != "" {
		msgs = append(msgs, llm.NewSystemMessage(session.SystemInstructions))
	}
	for _, cm := range session.Messages {
		msgs = append(msgs, cm.ToChatMessage())
	}
	return msgs
}
