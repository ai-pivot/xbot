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

	result, err := compactMessages(ctx, messages, client, model, m.config.MaxContextTokens, promptTokens)
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
	return compactMessages(ctx, messages, client, m.compressionModel(model), m.config.MaxContextTokens, promptTokens)
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
