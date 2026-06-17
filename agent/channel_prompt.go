package agent

import (
	"context"
	"sync"
)

// ChannelPromptProvider 定义 channel 特化 prompt 提供者接口。
// 由外部（main.go 中的适配器）实现并注入，不依赖 channel 包。
type ChannelPromptProvider interface {
	// ChannelPromptName 返回 channel 名称，用于匹配 MessageContext.Channel
	ChannelPromptName() string

	// ChannelSystemParts 返回 channel 特化的 system prompt 片段。
	// 返回 nil 或空 map 表示该 channel 没有特化 prompt。
	// key 命名建议使用 "05_channel_xxx" 前缀，确保在 "00_base" 之后、
	// "10_skills" 之前。
	ChannelSystemParts(ctx context.Context, chatID, senderID string) map[string]string
}

// ChannelPromptMiddleware 注入 channel 特化的 system prompt 片段。
// 优先级 5（在 SystemPromptMiddleware 之后，SkillsCatalog 之前）。
// 并发安全：支持运行时通过 AddProvider 动态添加 provider。
type ChannelPromptMiddleware struct {
	mu        sync.RWMutex
	providers map[string]ChannelPromptProvider // key: channel name
}

func NewChannelPromptMiddleware(providers ...ChannelPromptProvider) *ChannelPromptMiddleware {
	m := &ChannelPromptMiddleware{providers: make(map[string]ChannelPromptProvider)}
	for _, p := range providers {
		m.providers[p.ChannelPromptName()] = p
	}
	return m
}

// AddProvider 动态添加一个 channel prompt provider（线程安全）。
// 如果同名 provider 已存在，则覆盖更新。
func (m *ChannelPromptMiddleware) AddProvider(p ChannelPromptProvider) {
	m.mu.Lock()
	m.providers[p.ChannelPromptName()] = p
	m.mu.Unlock()
}

func (m *ChannelPromptMiddleware) Name() string  { return "channel_prompt" }
func (m *ChannelPromptMiddleware) Priority() int { return 5 }

func (m *ChannelPromptMiddleware) Process(mc *MessageContext) error {
	if mc.Channel == "" {
		return nil
	}
	m.mu.RLock()
	provider, ok := m.providers[mc.Channel]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	parts := provider.ChannelSystemParts(mc.Ctx, mc.ChatID, mc.SenderID)
	for k, v := range parts {
		mc.SystemParts[k] = v
	}
	return nil
}
