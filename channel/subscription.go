package channel

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"xbot/llm"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
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
	Args      string `json:"args,omitempty"`
	Detail    string `json:"detail,omitempty"`
	// UIMode/UILibs/UISurface carry the tool's UI capability + top-level panel
	// declaration from the persisted iteration snapshot. Without these the
	// frontend loses ui_mode on history (refresh) and renders display_html as a
	// plain folded tool card instead of the GenUI panel ("刷新后 panel 消失").
	UIMode    string              `json:"ui_mode,omitempty"`
	UILibs    []string            `json:"ui_libs,omitempty"`
	UISurface *protocol.UISurface `json:"ui_surface,omitempty"`
}

// isDegenerateCancelDetail reports whether a Detail JSON represents a
// degenerate restart-recovery snapshot: every iteration is a synthetic
// user_cancelled tool with no real content/reasoning/other tools. This is the
// ONLY case where ConvertMessagesToHistory should fall back to pendingIters
// (accumulated from tool_calls) — the resumed Run after a restart completed no
// iterations, so its Detail carries nothing but user_cancelled.
//
// A NORMAL Detail always has real iteration ids (e.g. 47, 48) with content or
// actual tools. The old `len(iters) < len(pendingIters)` check incorrectly
// fired on normal turns (2 intermediate tool_calls → pendingIters=2, Detail=1
// real iteration) and REPLACED the real ids with fabricated 1, 2 — the
// "加载会话后 iter 带着错误的 iter id" bug.
func isDegenerateCancelDetail(snaps []iterSnapshot) bool {
	if len(snaps) == 0 {
		return true
	}
	for _, snap := range snaps {
		if snap.Content != "" || snap.Reasoning != "" {
			return false // has real content → not degenerate
		}
		if len(snap.Tools) == 0 {
			continue // empty iteration — not real
		}
		for _, t := range snap.Tools {
			if t.Name != "user_cancelled" {
				return false // has a real tool → not degenerate
			}
		}
	}
	return true
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

// filterInternalMessages drops Internal messages from a render-facing message
// list. An Internal message is an LLM-side carrier, NOT user input: e.g. the
// view_image multimodal injection (OpenAI tool role cannot carry images, so the
// `![label](/api/files/viewimg/…)` refs ride a user-role message that reuses the
// turn_id of the user message that triggered it — see agent/engine_run.go
// injectViewImages).
//
// The render layer keeps exactly ONE user slot per turn, so rendering such a row
// REPLACES the user's own message: content becomes "📷 以下图片已通过 view_image
// 工具加载…" and the image URL switches from the uploaded file
// (/api/files/download?key=uploads/…) to the injection copy
// (/api/files/viewimg/<uuid>) — reported 2026-09-16.
//
// Only the render path filters. The LLM context reads via Replay/GetAllMessages,
// which keep Internal messages (the model must still see the image).
func filterInternalMessages(msgs []llm.ChatMessage) []llm.ChatMessage {
	hasInternal := false
	for i := range msgs {
		if msgs[i].Internal {
			hasInternal = true
			break
		}
	}
	if !hasInternal {
		return msgs
	}
	out := make([]llm.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Internal {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ConvertMessagesToHistory converts raw DB messages into HistoryMessages for CLI display.
// It handles three scenarios:
//  1. Normal completed turn: assistant with Detail → one tool_summary + assistant
//  2. Cancelled/interrupted turn: intermediate assistant(ToolCalls) without Detail → pending tool_summary
//  3. Mixed: some turns completed, last one cancelled
//
// ConvertMessagesToHistoryWithIterations is the v55+ version that uses
// structured iteration_history table data instead of parsing Detail JSON.
// turnIterMap maps turn_id → []IterationRecord (from DB, queried by turn_id).
// Intermediate messages (with tool_calls) go through the same flushPending
// flow as ConvertMessagesToHistory — they are NOT rendered as separate
// HistoryMessages. Only the final/[interrupted] message gets structured
// iteration data attached (queried by turn_id, merging all intermediate +
// final records into one complete list).
// Detail JSON is only used as a fallback for old data pre-v55.
func ConvertMessagesToHistoryWithIterations(msgs []llm.ChatMessage, turnIterMap map[uint64][]sqlite.IterationRecord) []HistoryMessage {
	// Internal 消息（仅模型可见的载体）不属于用户可见历史 —— 见
	// filterInternalMessages 的说明。
	msgs = filterInternalMessages(msgs)
	// If no structured data, fall back to the legacy path.
	if turnIterMap == nil {
		return ConvertMessagesToHistory(msgs)
	}
	// Check if any turn has structured iteration data.
	hasStructured := false
	for _, m := range msgs {
		if m.TurnID > 0 {
			if recs, ok := turnIterMap[m.TurnID]; ok && len(recs) > 0 {
				hasStructured = true
				break
			}
		}
	}
	if !hasStructured {
		return ConvertMessagesToHistory(msgs)
	}

	// Copy so the derivation below never mutates the caller's slice.
	msgs = append([]llm.ChatMessage(nil), msgs...)

	// Derive turn_id for legacy rows (same logic as ConvertMessagesToHistory).
	deriveTurnIDs(msgs)

	var history []HistoryMessage
	var pendingIters []HistoryIteration
	var curIterTools []protocol.ToolProgress
	var curIterIdx int
	var curIterThinking string
	var curIterReasoning string
	// pendingCompactions = 本轮历史转换中「turn 内发生的压缩」，循环结束后挂到
	// 对应 turn 的 assistant 行上（该行可能在标记之后才 append —— 挂载用后处理）。
	var pendingCompactions []pendingCompaction

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

	var lastAssistantTS time.Time
	var pendingTurnID uint64
	var lastAssistantID int64
	var syntheticIdx int
	// 同一 turn 的 assistant 行**只能有一条**（2026-09-17 实测修复）：v55 起回复文本存
	// iteration_history，session_messages 只留空壳占位，重启/续跑会各写一条空壳 ⇒
	// 旧实现为每条都追加一条 HistoryMessage（每条都重复带完整 turnIterMap 迭代 ≈272KB）
	// ⇒ 真实会话里同一 turn 重复 6 份（/api/history 12.9MB 中的 1.4MB）。
	// 这里按 turn 去重：迭代只附一次，真实最终回复文本（非空 content）并入那唯一一条。
	structuredRowIdx := map[uint64]int{}

	flushPending := func() {
		finishCurIter()
		// Reset the fabricated iteration counter — a flush always ends a turn
		// (turn boundary flush, user message, or final assistant). The next
		// turn's fallback (no turnIterMap) iteration numbers restart at 1.
		curIterIdx = 0
		if len(pendingIters) > 0 {
			// 该 turn 已经有权威行（structured）⇒ 绝不重复追加（见 structuredRowIdx）。
			if _, dup := structuredRowIdx[pendingTurnID]; dup && pendingTurnID > 0 {
				pendingIters = nil
				return
			}
			// v55: if structured iteration_history data exists for this turn,
			// use it as the authoritative source instead of fabricated pendingIters.
			// This handles BOTH cases:
			// 1. Turn with [interrupted] message: flushPending renders with
			//    turnIterMap data (real iteration ids), then [interrupted]
			//    message's !isIntermediate branch also renders — but
			//    hasCommitted check (same turnID + role + !isPartial) skips
			//    the duplicate.
			// 2. Turn WITHOUT [interrupted] message (cancelled mid-stream,
			//    no final message): flushPending is the ONLY render path —
			//    skipping it would lose ALL iterations.
			iters := pendingIters
			if pendingTurnID > 0 {
				if recs, ok := turnIterMap[pendingTurnID]; ok && len(recs) > 0 {
					iters = make([]HistoryIteration, 0, len(recs))
					for _, rec := range recs {
						var tools []protocol.ToolProgress
						if rec.Tools != "" && rec.Tools != "[]" {
							var snaps []iterToolSnap
							if json.Unmarshal([]byte(rec.Tools), &snaps) == nil {
								tools = make([]protocol.ToolProgress, len(snaps))
								for i, t := range snaps {
									label := t.Label
									if label == "" {
										label = t.Name
									}
									tools[i] = protocol.ToolProgress{
										Name: t.Name, Label: label, Status: t.Status,
										Elapsed: t.ElapsedMS, Iteration: rec.Iteration,
										Summary: t.Summary, Args: t.Args, Detail: t.Detail,
										UIMode: t.UIMode, UILibs: t.UILibs, UISurface: t.UISurface,
									}
								}
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration:    rec.Iteration,
							Content:      rec.Content,
							Reasoning:    rec.Reasoning,
							Tools:        tools,
							Tokens:       rec.Tokens,
							TTFTMs:       rec.TTFTMs,
							TokensPerSec: rec.TokensPerSec,
							TotalMs:      rec.TotalMs,
						})
					}
				}
			}
			ts := lastAssistantTS
			if ts.IsZero() {
				ts = time.Date(2024, 1, 1, 0, 0, 0, syntheticIdx, time.UTC)
				syntheticIdx++
			}
			history = append(history, HistoryMessage{
				ID:         lastAssistantID,
				HistoryID:  lastAssistantID,
				Role:       "assistant",
				Content:    "",
				Timestamp:  ts,
				Iterations: iters,
				TurnID:     pendingTurnID,
			})
			pendingIters = nil
		}
	}

	// Pre-scan tool messages for status fallback.
	toolResults := make(map[string]string)
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = m.Content
		}
	}

	var cmdAnchor uint64
	for _, m := range msgs {
		if m.TurnID > cmdAnchor {
			cmdAnchor = m.TurnID
		}
		if m.CommandRow {
			// 命令行（`!cmd` / slash）的输入与输出：**无 turn 的独立行** —— 输出
			// standalone + 锚点（= 走到这一行时已知的最新 turn），前端据此把它插回原位
			//（与实时渲染同一条 standalone 路径）。落库形态见 storage.HistoryRecordCommand
			//（display_only=1 + record_type='command'，永不进 LLM 上下文）。
			history = append(history, HistoryMessage{
				ID: m.ID, HistoryID: m.ID, Role: m.Role, Content: m.Content,
				Standalone: true, AnchorTurnID: cmdAnchor, Timestamp: m.Timestamp,
			})
			continue
		}
		if isCompactMarkerMsg(m) {
			// 压缩标记（[Compacted context]）：压缩在**迭代边界**触发（maybeCompress
			// 在 LLM 请求前），所以它属于「它所在的那个 turn」并渲染在**迭代之间**
			// （与迭代同级，Cursor 式 "context summarized"）。
			//
			// 定位 = 该 turn 中 created_at 早于压缩时刻的最大迭代号（AfterIteration）。
			// ⚠️ 绝不占用任何 turn 的 user 槽位（否则前端「每 turn 取第一条 user」会
			// 顶掉用户真实消息 —— P0 2026-09-26）。
			//
			// 老数据（无结构化迭代 / 迭代无时间戳）无法定位 ⇒ 回落为独立的
			// standalone 标记行（基本兼容，前端仍支持）。
			if after, ok := compactionIteration(turnIterMap, cmdAnchor, m.Timestamp); ok {
				pendingCompactions = append(pendingCompactions, pendingCompaction{
					turnID: cmdAnchor, afterIteration: after,
					content: m.Content, markerID: m.ID, timestamp: m.Timestamp,
				})
			} else {
				history = append(history, HistoryMessage{
					ID: m.ID, HistoryID: m.ID, Role: m.Role, Content: m.Content,
					Standalone: true, AnchorTurnID: cmdAnchor, Timestamp: m.Timestamp,
				})
			}
			continue
		}
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			lastAssistantTS = m.Timestamp
			lastAssistantID = m.ID

			// v55: structured iteration_history.
			// Intermediate messages (with tool_calls) go through the SAME
			// flushPending flow as ConvertMessagesToHistory — they are NOT
			// rendered as separate HistoryMessages. Only the FINAL message
			// (no tool_calls, has content) or [interrupted] message gets
			// structured iteration data attached, queried by turn_id (which
			// merges all intermediate + final records into one list).
			isIntermediate := len(m.ToolCalls) > 0
			if !isIntermediate && m.TurnID > 0 {
				if recs, ok := turnIterMap[m.TurnID]; ok && len(recs) > 0 {
					// Cross-turn guard (2026-08-23 turn 67→68 incident): the
					// pendingIters accumulated so far belong to a PREVIOUS
					// turn when the restart interrupted it mid-execution (no
					// final assistant of its own) and this empty-shell resume
					// assistant is the recovery turn's ONLY row (v55+ persists
					// reply text in iteration_history; the session_messages
					// row is an empty placeholder). Flushing at the boundary
					// renders the previous turn's iterations; the
					// `pendingIters = nil` below must NEVER evaporate them.
					if pendingTurnID > 0 && m.TurnID != pendingTurnID && len(pendingIters) > 0 {
						flushPending()
					}
					// Structured data available for this turn — use as
					// authoritative source. Build HistoryIteration list from
					// ALL records for this turn (intermediate + final).
					finishCurIter()
					pendingIters = nil

					iters := make([]HistoryIteration, 0, len(recs))
					for _, rec := range recs {
						var tools []protocol.ToolProgress
						if rec.Tools != "" && rec.Tools != "[]" {
							var snaps []iterToolSnap
							if json.Unmarshal([]byte(rec.Tools), &snaps) == nil {
								tools = make([]protocol.ToolProgress, len(snaps))
								for i, t := range snaps {
									label := t.Label
									if label == "" {
										label = t.Name
									}
									tools[i] = protocol.ToolProgress{
										Name:      t.Name,
										Label:     label,
										Status:    t.Status,
										Elapsed:   t.ElapsedMS,
										Iteration: rec.Iteration,
										Summary:   t.Summary,
										Args:      t.Args,
										Detail:    t.Detail,
										UIMode:    t.UIMode, UILibs: t.UILibs, UISurface: t.UISurface,
									}
								}
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: rec.Iteration,
							Content:   rec.Content,
							Reasoning: rec.Reasoning,
							Tools:     tools,
						})
					}

					if len(iters) > 0 {
						// 同一 turn 只能有一条 assistant 行：v55 空壳占位（重启/续跑各写一条）
						// 会让旧实现为**每条**都追加一份（都带同样迭代）⇒ 同一 turn 重复 N 份
						// （实测 6 份 × 272KB）。这里只把真实最终回复文本并进那唯一一条。
						if idx, dup := structuredRowIdx[m.TurnID]; dup {
							if m.Content != "" && !m.Interrupted && history[idx].Content == "" {
								history[idx].Content = m.Content
								history[idx].ID = m.ID
								history[idx].HistoryID = m.ID
								history[idx].Timestamp = m.Timestamp
							}
							continue
						}
						isInterrupted := m.Interrupted
						if m.Content != "" && !isInterrupted {
							structuredRowIdx[m.TurnID] = len(history)
							history = append(history, HistoryMessage{
								ID:         m.ID,
								Role:       "assistant",
								Content:    m.Content,
								Timestamp:  m.Timestamp,
								TurnID:     m.TurnID,
								Iterations: iters,
							})
						} else {
							structuredRowIdx[m.TurnID] = len(history)
							history = append(history, HistoryMessage{
								ID:         m.ID,
								Role:       "assistant",
								Content:    "",
								Timestamp:  m.Timestamp,
								TurnID:     m.TurnID,
								Iterations: iters,
							})
						}
					} else if m.Content != "" && !m.Interrupted {
						history = append(history, HistoryMessage{
							ID:        m.ID,
							Role:      "assistant",
							Content:   m.Content,
							Timestamp: m.Timestamp,
							TurnID:    m.TurnID,
						})
					}
					continue
				}
			}

			// Fallback: Detail JSON (old data without structured iteration_history)
			if m.Detail != "" {
				finishCurIter()

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
								Args:      t.Args,
								Detail:    t.Detail,
								UIMode:    t.UIMode, UILibs: t.UILibs, UISurface: t.UISurface,
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: snap.Iteration,
							Content:   snap.Content,
							Reasoning: snap.Reasoning,
							Tools:     toolList,
						})
					}

					if isDegenerateCancelDetail(snaps) && len(pendingIters) > 0 {
						last := &pendingIters[len(pendingIters)-1]
						for _, snap := range snaps {
							for _, t := range snap.Tools {
								label := t.Label
								if label == "" {
									label = t.Name
								}
								last.Tools = append(last.Tools, protocol.ToolProgress{
									Name:      t.Name,
									Label:     label,
									Status:    t.Status,
									Iteration: last.Iteration,
									UIMode:    t.UIMode, UILibs: t.UILibs, UISurface: t.UISurface,
								})
							}
						}
						iters = pendingIters
					}
					pendingIters = nil

					if len(iters) > 0 {
						isInterrupted := m.Interrupted
						if m.Content != "" && !isInterrupted {
							history = append(history, HistoryMessage{
								ID:         m.ID,
								Role:       "assistant",
								Content:    m.Content,
								Timestamp:  m.Timestamp,
								TurnID:     m.TurnID,
								Iterations: iters,
							})
						} else {
							history = append(history, HistoryMessage{
								ID:         m.ID,
								Role:       "assistant",
								Content:    "",
								Timestamp:  m.Timestamp,
								TurnID:     m.TurnID,
								Iterations: iters,
							})
						}
					} else if m.Content != "" && !m.Interrupted {
						history = append(history, HistoryMessage{
							ID:        m.ID,
							Role:      "assistant",
							Content:   m.Content,
							Timestamp: m.Timestamp,
							TurnID:    m.TurnID,
						})
					}
				}
			} else if len(m.ToolCalls) > 0 {
				// Intermediate assistant with tool_calls — accumulate into pending.
				// Turn boundary: two turns' intermediate messages can sit back to
				// back with NO user message between them (restart auto-recovery
				// continues an interrupted turn with a NEW turn_id). Without
				// flushing here, pendingIters mixes both turns and flushPending
				// replaces ALL of them with the LAST turn's turnIterMap records —
				// dropping every iteration of the earlier turn (fancy memory bug:
				// tenant 134262, turn 219 pre-restart iterations vanished).
				if pendingTurnID > 0 && m.TurnID != pendingTurnID {
					flushPending()
				}
				finishCurIter()
				curIterIdx++
				pendingTurnID = m.TurnID
				curIterThinking = m.Content
				curIterReasoning = m.ReasoningContent
				for _, tc := range m.ToolCalls {
					status := "done"
					if result, ok := toolResults[tc.ID]; ok && strings.HasPrefix(result, "Error:") {
						status = "error"
					}
					curIterTools = append(curIterTools, protocol.ToolProgress{
						Name:      tc.Name,
						Label:     tc.Name,
						Status:    status,
						Iteration: curIterIdx,
					})
				}
			} else if m.Content != "" {
				flushPending()
				history = append(history, HistoryMessage{
					ID:        m.ID,
					Role:      "assistant",
					Content:   m.Content,
					Timestamp: m.Timestamp,
					TurnID:    m.TurnID,
				})
			}
		case "user":
			flushPending()
			content := m.Content
			history = append(history, HistoryMessage{
				ID:        m.ID,
				Role:      "user",
				Content:   content,
				Timestamp: m.Timestamp,
				TurnID:    m.TurnID,
			})
		}
	}
	flushPending()
	history = attachCompactions(history, pendingCompactions)
	return history
}

// attachCompactions attaches each pending in-turn compaction to its turn's
// assistant row (前端渲染在迭代之间 —— 与迭代同级)。该 turn 的行不在本次窗口
// 内（分页裁剪）时回落为独立的 standalone 标记行 —— 信息绝不丢失（老数据 /
// 边界场景的基本兼容）。
func attachCompactions(history []HistoryMessage, pending []pendingCompaction) []HistoryMessage {
	if len(pending) == 0 {
		return history
	}
	rowIdx := make(map[uint64]int, len(history))
	for i := range history {
		if history[i].Role == "assistant" && history[i].TurnID > 0 {
			if _, ok := rowIdx[history[i].TurnID]; !ok {
				rowIdx[history[i].TurnID] = i
			}
		}
	}
	for _, c := range pending {
		if idx, ok := rowIdx[c.turnID]; ok {
			history[idx].Compactions = append(history[idx].Compactions, protocol.HistoryCompaction{
				AfterIteration: c.afterIteration,
				Content:        c.content,
				Timestamp:      c.timestamp,
			})
			continue
		}
		history = append(history, HistoryMessage{
			ID: c.markerID, HistoryID: c.markerID, Role: "user", Content: c.content,
			Standalone: true, AnchorTurnID: c.turnID, Timestamp: c.timestamp,
		})
	}
	return history
}

// deriveTurnIDs derives turn_id for legacy rows (turn_id=0).
// Extracted from ConvertMessagesToHistory for reuse.
// isCompactMarkerMsg reports whether a history message is the [Compacted context]
// summary marker (see agent/compress.go: llm.NewUserMessage("[Compacted context]\n\n"+summary)).
//
// The marker is written as a **user** role message with turn_id=0 and sits at its
// stream position (the compress record's id, which can fall INSIDE a turn's id
// range). It must NOT participate in turn derivation: binding it to a turn makes
// the frontend treat it as that turn's user row and DROP the real user message
// (P0 2026-09-26: user sent "继续", no compaction happened, but the UI replaced
// their message with a previous compaction's marker — the marker had been bound
// to the FOLLOWING turn by deriveTurnIDs).
func isCompactMarkerMsg(m llm.ChatMessage) bool {
	return m.Role == "user" && strings.HasPrefix(strings.TrimSpace(m.Content), "[Compacted context]")
}

// pendingCompaction = 一个「turn 内发生」的压缩，等待挂到它所属 turn 的 assistant 行。
type pendingCompaction struct {
	turnID         uint64
	afterIteration int
	content        string
	markerID       int64
	timestamp      time.Time
}

// compactionIteration returns the iteration number after which the compaction
// recorded at ts happened inside turn turnID — 压缩由 agent.maybeCompress 在
// LLM 请求**之前**触发，即迭代边界，故渲染在「迭代 N 与 N+1 之间」。
//
// ok=false 当位置无法确定（该 turn 无结构化迭代，或迭代记录都没有 created_at
// —— 老数据），调用方回落为独立的 standalone 标记行（基本兼容）。
func compactionIteration(turnIterMap map[uint64][]sqlite.IterationRecord, turnID uint64, ts time.Time) (int, bool) {
	if turnID == 0 || ts.IsZero() {
		return 0, false
	}
	recs, ok := turnIterMap[turnID]
	if !ok || len(recs) == 0 {
		return 0, false
	}
	after, anyTS := 0, false
	for _, rec := range recs {
		if rec.CreatedAt.IsZero() {
			continue
		}
		anyTS = true
		if rec.CreatedAt.Before(ts) && rec.Iteration > after {
			after = rec.Iteration
		}
	}
	if !anyTS {
		return 0, false
	}
	return after, true
}

func deriveTurnIDs(msgs []llm.ChatMessage) {
	// Pass 1 (forward, user only): assign the first turn_id>0 to preceding user rows.
	nextTurnID := uint64(0)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].CommandRow || isCompactMarkerMsg(msgs[i]) {
			continue // 命令行 / 压缩标记：无 turn 的独立行，绝不参与 turn 推导
		}
		if msgs[i].Role == "user" && msgs[i].TurnID > 0 {
			nextTurnID = msgs[i].TurnID
		}
		if msgs[i].Role == "user" && msgs[i].TurnID == 0 && nextTurnID > 0 {
			msgs[i].TurnID = nextTurnID
		}
	}
	// Pass 2 (backward): assign nearest preceding turn_id>0 to assistant/tool rows.
	var lastTurnID uint64
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].CommandRow || isCompactMarkerMsg(msgs[i]) {
			continue // 同上
		}
		if msgs[i].TurnID > 0 {
			lastTurnID = msgs[i].TurnID
		} else if lastTurnID > 0 {
			msgs[i].TurnID = lastTurnID
		}
	}
	// Pass 3 (forward, assistant/tool only): rows still at turn 0 after Pass 2 —
	// the trailing final-reply shape. SubAgent completion paths historically
	// persisted the final assistant message WITHOUT TurnID (spawn/send
	// AppendMessage before the fix), while the Run's intermediate rows carry
	// turn_id=N. The trailing un-stamped row is hit FIRST by the backward scan
	// (lastTurnID still 0), so Pass 2 cannot derive it. Forward scan assigns
	// the nearest PRECEDING turn>0 (its own turn's iterations) — without this,
	// the row renders with turnID=0 and the frontend (turnID, roleRank) sort
	// pins it ABOVE the user message that triggered it ("user message renders
	// below the assistant reply" in the SubAgent session view).
	var prevTurnID uint64
	for i := 0; i < len(msgs); i++ {
		if msgs[i].CommandRow || isCompactMarkerMsg(msgs[i]) {
			continue // 同上
		}
		if msgs[i].TurnID > 0 {
			prevTurnID = msgs[i].TurnID
		} else if prevTurnID > 0 && msgs[i].Role != "user" {
			msgs[i].TurnID = prevTurnID
		}
	}
}

func ConvertMessagesToHistory(msgs []llm.ChatMessage) []HistoryMessage {
	// Internal 消息（仅模型可见的载体）不属于用户可见历史 —— 见
	// filterInternalMessages 的说明。
	msgs = filterInternalMessages(msgs)
	// Copy so the derivation below never mutates the caller's slice.
	msgs = append([]llm.ChatMessage(nil), msgs...)

	// Derive turn_id for legacy rows (turn_id=0, written before eager-save
	// stamped it). Two passes:
	//  Pass 1 (forward, user only): a user row belongs to the first turn_id>0
	//    row that follows it before the next user row. Deterministic — based on
	//    the append-only row order.
	//  Pass 2 (backward, all roles): an assistant/tool row with turn_id=0
	//    belongs to the same turn as the nearest preceding message with
	//    turn_id>0 (typically the user message of this turn). Without this,
	//    flushPending's pendingTurnID=0 → the tool_summary has turn_id=0 →
	//    the frontend's turnID:role dedup in loadMore can't match it against
	//    the final assistant (same turn, different batch), causing duplicate
	//    assistant messages at batch boundaries within a super-long turn.
	for i := range msgs {
		if msgs[i].CommandRow || isCompactMarkerMsg(msgs[i]) {
			continue // 命令行（`!cmd`）/ 压缩标记无 turn：不参与推导（同 deriveTurnIDs）
		}
		if msgs[i].Role != "user" || msgs[i].TurnID > 0 {
			continue
		}
		for j := i + 1; j < len(msgs); j++ {
			if msgs[j].Role == "user" {
				break
			}
			if msgs[j].TurnID > 0 {
				msgs[i].TurnID = msgs[j].TurnID
				break
			}
		}
	}
	// Pass 2: backward search for assistant messages with turn_id=0.
	// Stops at the preceding user message (turn boundary).
	for i := range msgs {
		if msgs[i].CommandRow || isCompactMarkerMsg(msgs[i]) {
			continue // 命令行（`!cmd`）/ 压缩标记无 turn：不参与推导（同 deriveTurnIDs）
		}
		if msgs[i].TurnID > 0 {
			continue
		}
		for j := i - 1; j >= 0; j-- {
			if msgs[j].Role == "user" {
				// Don't cross into the previous turn.
				if msgs[j].TurnID > 0 {
					msgs[i].TurnID = msgs[j].TurnID
				}
				break
			}
			if msgs[j].TurnID > 0 {
				msgs[i].TurnID = msgs[j].TurnID
				break
			}
		}
	}

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
	// pendingTurnID tracks the TurnID of the last assistant message with tool_calls.
	// flushPending() uses it to stamp the generated HistoryMessage so the frontend
	// can match it against the live store's TurnID (same-turn dedup).
	var pendingTurnID uint64
	var lastAssistantID int64
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
				ID:         lastAssistantID,
				HistoryID:  lastAssistantID,
				Role:       "assistant",
				Content:    "",
				Timestamp:  ts,
				Iterations: pendingIters,
				TurnID:     pendingTurnID,
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

	var cmdAnchor uint64
	for _, m := range msgs {
		if m.TurnID > cmdAnchor {
			cmdAnchor = m.TurnID
		}
		if m.CommandRow {
			// 命令行（`!cmd` / slash）的输入与输出：**无 turn 的独立行** —— 输出
			// standalone + 锚点（= 走到这一行时已知的最新 turn），前端据此把它插回原位
			//（与实时渲染同一条 standalone 路径）。落库形态见 storage.HistoryRecordCommand
			//（display_only=1 + record_type='command'，永不进 LLM 上下文）。
			history = append(history, HistoryMessage{
				ID: m.ID, HistoryID: m.ID, Role: m.Role, Content: m.Content,
				Standalone: true, AnchorTurnID: cmdAnchor, Timestamp: m.Timestamp,
			})
			continue
		}
		if isCompactMarkerMsg(m) {
			// 压缩标记（[Compacted context]）：无 turn 的独立展示行 —— 绝不占用任何
			// turn 的 user 槽位（否则前端「每 turn 取第一条 user」会顶掉用户真实消息，
			// P0 2026-09-26）。同 CommandRow：按锚点插回原位。
			history = append(history, HistoryMessage{
				ID: m.ID, HistoryID: m.ID, Role: m.Role, Content: m.Content,
				Standalone: true, AnchorTurnID: cmdAnchor, Timestamp: m.Timestamp,
			})
			continue
		}
		switch m.Role {
		case "tool":
			continue
		case "assistant":
			lastAssistantTS = m.Timestamp
			lastAssistantID = m.ID
			if m.Detail != "" {
				// Detail has authoritative iteration history. Discard pending iters
				// from intermediate assistant messages — they lack elapsed/label data.
				finishCurIter()

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
								Args:      t.Args,
								Detail:    t.Detail,
								UIMode:    t.UIMode, UILibs: t.UILibs, UISurface: t.UISurface,
							}
						}
						iters = append(iters, HistoryIteration{
							Iteration: snap.Iteration,
							Content:   snap.Content,
							Reasoning: snap.Reasoning,
							Tools:     toolList,
						})
					}

					// Restart recovery: ONLY when the Detail is degenerate — every
					// iteration is a synthetic user_cancelled tool with no real
					// content/reasoning/tools (the resumed Run after a restart
					// completed no iterations, so out.IterationHistory was empty
					// and handleCancelledRun fell back to user_cancelled only) —
					// should we use the pendingIters accumulated from tool_calls
					// (which carry the real pre-restart iterations).
					//
					// CRITICAL: the old check `len(iters) < len(pendingIters)` was
					// WRONG — a normal turn with 2 intermediate tool_calls assistant
					// messages accumulates 2 pendingIters (fabricated ids 1, 2 via
					// curIterIdx++) while the Detail has 1 REAL iteration (e.g. 47).
					// `1 < 2` triggered the branch and REPLACED the real id (47) with
					// fabricated sequential ids (1, 2) — the "加载会话后 iter 带着错误
					// 的 iter id" bug. Only a truly degenerate Detail (all
					// user_cancelled, no real content) warrants the fallback.
					if isDegenerateCancelDetail(snaps) && len(pendingIters) > 0 {
						if len(pendingIters) > 0 {
							last := &pendingIters[len(pendingIters)-1]
							for _, snap := range snaps {
								for _, t := range snap.Tools {
									label := t.Label
									if label == "" {
										label = t.Name
									}
									last.Tools = append(last.Tools, protocol.ToolProgress{
										Name:      t.Name,
										Label:     label,
										Status:    t.Status,
										Iteration: last.Iteration,
										UIMode:    t.UIMode, UILibs: t.UILibs, UISurface: t.UISurface,
									})
								}
							}
						}
						iters = pendingIters
					}
					pendingIters = nil

					if len(iters) > 0 {
						// Interrupted messages (m.Interrupted=true) carry cancelled-turn
						// iteration history. Use empty Content so the UI shows only the
						// progress block, not the "[interrupted]" marker text.
						isInterrupted := m.Interrupted
						if m.Content != "" && !isInterrupted {
							history = append(history, HistoryMessage{
								ID:         m.ID,
								Role:       "assistant",
								Content:    m.Content,
								Timestamp:  m.Timestamp,
								TurnID:     m.TurnID,
								Iterations: iters,
							})
						} else {
							// Detail has iterations but no displayable content
							// (intermediate assistant, cancelled turn, or [interrupted] marker).
							history = append(history, HistoryMessage{
								ID:         m.ID,
								Role:       "assistant",
								Content:    "",
								Timestamp:  m.Timestamp,
								TurnID:     m.TurnID,
								Iterations: iters,
							})
						}
					} else if m.Content != "" && !m.Interrupted {
						history = append(history, HistoryMessage{
							ID:        m.ID,
							Role:      "assistant",
							Content:   m.Content,
							Timestamp: m.Timestamp,
							TurnID:    m.TurnID,
						})
					}
				}
			} else if len(m.ToolCalls) > 0 {
				// Intermediate assistant with tool_calls from incremental persistence.
				// Accumulate into pending — don't flush yet.
				finishCurIter()
				curIterIdx++
				pendingTurnID = m.TurnID
				curIterThinking = m.Content
				curIterReasoning = m.ReasoningContent
				for _, tc := range m.ToolCalls {
					// Determine tool status from the corresponding tool result message.
					// Tool errors are stored as content starting with "Error:" (see
					// engine_run_tools.go: updateToolResultLine sets llmContent prefix).
					status := "done"
					result, hasResult := toolResults[tc.ID]
					if hasResult && strings.HasPrefix(result, "Error:") {
						status = "error"
					}
					tp := protocol.ToolProgress{
						Name:      tc.Name,
						Label:     formatToolLabel(tc.Name, tc.Arguments),
						Status:    status,
						Elapsed:   0,
						Iteration: curIterIdx,
					}
					// Injected notification tools: their result IS user-facing content
					// (notification/interjection text, markdown). Carry it in the unified
					// Detail field (like Shell/Read do) so the web renderer can show it.
					if hasResult && tools.IsSyntheticToolName(tc.Name) {
						tp.Detail = tools.TruncateHeadPreview(result, maxHistoryToolPreview)
					}
					curIterTools = append(curIterTools, tp)
				}
			} else if m.Content != "" {
				flushPending()
				// Merge with previous assistant message that had iterations but no content.
				// Backend stores iterations in a separate DisplayOnly assistant message
				// (Detail set, content empty), followed by the real assistant reply (content set).
				// We need to combine them into one HistoryMessage for unified rendering.
				if len(history) > 0 && history[len(history)-1].Role == "assistant" &&
					history[len(history)-1].Content == "" && len(history[len(history)-1].Iterations) > 0 {
					// Stamp BOTH ID and HistoryID with the final assistant's DB id.
					// The row was created by flushPending() with ID=0 (or by the
					// Detail path with an older ID); leaving ID stale/zero makes
					// json:"id,omitempty" drop the field → frontend falls back to
					// a batch-index temp ID (hist-${i}), breaking loadMore dedup.
					history[len(history)-1].ID = m.ID
					history[len(history)-1].HistoryID = m.ID
					history[len(history)-1].Content = m.Content
					history[len(history)-1].Timestamp = m.Timestamp
					history[len(history)-1].TurnID = m.TurnID
				} else {
					hm := HistoryMessage{
						ID:        m.ID,
						Role:      "assistant",
						Content:   m.Content,
						Timestamp: m.Timestamp,
						TurnID:    m.TurnID,
					}
					// For turns with no tools, Detail is not set (snapshotCompletedIteration
					// is only called from executeToolCalls). ReasoningContent is on the
					// ChatMessage but would be lost without wrapping it in an iteration.
					if m.ReasoningContent != "" {
						hm.Iterations = []HistoryIteration{{
							Iteration: 1, // 1-based, consistent with engine
							Reasoning: m.ReasoningContent,
						}}
					}
					history = append(history, hm)
				}
			}
		default:
			flushPending()
			// Reset lastAssistantTS after flushing: the next tool_summary
			// belongs to a new turn (this default case is typically "user"),
			// so it should use its own synthetic timestamp if that turn
			// is also interrupted (no assistant reply).
			lastAssistantTS = time.Time{}
			lastAssistantID = 0
			if m.Content != "" {
				history = append(history, HistoryMessage{
					ID:        m.ID,
					Role:      m.Role,
					Content:   m.Content,
					Timestamp: m.Timestamp,
					TurnID:    m.TurnID,
				})
			}
		}
	}
	flushPending()
	return history
}

// ConvertHistoryRecords exposes one row for every raw message and compression
// marker while keeping internal history controls private.
func ConvertHistoryRecords(records []sqlite.HistoryRecord) []HistoryMessage {
	ordered := append([]sqlite.HistoryRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return ordered[i].HistoryID < ordered[j].HistoryID
	})

	toolResults := make(map[string]string)
	for _, record := range ordered {
		if record.Type == sqlite.HistoryRecordMessage && record.Message.Role == "tool" && record.Message.ToolCallID != "" {
			toolResults[record.Message.ToolCallID] = record.Message.Content
		}
	}

	history := make([]HistoryMessage, 0, len(ordered))
	for _, record := range ordered {
		if record.Type == sqlite.HistoryRecordMessage {
			message := record.Message

			// Skip tool-role messages: their results are already embedded in
			// the preceding assistant message's Detail/iterations. Emitting
			// them as separate HistoryMessage rows causes the frontend to
			// render them as assistant messages (with copy buttons) and leak
			// raw tool output. This mirrors master's ConvertMessagesToHistory
			// which does `case "tool": continue`.
			if message.Role == "tool" {
				continue
			}

			// Skip display_only messages: these are synthetic tool pairs
			// (background notifications, user_cancelled) and intermediate
			// iteration snapshots. Replay() also filters them out (line 891).
			// Including them causes the frontend to render synthetic text
			// like "A background task has completed..." as final assistant
			// content with copy buttons.
			if message.DisplayOnly {
				continue
			}

			timestamp := message.Timestamp
			if timestamp.IsZero() {
				timestamp = record.CreatedAt
			}
			toolCalls := make([]protocol.HistoryToolCall, len(message.ToolCalls))
			for i, call := range message.ToolCalls {
				toolCalls[i] = protocol.HistoryToolCall{
					ID: call.ID, Name: call.Name, Arguments: call.Arguments,
				}
			}
			// Intermediate assistant with ToolCalls but no Detail: content is
			// the LLM's narration, not the final reply. rawMessageIterations
			// puts it in the iteration's Content (thinking). Set message
			// content empty to prevent shouldRenderFinalContent from treating
			// it as the final reply (which would add a copy button).
			emitContent := message.Content
			iters := rawMessageIterations(message, toolResults)
			if len(iters) > 0 && message.Detail == "" && len(message.ToolCalls) > 0 {
				emitContent = ""
			}
			history = append(history, HistoryMessage{
				ID:               record.HistoryID,
				HistoryID:        record.HistoryID,
				Role:             message.Role,
				Content:          emitContent,
				ReasoningContent: message.ReasoningContent,
				ToolCallID:       message.ToolCallID,
				ToolName:         message.ToolName,
				ToolArguments:    message.ToolArguments,
				ToolCalls:        toolCalls,
				Timestamp:        timestamp,
				TurnID:           message.TurnID,
				Iterations:       iters,
				RecordType:       string(sqlite.HistoryRecordMessage),
				CompactedBy:      record.CompactedBy,
				DisplayOnly:      message.DisplayOnly,
			})
			continue
		}
		if record.Type != sqlite.HistoryRecordCompress {
			continue
		}
		control := HistoryMessage{
			ID: record.HistoryID, HistoryID: record.HistoryID, Role: "control", Timestamp: record.CreatedAt, RecordType: string(record.Type),
			TargetHistoryID: record.TargetHistoryID, CompactedBy: record.CompactedBy,
		}
		control.Role = "system"
		control.Content = "[Compacted context]"
		var snapshot sqlite.ContextSnapshot
		if err := json.Unmarshal(record.Data, &snapshot); err == nil {
			for _, msg := range snapshot.Messages {
				if strings.HasPrefix(strings.TrimSpace(msg.Content), "[Compacted context]") {
					control.Content = msg.Content
					break
				}
			}
		}
		if record.Compression != nil {
			control.Compression = &protocol.HistoryCompression{
				StartHistoryID:   record.Compression.StartHistoryID,
				EndHistoryID:     record.Compression.EndHistoryID,
				SourceHistoryIDs: append([]int64(nil), record.Compression.SourceHistoryIDs...),
			}
		}
		history = append(history, control)
	}
	return history
}

// maxHistoryToolPreview bounds the tool-result text a reconstructed history row
// carries in the unified Detail field (injected notification tools only — their
// result IS user-facing content). Rune-safe truncation via tools.TruncateHeadPreview.
const maxHistoryToolPreview = 2000

func rawMessageIterations(message llm.ChatMessage, toolResults map[string]string) []HistoryIteration {
	if message.Detail != "" {
		var snapshots []iterSnapshot
		if err := json.Unmarshal([]byte(message.Detail), &snapshots); err == nil {
			iterations := make([]HistoryIteration, len(snapshots))
			for i, snapshot := range snapshots {
				tools := make([]protocol.ToolProgress, len(snapshot.Tools))
				for j, tool := range snapshot.Tools {
					label := tool.Label
					if label == "" {
						label = tool.Name
					}
					tools[j] = protocol.ToolProgress{
						Name: tool.Name, Label: label, Status: tool.Status,
						Elapsed: tool.ElapsedMS, Iteration: snapshot.Iteration,
						Summary: tool.Summary, Args: tool.Args, Detail: tool.Detail,
						UIMode: tool.UIMode, UILibs: tool.UILibs, UISurface: tool.UISurface,
					}
				}
				iterations[i] = HistoryIteration{
					Iteration: snapshot.Iteration, Content: snapshot.Content,
					Reasoning: snapshot.Reasoning, Tools: tools,
				}
			}
			return iterations
		}
	}
	if message.Role != "assistant" || (len(message.ToolCalls) == 0 && message.ReasoningContent == "") {
		return nil
	}
	toolEntries := make([]protocol.ToolProgress, len(message.ToolCalls))
	for i, call := range message.ToolCalls {
		status := "done"
		result, hasResult := toolResults[call.ID]
		if hasResult && strings.HasPrefix(result, "Error:") {
			status = "error"
		}
		// Injected notification tools carry their (user-facing) result text in the
		// unified Detail field — same field Shell/Read use for their output — so the
		// web renderer can show it (as markdown) instead of "no structured data".
		detail := ""
		if hasResult && tools.IsSyntheticToolName(call.Name) {
			detail = tools.TruncateHeadPreview(result, maxHistoryToolPreview)
		}
		toolEntries[i] = protocol.ToolProgress{
			Name: call.Name, Label: formatToolLabel(call.Name, call.Arguments),
			Status: status, Iteration: 1, Detail: detail,
		}
	}
	// The intermediate assistant's Content is the LLM's narration ("两端就绪 ✅..."),
	// NOT the final reply. Put it in the iteration's Content (frontend maps to
	// thinking) so it renders inside the iteration fold, not as message.content
	// (which would get a copy button via shouldRenderFinalContent).
	return []HistoryIteration{{Iteration: 1, Content: message.Content, Reasoning: message.ReasoningContent, Tools: toolEntries}}
}

// ⛔ 每 turn 的迭代**必须完整下发**（用户 2026-09-21 定稿：「不能有任何 gap，任何 gap 都是
// 破坏线性一致性」）—— 这里**禁止**再引入任何"有界窗口/尾部截断"。
//
// 历史教训：曾用 BoundHistoryIterations 把每个 turn 截到最近 60 个（2026-09-15 为压 payload
// 体积）。截断的代价是**用户会看到迭代缺失**（turn-1-c 的 iter-range=60-119、1..59 不见），
// 而且当时**没有取回通路**（全 history 搜索 `before_iteration` 零命中）⇒ 永久缺。
// 体积/渲染性能归**渲染层**（TurnBody 的迭代级窗口化：只挂载视口附近的块 + contain，代价与
// 迭代数解耦），绝不以丢数据换体积。
