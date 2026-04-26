package agent

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AgentMetrics Agent runtime metrics (global singleton, resets on process restart).
// Uses atomic operations to ensure concurrency safety with zero locks.
type AgentMetrics struct {
	StartTime time.Time // Process start time

	// === Conversation metrics ===
	TotalConversations atomic.Int64 // Total conversations (each user message counts as one)
	TotalIterations    atomic.Int64 // Total Agent iterations
	TotalToolCalls     atomic.Int64 // Total tool calls
	TotalLLMCalls      atomic.Int64 // Total LLM API calls
	TotalInputTokens   atomic.Int64 // Total input tokens
	TotalOutputTokens  atomic.Int64 // Total output tokens

	// === Context management metrics (core — measuring four-layer defense effectiveness) ===
	MaskingEvents     atomic.Int64 // Observation Masking trigger count
	MaskedItems       atomic.Int64 // Masked tool result count
	OffloadEvents     atomic.Int64 // Offload trigger count
	OffloadedItems    atomic.Int64 // Offloaded (written to disk) tool result count
	OffloadedRecalls  atomic.Int64 // offload_recall unique ID callback count (deduplicated)
	MaskedRecalls     atomic.Int64 // recall_masked unique ID callback count (deduplicated)
	CompressEvents    atomic.Int64 // Context compression trigger count
	CompressTokensIn  atomic.Int64 // Total tokens before compression
	CompressTokensOut atomic.Int64 // Total tokens after compression
	ContextEditEvents atomic.Int64 // Context Editing trigger count
	SummaryRefines    atomic.Int64 // Summary refinement trigger count

	// === Efficiency metrics ===
	TotalToolErrors atomic.Int64 // Tool execution错误次数
	TotalLLMErrors  atomic.Int64 // LLM call error count

	// === Deduplication tracking (recall rate calculated based on unique items) ===
	recalledOffloadIDs sync.Map // string -> struct{}, unique offload IDs that have been recalled
	recalledMaskedIDs  sync.Map // string -> struct{}, unique mask IDs that have been recalled
}

// RecordOffloadRecall records one offload_recall call (deduplicated: same ID counted only once).
// Returns true if first callback.
func (m *AgentMetrics) RecordOffloadRecall(id string) bool {
	if _, loaded := m.recalledOffloadIDs.LoadOrStore(id, struct{}{}); loaded {
		return false
	}
	m.OffloadedRecalls.Add(1)
	return true
}

// RecordMaskedRecall records one recall_masked call (deduplicated: same ID counted only once).
// Returns true if first callback.
func (m *AgentMetrics) RecordMaskedRecall(id string) bool {
	if _, loaded := m.recalledMaskedIDs.LoadOrStore(id, struct{}{}); loaded {
		return false
	}
	m.MaskedRecalls.Add(1)
	return true
}

// ClearRecallTracking clears recall tracking data (called at conversation end).
func (m *AgentMetrics) ClearRecallTracking() {
	m.recalledOffloadIDs.Range(func(key, _ any) bool {
		m.recalledOffloadIDs.Delete(key)
		return true
	})
	m.recalledMaskedIDs.Range(func(key, _ any) bool {
		m.recalledMaskedIDs.Delete(key)
		return true
	})
}

// MetricsSnapshot metrics snapshot (for Settings display).
type MetricsSnapshot struct {
	UptimeSeconds      int64
	TotalConversations int64
	TotalIterations    int64
	TotalToolCalls     int64
	TotalLLMCalls      int64
	TotalInputTokens   int64
	TotalOutputTokens  int64

	// Context management
	MaskingEvents     int64
	MaskedItems       int64
	OffloadEvents     int64
	OffloadedItems    int64
	OffloadedRecalls  int64
	MaskedRecalls     int64
	CompressEvents    int64
	CompressTokensIn  int64
	CompressTokensOut int64
	ContextEditEvents int64
	SummaryRefines    int64

	// Efficiency
	TotalToolErrors int64
	TotalLLMErrors  int64

	// Calculated metrics
	AvgTokensPerIter float64 // Average input tokens per iteration
	CompressRatio    float64 // Overall compression ratio (out/in)
	RecallRate       float64 // Recall rate (recalls / offloads+maskings)

	// Compression efficiency
	AvgTokensSavedPerCompress float64 // Average tokens saved per compression
	TokenSavingRate           float64 // Token saving rate (saved/in)

	// Memory Quality
	OffloadRecallRate float64 // Offload recall rate
	MaskedRecallRate  float64 // masked 回调率
	SummaryRefineRate float64 // Summary refine rate = SummaryRefines / CompressEvents
	ContextEditRate   float64 // Context edit rate = ContextEditEvents / TotalConversations

	// Task completion effectiveness
	ToolSuccessRate     float64 // Tool success rate
	LLMSuccessRate      float64 // LLM success rate
	AvgToolCallsPerConv float64 // Average tool calls per conversation

	// Cost efficiency
	AvgTokensPerConv float64 // Average tokens per conversation (input+output)
	OutputInputRatio float64 // Output/input ratio

	// Four-Layer Defense Effectiveness
	CombinedSavingRate float64 // Four-layer combined saving rate = total saved / total input
}

// GlobalMetrics global metrics singleton.
var GlobalMetrics *AgentMetrics

func init() {
	GlobalMetrics = &AgentMetrics{
		StartTime: time.Now(),
	}
}

// RecordConversation records one conversation completion.
// llmCalls parameter as fallback validation: if direct counting in engine.go misses some paths,
// RecordConversation can still fill in via this parameter.
func (m *AgentMetrics) RecordConversation(iterations, toolCalls, llmCalls, inputTokens, outputTokens int) {
	m.TotalConversations.Add(1)
	m.TotalIterations.Add(int64(iterations))
	m.TotalToolCalls.Add(int64(toolCalls))
	m.TotalLLMCalls.Add(int64(llmCalls))
	m.TotalInputTokens.Add(int64(inputTokens))
	m.TotalOutputTokens.Add(int64(outputTokens))
}

// Snapshot gets metrics snapshot (for Settings display).
func (m *AgentMetrics) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}

	totalMasked := m.MaskedItems.Load()
	totalOffloaded := m.OffloadedItems.Load()
	totalRecalls := m.OffloadedRecalls.Load() + m.MaskedRecalls.Load()
	totalEvictions := totalMasked + totalOffloaded

	s := MetricsSnapshot{
		UptimeSeconds:      int64(time.Since(m.StartTime).Seconds()),
		TotalConversations: m.TotalConversations.Load(),
		TotalIterations:    m.TotalIterations.Load(),
		TotalToolCalls:     m.TotalToolCalls.Load(),
		TotalLLMCalls:      m.TotalLLMCalls.Load(),
		TotalInputTokens:   m.TotalInputTokens.Load(),
		TotalOutputTokens:  m.TotalOutputTokens.Load(),

		MaskingEvents:     m.MaskingEvents.Load(),
		MaskedItems:       totalMasked,
		OffloadEvents:     m.OffloadEvents.Load(),
		OffloadedItems:    totalOffloaded,
		OffloadedRecalls:  m.OffloadedRecalls.Load(),
		MaskedRecalls:     m.MaskedRecalls.Load(),
		CompressEvents:    m.CompressEvents.Load(),
		CompressTokensIn:  m.CompressTokensIn.Load(),
		CompressTokensOut: m.CompressTokensOut.Load(),
		ContextEditEvents: m.ContextEditEvents.Load(),
		SummaryRefines:    m.SummaryRefines.Load(),

		TotalToolErrors: m.TotalToolErrors.Load(),
		TotalLLMErrors:  m.TotalLLMErrors.Load(),
	}

	// Calculated metrics
	iters := m.TotalIterations.Load()
	if iters > 0 {
		s.AvgTokensPerIter = float64(s.TotalInputTokens) / float64(iters)
	}

	tokensIn := m.CompressTokensIn.Load()
	if tokensIn > 0 {
		s.CompressRatio = float64(m.CompressTokensOut.Load()) / float64(tokensIn)
	}

	if totalEvictions > 0 {
		s.RecallRate = float64(totalRecalls) / float64(totalEvictions)
	}

	// Compression efficiency
	if s.CompressEvents > 0 {
		s.AvgTokensSavedPerCompress = float64(s.CompressTokensIn-s.CompressTokensOut) / float64(s.CompressEvents)
	}
	if s.CompressTokensIn > 0 {
		s.TokenSavingRate = float64(s.CompressTokensIn-s.CompressTokensOut) / float64(s.CompressTokensIn)
	}

	// Memory Quality
	if s.OffloadedItems > 0 {
		s.OffloadRecallRate = float64(s.OffloadedRecalls) / float64(s.OffloadedItems)
	}
	if s.MaskedItems > 0 {
		s.MaskedRecallRate = float64(s.MaskedRecalls) / float64(s.MaskedItems)
	}
	if s.CompressEvents > 0 {
		s.SummaryRefineRate = float64(s.SummaryRefines) / float64(s.CompressEvents)
	}
	if s.TotalConversations > 0 {
		s.ContextEditRate = float64(s.ContextEditEvents) / float64(s.TotalConversations)
	}

	// Task completion effectiveness
	if s.TotalToolCalls > 0 {
		s.ToolSuccessRate = float64(s.TotalToolCalls-s.TotalToolErrors) / float64(s.TotalToolCalls)
	}
	if s.TotalLLMCalls > 0 {
		s.LLMSuccessRate = float64(s.TotalLLMCalls-s.TotalLLMErrors) / float64(s.TotalLLMCalls)
	}
	if s.TotalConversations > 0 {
		s.AvgToolCallsPerConv = float64(s.TotalToolCalls) / float64(s.TotalConversations)
	}

	// Cost efficiency
	if s.TotalConversations > 0 {
		s.AvgTokensPerConv = float64(s.TotalInputTokens+s.TotalOutputTokens) / float64(s.TotalConversations)
	}
	if s.TotalInputTokens > 0 {
		s.OutputInputRatio = float64(s.TotalOutputTokens) / float64(s.TotalInputTokens)
	}

	// Four-Layer Defense Effectiveness
	if s.TotalInputTokens > 0 {
		s.CombinedSavingRate = float64(s.CompressTokensIn-s.CompressTokensOut) / float64(s.TotalInputTokens)
	}

	return s
}

// FormatMarkdown formats MetricsSnapshot as Feishu markdown card text.
// 按能力维度分组为四段：Runtime Overview、Task Execution Effectiveness、Memory Quality、Compression efficiency、Four-Layer Defense Effectiveness。
func (s MetricsSnapshot) FormatMarkdown() string {
	var sb strings.Builder

	hasOverview := s.TotalConversations > 0 || s.TotalIterations > 0 || s.TotalToolCalls > 0
	hasMemory := s.MaskedItems > 0 || s.OffloadedItems > 0 || s.ContextEditEvents > 0 || s.CompressEvents > 0
	hasCompress := s.CompressEvents > 0
	hasDefense := s.MaskedItems > 0 || s.OffloadedItems > 0 || s.CompressTokensIn > 0

	// ── Runtime Overview ──
	sb.WriteString("⏱️ **Runtime Overview**\n──────────────\n")
	if hasOverview {
		fmt.Fprintf(&sb, "⏱️ Uptime: %s\n", formatDuration(s.UptimeSeconds))
		fmt.Fprintf(&sb, "💬 Conversations: %d 次", s.TotalConversations)
		if s.TotalConversations > 0 {
			avgIter := float64(s.TotalIterations) / float64(s.TotalConversations)
			fmt.Fprintf(&sb, " | 🔄 Avg iterations: %.1f 次/对话", avgIter)
		}
		sb.WriteString("\n")

		fmt.Fprintf(&sb, "🛠️ 工具调用：%d", s.TotalToolCalls)
		if s.TotalToolCalls > 0 {
			fmt.Fprintf(&sb, "（成功率 %.1f%%）", s.ToolSuccessRate*100)
		}
		fmt.Fprintf(&sb, " | 🤖 LLM：%d", s.TotalLLMCalls)
		if s.TotalLLMCalls > 0 {
			fmt.Fprintf(&sb, "（成功率 %.1f%%）", s.LLMSuccessRate*100)
		}
		sb.WriteString("\n")

		if s.TotalConversations > 0 {
			fmt.Fprintf(&sb, "💰 平均成本：%s tokens/对话", formatTokenCount(int64(s.AvgTokensPerConv)))
			if s.TotalInputTokens > 0 {
				fmt.Fprintf(&sb, " | Output/input ratio：%.1f%%", s.OutputInputRatio*100)
			}
			sb.WriteString("\n")
		}
	} else {
		sb.WriteString("No data available\n")
	}

	// ── Task Execution Effectiveness ──
	sb.WriteString("\n🎯 **Task Execution Effectiveness**\n──────────────\n")
	if s.TotalConversations > 0 {
		avgIter := float64(s.TotalIterations) / float64(s.TotalConversations)
		fmt.Fprintf(&sb, "📊 Avg iterations: %.1f 次/对话\n", avgIter)

		if s.TotalToolCalls > 0 {
			fmt.Fprintf(&sb, "🛠️ Tool success rate：%.1f%%（%d 调用 / %d 错误）\n",
				s.ToolSuccessRate*100, s.TotalToolCalls, s.TotalToolErrors)
		} else {
			sb.WriteString("🛠️ Tool success rate：N/A（无调用）\n")
		}

		if s.TotalLLMCalls > 0 {
			fmt.Fprintf(&sb, "🤖 LLM success rate：%.1f%%（%d 调用 / %d 错误）\n",
				s.LLMSuccessRate*100, s.TotalLLMCalls, s.TotalLLMErrors)
		} else {
			sb.WriteString("🤖 LLM success rate：N/A（无调用）\n")
		}

		fmt.Fprintf(&sb, "🔧 每对话工具：%.1f 次\n", s.AvgToolCallsPerConv)
	} else {
		sb.WriteString("No data available\n")
	}

	// ── Memory Quality ──
	sb.WriteString("\n🧠 **Memory Quality**\n──────────────\n")
	if hasMemory {
		if s.MaskedItems > 0 {
			fmt.Fprintf(&sb, "🎭 Masking 回调率：%.1f%%（%d / %d）\n",
				s.MaskedRecallRate*100, s.MaskedRecalls, s.MaskedItems)
		}
		if s.OffloadedItems > 0 {
			fmt.Fprintf(&sb, "💾 Offload 回调率：%.1f%%（%d / %d）\n",
				s.OffloadRecallRate*100, s.OffloadedRecalls, s.OffloadedItems)
		}
		totalEvictions := s.MaskedItems + s.OffloadedItems
		totalRecalls := s.OffloadedRecalls + s.MaskedRecalls
		if totalEvictions > 0 {
			fmt.Fprintf(&sb, "📉 总回调率：%.1f%%（%d / %d）\n",
				s.RecallRate*100, totalRecalls, totalEvictions)
		}
		if s.TotalConversations > 0 {
			fmt.Fprintf(&sb, "✂️ 上下文编辑率：%.1f%%（%d / %d 对话）\n",
				s.ContextEditRate*100, s.ContextEditEvents, s.TotalConversations)
		}
		if s.CompressEvents > 0 {
			fmt.Fprintf(&sb, "🔍 摘要精化率：%.1f%%（%d / %d 压缩）← 精化率高说明压缩摘要质量差\n",
				s.SummaryRefineRate*100, s.SummaryRefines, s.CompressEvents)
		}
	} else {
		sb.WriteString("No data available\n")
	}

	// ── Compression efficiency ──
	sb.WriteString("\n📦 **Compression efficiency**\n──────────────\n")
	if hasCompress {
		saved := s.CompressTokensIn - s.CompressTokensOut
		fmt.Fprintf(&sb, "🧹 压缩：%d 次 | 节省 %s tokens（节省率 %.1f%%）\n",
			s.CompressEvents, formatTokenCount(saved), s.TokenSavingRate*100)
		fmt.Fprintf(&sb, "📊 每次平均节省：%s tokens\n",
			formatTokenCount(int64(s.AvgTokensSavedPerCompress)))
		fmt.Fprintf(&sb, "📐 Output/input ratio：%.1f%%\n", s.CompressRatio*100)
	} else {
		sb.WriteString("No data available\n")
	}

	// ── Four-Layer Defense Effectiveness ──
	sb.WriteString("\n🛡️ **Four-Layer Defense Effectiveness**\n──────────────\n")
	if hasDefense {
		fmt.Fprintf(&sb, "🎭 Masking：遮蔽 %d 条（免费压缩）\n", s.MaskedItems)
		fmt.Fprintf(&sb, "💾 Offload：落盘 %d 条（磁盘保存）\n", s.OffloadedItems)
		if s.CompressTokensIn > 0 {
			saved := s.CompressTokensIn - s.CompressTokensOut
			fmt.Fprintf(&sb, "🧹 压缩：节省 %s tokens（节省率 %.1f%%）\n",
				formatTokenCount(saved), s.TokenSavingRate*100)
		} else {
			sb.WriteString("🧹 压缩：无数据\n")
		}
		fmt.Fprintf(&sb, "📈 联合保存率：%.1f%%（节省 %s / 总输入 %s）\n",
			s.CombinedSavingRate*100,
			formatTokenCount(s.CompressTokensIn-s.CompressTokensOut),
			formatTokenCount(s.TotalInputTokens))
	} else {
		sb.WriteString("No data available\n")
	}

	return sb.String()
}

// formatDuration formats seconds into human-readable format (e.g. "3h 25m").
func formatDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	if seconds < 3600 {
		return fmt.Sprintf("%dm %ds", seconds/60, seconds%60)
	}
	hours := seconds / 3600
	mins := (seconds % 3600) / 60
	return fmt.Sprintf("%dh %dm", hours, mins)
}
