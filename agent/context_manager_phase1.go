package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
)

// phase1Manager implements ContextManager using single-pass structured compaction.
type phase1Manager struct {
	config *ContextManagerConfig
}

func newPhase1Manager(cfg *ContextManagerConfig) *phase1Manager {
	return &phase1Manager{
		config: cfg,
	}
}

// compressionModel returns the model name for a compaction LLM call:
// config.CompressionModel overrides the session's model when set (compaction
// on a faster/cheaper model of the same endpoint — the summary needs speed,
// not the session flagship's intelligence). Empty config → the session's model.
func (m *phase1Manager) compressionModel(sessionModel string) string {
	if m.config.CompressionModel != "" {
		// ⚠️ CR xbotgh: prefix/radix cache is keyed by MODEL — an override to a
		// DIFFERENT model silently forfeits this PR's core optimization: the
		// verbatim-history request ([original system, ...verbatim, instruction])
		// re-prefills the ENTIRE history on the override model (exactly the ~900k
		// re-prefill commit e7de1108 eliminated). Warn ONCE per config change so
		// the trade-off is visible; to keep the cache-hit the override must name
		// the SAME model as the session.
		if m.config.CompressionModel != sessionModel {
			log.WithFields(log.Fields{
				"compression_model": m.config.CompressionModel,
				"session_model":     sessionModel,
			}).Warn("CompressionModel override forfeits the verbatim-history radix cache-hit: prefix cache is keyed by model, the full history re-prefills on the override model. Keep the same model name as the session to preserve the cache-hit.")
		}
		return m.config.CompressionModel
	}
	return sessionModel
}

func (m *phase1Manager) Mode() ContextMode { return ContextModePhase1 }

func (m *phase1Manager) ShouldCompress(messages []llm.ChatMessage, model string, toolTokens int) bool {
	if len(messages) <= 3 {
		return false
	}
	// Use total character count / 3 as rough token estimate.
	// This avoids tiktoken dependency; exact values come from API prompt_tokens.
	totalChars := 0
	for _, msg := range messages {
		totalChars += len([]rune(msg.Content))
	}
	msgTokens := totalChars / 3
	return shouldCompact(msgTokens+toolTokens, m.config.MaxContextTokens, m.config.CompressionThreshold)
}

// Compress executes structured compaction via the agent loop (engine.Run).
// promptTokens is the REAL API prompt_tokens (usage) — the verbatim
// cache-hit path's budget check uses it (Never-Estimate-Tokens rule); 0
// (unknown, e.g. error paths without usage) forces the flatten fallback.
func (m *phase1Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string, promptTokens int64) (*CompressResult, error) {
	// CompressionModel override: compaction runs on a faster/cheaper model of
	// the same endpoint when configured (config agent.compression_model). The
	// summary needs speed, not the session flagship's intelligence — Claude
	// Code's ecosystem practice is a cheap summarizer model.
	model = m.compressionModel(model)
	originalTokens := len(messages) * 200 // rough estimate

	log.Ctx(ctx).WithFields(map[string]any{
		"original_tokens": originalTokens,
		"max_tokens":      m.config.MaxContextTokens,
	}).Info("Context compaction: starting")

	result, err := compactMessages(ctx, messages, client, model, m.config.MaxContextTokens, promptTokens, m.config.MaxOutputTokens)
	if err != nil {
		return nil, err
	}

	newTokens := len(result.LLMView) * 200 // rough estimate
	reductionRate := 0.0
	if originalTokens > 0 {
		reductionRate = 1.0 - float64(newTokens)/float64(originalTokens)
	}

	if reductionRate < 0.10 {
		log.Ctx(ctx).WithFields(map[string]any{
			"reduction_rate":  reductionRate,
			"new_tokens":      newTokens,
			"original_tokens": originalTokens,
		}).Warn("Context compaction: low reduction rate")
	}

	log.Ctx(ctx).WithFields(map[string]any{
		"reduction_rate": reductionRate,
		"new_tokens":     newTokens,
	}).Info("Context compaction completed")

	return result, nil
}

// ManualCompress handles /compress command. promptTokens is the REAL API
// prompt_tokens (usage) — same Never-Estimate-Tokens contract as Compress.
func (m *phase1Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string, promptTokens int64) (*CompressResult, error) {
	return compactMessages(ctx, messages, client, m.compressionModel(model), m.config.MaxContextTokens, promptTokens, m.config.MaxOutputTokens)
}

func (m *phase1Manager) ContextInfo(messages []llm.ChatMessage, model string, toolTokens int) *ContextStats {
	// Use message count as rough estimate — exact token counts come from API.
	msgTokens := len(messages) * 200
	totalTokens := msgTokens + toolTokens
	threshold := int(float64(m.config.MaxContextTokens) * m.config.CompressionThreshold)

	return &ContextStats{
		SystemTokens:      msgTokens / 4,
		UserTokens:        msgTokens / 4,
		AssistantTokens:   msgTokens / 4,
		ToolMsgTokens:     msgTokens / 4,
		ToolDefTokens:     toolTokens,
		TotalTokens:       totalTokens,
		MaxTokens:         m.config.MaxContextTokens,
		Threshold:         threshold,
		Mode:              ContextModePhase1,
		IsRuntimeOverride: m.config.RuntimeMode() != "",
		DefaultMode:       m.config.DefaultMode,
	}
}

func (m *phase1Manager) SessionHook() SessionCompressHook { return nil }
