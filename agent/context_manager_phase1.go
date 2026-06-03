package agent

import (
	"context"

	"xbot/llm"
	log "xbot/logger"
)

// phase1Manager implements ContextManager using single-pass structured compaction.
type phase1Manager struct {
	config      *ContextManagerConfig
	memTools    []llm.ToolDefinition
	memToolExec func(ctx context.Context, tc llm.ToolCall) (content string, err error)
}

// SetMemoryTools injects memory tool definitions and executor for use during compaction.
func (m *phase1Manager) SetMemoryTools(tools []llm.ToolDefinition, exec func(ctx context.Context, tc llm.ToolCall) (string, error)) {
	m.memTools = tools
	m.memToolExec = exec
}

func newPhase1Manager(cfg *ContextManagerConfig) *phase1Manager {
	return &phase1Manager{
		config: cfg,
	}
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

// Compress executes structured compaction via a single LLM call.
func (m *phase1Manager) Compress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	originalTokens := len(messages) * 200 // rough estimate

	log.Ctx(ctx).WithFields(map[string]any{
		"original_tokens": originalTokens,
		"max_tokens":      m.config.MaxContextTokens,
	}).Info("Context compaction: starting")

	result, err := compactMessages(ctx, messages, client, model, m.config.MaxContextTokens, m.memTools, m.memToolExec)
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

// ManualCompress handles /compress command.
func (m *phase1Manager) ManualCompress(ctx context.Context, messages []llm.ChatMessage, client llm.LLM, model string) (*CompressResult, error) {
	return compactMessages(ctx, messages, client, model, m.config.MaxContextTokens, m.memTools, m.memToolExec)
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
