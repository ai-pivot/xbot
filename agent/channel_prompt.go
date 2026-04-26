package agent

import "context"

// ChannelPromptProvider defines the channel-specific prompt provider interface.
// Implemented and injected externally (adapter in main.go), doesn't depend on channel package.
type ChannelPromptProvider interface {
	// ChannelPromptName returns channel name, for matching MessageContext.Channel
	ChannelPromptName() string

	// ChannelSystemParts returns channel-specific system prompt fragments.
	// Returns nil or empty map means this channel has no specialized prompt.
	// Key naming convention: use "05_channel_xxx" prefix, ensuring it comes after "00_base" and
	// before "10_skills".
	ChannelSystemParts(ctx context.Context, chatID, senderID string) map[string]string
}

// ChannelPromptMiddleware injects channel-specific system prompt fragments.
// Priority 5 (after SystemPromptMiddleware, before SkillsCatalog).
type ChannelPromptMiddleware struct {
	providers map[string]ChannelPromptProvider // key: channel name
}

// NewChannelPromptMiddleware creates a middleware that injects channel-specific prompts.
func NewChannelPromptMiddleware(providers ...ChannelPromptProvider) *ChannelPromptMiddleware {
	m := &ChannelPromptMiddleware{providers: make(map[string]ChannelPromptProvider)}
	for _, p := range providers {
		m.providers[p.ChannelPromptName()] = p
	}
	return m
}

func (m *ChannelPromptMiddleware) Name() string  { return "channel_prompt" }
func (m *ChannelPromptMiddleware) Priority() int { return 5 }

func (m *ChannelPromptMiddleware) Process(mc *MessageContext) error {
	if mc.Channel == "" {
		return nil
	}
	provider, ok := m.providers[mc.Channel]
	if !ok {
		return nil
	}
	parts := provider.ChannelSystemParts(mc.Ctx, mc.ChatID, mc.SenderID)
	for k, v := range parts {
		mc.SystemParts[k] = v
	}
	return nil
}
