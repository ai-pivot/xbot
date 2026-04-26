package llm

import (
	"context"
	"strings"
	"time"
)

// MockLLM is a mock LLM implementation for testing
type MockLLM struct {
	ChunkSize     int           // Characters per streaming chunk, default 5
	ChunkInterval time.Duration // Interval between streaming chunks, default 50ms
	GenerateFn    func(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error)
}

// NewMockLLM creates a new MockLLM
func NewMockLLM() *MockLLM {
	return &MockLLM{
		ChunkSize:     5,
		ChunkInterval: 50 * time.Millisecond,
	}
}

// Generate (non-streaming): concatenates all message content as response; token cost equals content length
func (m *MockLLM) Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error) {
	if m.GenerateFn != nil {
		return m.GenerateFn(ctx, model, messages, tools, thinkingMode)
	}

	var sb strings.Builder
	for _, msg := range messages {
		if msg.Content != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[" + msg.Role + "] " + msg.Content)
		}
	}

	content := sb.String()
	contentLen := int64(len([]rune(content)))

	return &LLMResponse{
		Content:      content,
		FinishReason: FinishReasonStop,
		Usage: TokenUsage{
			PromptTokens:     contentLen,
			CompletionTokens: contentLen,
			TotalTokens:      contentLen * 2,
		},
	}, nil
}

// ListModels returns the mock model list
func (m *MockLLM) ListModels() []string {
	return []string{"mock"}
}

// GenerateStream (streaming): sends all message content in chunks of ChunkSize at ChunkInterval
func (m *MockLLM) GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error) {
	var sb strings.Builder
	for _, msg := range messages {
		if msg.Content != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString("[" + msg.Role + "] " + msg.Content)
		}
	}

	content := sb.String()
	runes := []rune(content)
	contentLen := int64(len(runes))

	chunkSize := m.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 5
	}
	interval := m.ChunkInterval
	if interval <= 0 {
		interval = 50 * time.Millisecond
	}

	ch := make(chan StreamEvent, 10)

	go func() {
		defer close(ch)

		for i := 0; i < len(runes); i += chunkSize {
			select {
			case <-ctx.Done():
				ch <- StreamEvent{Type: EventError, Error: ctx.Err().Error()}
				return
			default:
			}

			end := i + chunkSize
			if end > len(runes) {
				end = len(runes)
			}
			chunk := string(runes[i:end])

			ch <- StreamEvent{Type: EventContent, Content: chunk}

			if end < len(runes) {
				time.Sleep(interval)
			}
		}

		ch <- StreamEvent{
			Type: EventUsage,
			Usage: &TokenUsage{
				PromptTokens:     contentLen,
				CompletionTokens: contentLen,
				TotalTokens:      contentLen * 2,
			},
		}

		ch <- StreamEvent{
			Type:         EventDone,
			FinishReason: FinishReasonStop,
		}
	}()

	return ch, nil
}
