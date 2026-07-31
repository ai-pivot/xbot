package agent

import (
	"context"
	"testing"
)

// mockChannelPromptProvider 是测试用的 ChannelPromptProvider mock
type mockChannelPromptProvider struct {
	name  string
	parts map[string]string
}

func (m *mockChannelPromptProvider) ChannelPromptName() string {
	return m.name
}

func (m *mockChannelPromptProvider) ChannelSystemParts(_ context.Context, _ string, _ string) map[string]string {
	return m.parts
}

func TestChannelPromptMiddleware_NoMatchingProvider(t *testing.T) {
	// 只有 feishu provider，但 channel 是 "cli"，应静默跳过
	provider := &mockChannelPromptProvider{
		name: "feishu",
		parts: map[string]string{
			"05_channel_feishu": "feishu rules",
		},
	}
	mw := NewChannelPromptMiddleware(provider)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "cli",
		ChatID:      "test_chat",
		SenderID:    "test_user",
	}

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if len(mc.SystemParts) != 0 {
		t.Errorf("expected no system parts, got %d", len(mc.SystemParts))
	}
}

func TestChannelPromptMiddleware_MatchingProvider(t *testing.T) {
	provider := &mockChannelPromptProvider{
		name: "feishu",
		parts: map[string]string{
			"05_channel_feishu": "feishu specific rules",
		},
	}
	mw := NewChannelPromptMiddleware(provider)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "feishu",
		ChatID:      "oc_test",
		SenderID:    "ou_test",
	}

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if got, ok := mc.SystemParts["05_channel_feishu"]; !ok {
		t.Error("expected 05_channel_feishu key in SystemParts")
	} else if got != "feishu specific rules" {
		t.Errorf("expected 'feishu specific rules', got %q", got)
	}
}

func TestChannelPromptMiddleware_EmptyChannel(t *testing.T) {
	provider := &mockChannelPromptProvider{
		name: "feishu",
		parts: map[string]string{
			"05_channel_feishu": "feishu rules",
		},
	}
	mw := NewChannelPromptMiddleware(provider)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "", // 空 channel
		ChatID:      "test_chat",
		SenderID:    "test_user",
	}

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if len(mc.SystemParts) != 0 {
		t.Errorf("expected no system parts for empty channel, got %d", len(mc.SystemParts))
	}
}

func TestChannelPromptMiddleware_Priority(t *testing.T) {
	mw := NewChannelPromptMiddleware()
	if mw.Priority() != 5 {
		t.Errorf("expected priority 5, got %d", mw.Priority())
	}
}

func TestChannelPromptMiddleware_MultipleProviders(t *testing.T) {
	feishuProvider := &mockChannelPromptProvider{
		name: "feishu",
		parts: map[string]string{
			"05_channel_feishu": "feishu rules",
		},
	}
	qqProvider := &mockChannelPromptProvider{
		name: "qq",
		parts: map[string]string{
			"05_channel_qq": "qq rules",
		},
	}
	mw := NewChannelPromptMiddleware(feishuProvider, qqProvider)

	// 测试 feishu
	mcFeishu := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "feishu",
		ChatID:      "oc_test",
		SenderID:    "ou_test",
	}
	if err := mw.Process(mcFeishu); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if got, ok := mcFeishu.SystemParts["05_channel_feishu"]; !ok || got != "feishu rules" {
		t.Errorf("expected feishu rules, got %q (ok=%v)", got, ok)
	}
	if _, ok := mcFeishu.SystemParts["05_channel_qq"]; ok {
		t.Error("qq rules should not be injected for feishu channel")
	}

	// 测试 qq
	mcQQ := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "qq",
		ChatID:      "qq_group",
		SenderID:    "qq_user",
	}
	if err := mw.Process(mcQQ); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if got, ok := mcQQ.SystemParts["05_channel_qq"]; !ok || got != "qq rules" {
		t.Errorf("expected qq rules, got %q (ok=%v)", got, ok)
	}
}

func TestChannelPromptMiddleware_NilPartsFromProvider(t *testing.T) {
	provider := &mockChannelPromptProvider{
		name:  "test",
		parts: nil, // 返回 nil
	}
	mw := NewChannelPromptMiddleware(provider)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "test",
		ChatID:      "chat1",
		SenderID:    "user1",
	}

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	// nil parts 不应 panic，SystemParts 应为空
	if len(mc.SystemParts) != 0 {
		t.Errorf("expected no system parts when provider returns nil, got %d", len(mc.SystemParts))
	}
}

func TestChannelPromptMiddleware_ExistingPartsPreserved(t *testing.T) {
	provider := &mockChannelPromptProvider{
		name: "feishu",
		parts: map[string]string{
			"05_channel_feishu": "feishu rules",
		},
	}
	mw := NewChannelPromptMiddleware(provider)

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: map[string]string{"00_base": "base prompt"},
		Channel:     "feishu",
		ChatID:      "oc_test",
		SenderID:    "ou_test",
	}

	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	// 原有 parts 应保留
	if got, ok := mc.SystemParts["00_base"]; !ok || got != "base prompt" {
		t.Errorf("existing 00_base should be preserved, got %q (ok=%v)", got, ok)
	}
	if got, ok := mc.SystemParts["05_channel_feishu"]; !ok || got != "feishu rules" {
		t.Errorf("expected 05_channel_feishu to be injected, got %q (ok=%v)", got, ok)
	}
}

// TestChannelPromptMiddleware_AddProvider 测试动态添加 provider。
func TestChannelPromptMiddleware_AddProvider(t *testing.T) {
	mw := NewChannelPromptMiddleware()

	// 初始为空
	mc1 := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "telegram",
	}
	if err := mw.Process(mc1); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if len(mc1.SystemParts) != 0 {
		t.Errorf("expected no parts initially, got %d", len(mc1.SystemParts))
	}

	// 动态添加 provider
	mw.AddProvider(&mockChannelPromptProvider{
		name: "telegram",
		parts: map[string]string{
			"05_channel_telegram": "telegram rules",
		},
	})

	// 现在应该能匹配
	mc2 := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "telegram",
	}
	if err := mw.Process(mc2); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if got, ok := mc2.SystemParts["05_channel_telegram"]; !ok || got != "telegram rules" {
		t.Errorf("expected 'telegram rules', got %q (ok=%v)", got, ok)
	}
}

// TestChannelPromptMiddleware_AddProviderOverwrite 测试 AddProvider 覆盖同名 provider。
func TestChannelPromptMiddleware_AddProviderOverwrite(t *testing.T) {
	mw := NewChannelPromptMiddleware()

	mw.AddProvider(&mockChannelPromptProvider{
		name:  "github",
		parts: map[string]string{"05_channel_github": "v1"},
	})
	mw.AddProvider(&mockChannelPromptProvider{
		name:  "github",
		parts: map[string]string{"05_channel_github": "v2"},
	})

	mc := &MessageContext{
		Ctx:         context.Background(),
		SystemParts: make(map[string]string),
		Channel:     "github",
	}
	if err := mw.Process(mc); err != nil {
		t.Fatalf("Process() returned error: %v", err)
	}
	if got := mc.SystemParts["05_channel_github"]; got != "v2" {
		t.Errorf("expected 'v2' after overwrite, got %q", got)
	}
}
