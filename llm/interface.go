package llm

import "context"

// LLM is the interface using business-layer message and response types
type LLM interface {
	// Generate produces an LLM response
	// model: model name
	// messages: message list
	// tools: tool definition list
	// thinkingMode: thinking mode ("", "enabled", "disabled"), for DeepSeek/OpenAI reasoning models
	Generate(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (*LLMResponse, error)

	// ListModels returns the available model list
	ListModels() []string
}

// StreamingLLM is the streaming LLM interface
type StreamingLLM interface {
	LLM
	// GenerateStream produces a streaming response, returning an event channel
	// model: model name
	// messages: message list
	// tools: tool definition list
	// thinkingMode: 思考模式 ("", "enabled", "disabled")
	// The channel is closed on completion or error
	GenerateStream(ctx context.Context, model string, messages []ChatMessage, tools []ToolDefinition, thinkingMode string) (<-chan StreamEvent, error)
}

// ModelLoader is implemented by LLM clients that can refresh their model list from API.
type ModelLoader interface {
	LoadModelsFromAPI(ctx context.Context) error
}
