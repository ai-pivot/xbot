package channel

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"xbot/llm"
	"xbot/protocol"
)

// Subscription represents a LLM subscription for display/selection.
type Subscription = protocol.Subscription

// PerModelConfig stores per-model token overrides within a subscription.
type PerModelConfig = protocol.PerModelConfig

// HistoryIteration 历史迭代快照（用于会话恢复的 tool_summary 渲染）
type HistoryIteration = protocol.HistoryIteration

// HistoryMessage 历史消息（用于会话恢复）
type HistoryMessage = protocol.HistoryMessage

// DailyTokenUsage represents token usage for a specific day+model.
// Mirror of sqlite.DailyTokenUsage — used in CLIChannelConfig.UsageQuery callback
// so that cmd/xbot-cli does not need to import the sqlite package.
type DailyTokenUsage struct {
	Date              string `json:"date"` // YYYY-MM-DD
	SenderID          string `json:"sender_id"`
	Model             string `json:"model"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CachedTokens      int64  `json:"cached_tokens"`
	ConversationCount int64  `json:"conversation_count"`
	LLMCallCount      int64  `json:"llm_call_count"`
}

// iterSnapshot mirrors agent.IterationSnapshot for JSON unmarshaling Detail field.
type iterSnapshot struct {
	Iteration int            `json:"iteration"`
	Content   string         `json:"content,omitempty"`
	Reasoning string         `json:"reasoning,omitempty"`
	Tools     []iterToolSnap `json:"tools"`
}

type iterToolSnap struct {
	Name      string `json:"name"`
	Label     string `json:"label,omitempty"`
	Status    string `json:"status"`
	ElapsedMS int64  `json:"elapsed_ms"`
	Summary   string `json:"summary,omitempty"`
}

// truncateLabel safely truncates a string to maxRunes.
// Appends "..." if truncated and maxRunes > 3.
// If maxRunes <= 0 or the string already fits, returns original unchanged.
func truncateLabel(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 3 {
		return string(runes[:maxRunes])
	}
	return string(runes[:maxRunes-3]) + "..."
}

// formatToolLabel generates a short human-readable label from a tool name and its JSON arguments.
// Used when restoring progress from intermediate assistant messages (no Detail snapshot),
// e.g. after server restart. Produces labels like "Shell(tail -100 file.log)" or "Read(path)".
func formatToolLabel(name, argsJSON string) string {
	const maxLen = 60
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return name
	}

	get := func(key string) string {
		if v, ok := args[key]; ok {
			if s, ok := v.(string); ok {
				return s
			}
			return fmt.Sprintf("%v", v)
		}
		return ""
	}

	// budget returns the max runes available for the argument value,
	// accounting for "name(" + ")" wrapper. Returns 0 if name itself exceeds maxLen.
	// Tool names are always ASCII, so len(name) == rune count.
	budget := func() int {
		n := maxLen - len(name) - 2 // len("name(") + len(")") = len(name) + 2
		if n < 0 {
			n = 0
		}
		return n
	}

	switch name {
	case "Shell":
		cmd := get("command")
		if cmd != "" {
			return name + "(" + truncateLabel(cmd, budget()) + ")"
		}
	case "Read":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Grep":
		p := get("pattern")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Glob":
		p := get("pattern")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Write", "FileCreate":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "Edit", "FileReplace":
		p := get("path")
		if p != "" {
			return name + "(" + p + ")"
		}
	case "WebSearch":
		q := get("query")
		if q != "" {
			return name + "(" + q + ")"
		}
	case "SubAgent":
		r := get("role")
		t := get("task")
		if r != "" {
			if t != "" {
				t = truncateLabel(t, 30)
			}
			if t != "" {
				return name + "(" + r + ": " + t + ")"
			}
			return name + "(" + r + ")"
		}
	default:
		// Generic: show first string parameter
		for _, v := range args {
			if s, ok := v.(string); ok && s != "" {
				return name + "(" + truncateLabel(s, budget()) + ")"
			}
		}
	}
	return name
}

// ConvertMessagesToHistory converts raw DB messages into HistoryMessages for CLI display.
// It handles three scenarios:
//  1. Normal completed turn: assistant with Detail → one tool_summary + assistant
//  2. Cancelled/interrupted turn: intermediate assistant(ToolCalls) without Detail → pending tool_summary
//  3. Mixed: some turns completed, last one cancelled
func ConvertMessagesToHistory(msgs []llm.ChatMessage) []HistoryMessage {
	var history []HistoryMessage
	var pendingIters []HistoryIteration
	var curIterTools []protocol.ToolProgress
	var curIterIdx int
	var curIterThinking string
	var curIterReasoning string

	finishCurIter := func() {
		if len(curIterTools) > 0 || curIterThinking != "" || curIterReasoning != "" {
			pendingIters = append(pendingIters, HistoryIteration{
				Iteration: curIterIdx,
				Content:   curIterThinking,
				Reasoning: curIterReasoning,
				Tools:     curIterTools,
			})
		}
		curIterTools = nil
		curIterThinking = ""
		curIterReasoning = ""
	}

	// lastAssistantTS tracks the timestamp of the last processed assistant
	// message, used to assign a unique Timestamp to flushPending()-generated
	// tool_summary messages. Without this, multiple interrupted turns produce
	// tool_summary messages with zero timestamps, causing dedup to drop all
	// but the first.
	var lastAssistantTS time.Time
	// syntheticIdx provides monotonically-increasing nanosecond offsets to
	// guarantee unique timestamps for consecutive flushPending() calls when
	// no real assistant timestamp is available (e.g. all turns interrupted).
	var syntheticIdx int

	flushPending := func() {
		finishCurIter()
		if len(pendingIters) > 0 {
			ts := lastAssistantTS
			if ts.IsZero() {
				// No assistant message in this turn — assign a synthetic
				// timestamp so each assistant message gets a unique dedup key.
				ts = time.Date(2024, 1, 1, 0, 0, 0, syntheticIdx, time.UTC)
				syntheticIdx++
			}
			history = append(history, HistoryMessage{
				Role:       "assistant",
				Content:    "",
				Timestamp:  ts,
				Iterations: pendingIters,
			})
			pendingIters = nil
		}
	}

	// Pre-scan tool messages to build a toolCallID → content map.
	// Used as fallback for determining tool status (done/error) when
	// assistant messages lack Detail (e.g. server crash mid-turn, old data).
	toolResults := make(map[string]string)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	for _, m := range msgs {
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			lastAssistantTS = m.Timestamp
			if m.Detail != "" {
				// Detail has authoritative iteration history. Discard pending iters
				// from intermediate assistant messages — they lack elapsed/label data.
				finishCurIter()
				pendingIters = nil

				var snaps []iterSnapshot
				if jsonErr := json.Unmarshal([]byte(m.Detail), &snaps); jsonErr == nil {
					iters := make([]HistoryIteration, 0, len(snaps))
					for _, snap := range snaps {
						toolList := make([]protocol.ToolProgress, len(snap.Tools))
						for i, t := range snap.Tools {
							label := t.Label
							if label == "" {
								label = t.Name
							}
							toolList[i] = protocol.ToolProgress{
								Name:      t.Name,
								Label:     label,
								Status:    t.Status,
								Elapsed:   t.ElapsedMS,
								Iteration: snap.Iteration,
								Summary:   t.Summary,
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: snap.Iteration,
							Content:   snap.Content,
							Reasoning: snap.Reasoning,
							Tools:     toolList,
						})
					}
					if len(iters) > 0 {
						// [interrupted] messages carry cancelled-turn iteration history
						// with full elapsed data. Use empty Content so the UI shows
						// only the progress block, not the "[interrupted]" marker text.
						isInterrupted := strings.HasPrefix(m.Content, "[interrupted]")
						if m.Content != "" && !isInterrupted {
							history = append(history, HistoryMessage{
								Role:       "assistant",
								Content:    m.Content,
								Timestamp:  m.Timestamp,
								Iterations: iters,
							})
						} else {
							// Detail has iterations but no displayable content
							// (intermediate assistant, cancelled turn, or [interrupted] marker).
							history = append(history, HistoryMessage{
								Role:       "assistant",
								Content:    "",
								Timestamp:  m.Timestamp,
								Iterations: iters,
							})
						}
					} else if m.Content != "" && !strings.HasPrefix(m.Content, "[interrupted]") {
						history = append(history, HistoryMessage{
							Role:      "assistant",
							Content:   m.Content,
							Timestamp: m.Timestamp,
						})
					}
				}
			} else if len(m.ToolCalls) > 0 {
				// Intermediate assistant with tool_calls from incremental persistence.
				// Accumulate into pending — don't flush yet.
				finishCurIter()
				curIterIdx++
				curIterThinking = m.Content
				curIterReasoning = m.ReasoningContent
				for _, tc := range m.ToolCalls {
					// Determine tool status from the corresponding tool result message.
					// Tool errors are stored as content starting with "Error:" (see
					// engine_run_tools.go: updateToolResultLine sets llmContent prefix).
					status := "done"
					if content, ok := toolResults[tc.ID]; ok && strings.HasPrefix(content, "Error:") {
						status = "error"
					}
					curIterTools = append(curIterTools, protocol.ToolProgress{
						Name:      tc.Name,
						Label:     formatToolLabel(tc.Name, tc.Arguments),
						Status:    status,
						Elapsed:   0,
						Iteration: curIterIdx,
					})
				}
			} else if m.Content != "" {
				flushPending()
				// Merge with previous assistant message that had iterations but no content.
				// Backend stores iterations in a separate DisplayOnly assistant message
				// (Detail set, content empty), followed by the real assistant reply (content set).
				// We need to combine them into one HistoryMessage for unified rendering.
				if len(history) > 0 && history[len(history)-1].Role == "assistant" &&
					history[len(history)-1].Content == "" && len(history[len(history)-1].Iterations) > 0 {
					history[len(history)-1].Content = m.Content
					history[len(history)-1].Timestamp = m.Timestamp
				} else {
					history = append(history, HistoryMessage{
						Role:      "assistant",
						Content:   m.Content,
						Timestamp: m.Timestamp,
					})
				}
			}
		default:
			flushPending()
			// Reset lastAssistantTS after flushing: the next tool_summary
			// belongs to a new turn (this default case is typically "user"),
			// so it should use its own synthetic timestamp if that turn
			// is also interrupted (no assistant reply).
			lastAssistantTS = time.Time{}
			if m.Content != "" {
				history = append(history, HistoryMessage{
					Role:      m.Role,
					Content:   m.Content,
					Timestamp: m.Timestamp,
				})
			}
		}
	}
	flushPending()
	return history
}
