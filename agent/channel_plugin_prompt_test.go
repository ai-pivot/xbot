package agent

import (
	"context"
	"sync"
	"testing"
)

func TestChannelPluginPromptProvider_Empty(t *testing.T) {
	p := newChannelPluginPromptProvider("telegram")

	// Empty provider should return nil, not empty map
	parts := p.ChannelSystemParts(context.Background(), "chat1", "user1")
	if parts != nil {
		t.Errorf("expected nil for empty provider, got %v", parts)
	}
}

func TestChannelPluginPromptProvider_SetAndGet(t *testing.T) {
	p := newChannelPluginPromptProvider("telegram")

	p.setSystemParts(map[string]string{
		"05_channel_telegram": "telegram specific rules",
	})

	parts := p.ChannelSystemParts(context.Background(), "chat1", "user1")
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}
	if got := parts["05_channel_telegram"]; got != "telegram specific rules" {
		t.Errorf("expected 'telegram specific rules', got %q", got)
	}
}

func TestChannelPluginPromptProvider_HotUpdate(t *testing.T) {
	p := newChannelPluginPromptProvider("github")

	// First declaration
	p.setSystemParts(map[string]string{
		"05_channel_github": "v1 rules",
	})

	// Second declaration should replace
	p.setSystemParts(map[string]string{
		"05_channel_github": "v2 rules",
	})

	parts := p.ChannelSystemParts(context.Background(), "chat1", "user1")
	if got := parts["05_channel_github"]; got != "v2 rules" {
		t.Errorf("expected 'v2 rules' after hot-update, got %q", got)
	}
}

func TestChannelPluginPromptProvider_Name(t *testing.T) {
	p := newChannelPluginPromptProvider("telegram")
	if got := p.ChannelPromptName(); got != "telegram" {
		t.Errorf("expected name 'telegram', got %q", got)
	}
}

func TestChannelPluginPromptProvider_ConcurrentAccess(t *testing.T) {
	p := newChannelPluginPromptProvider("telegram")

	var wg sync.WaitGroup
	// Concurrent writes
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p.setSystemParts(map[string]string{
				"05_channel": "rules",
			})
		}(i)
	}
	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.ChannelSystemParts(context.Background(), "chat1", "user1")
		}()
	}
	wg.Wait()
	// No race detector errors expected
}

func TestChannelPluginPromptProvider_Interface(t *testing.T) {
	// Verify it implements the interface
	var _ ChannelPromptProvider = (*channelPluginPromptProvider)(nil)

	p := newChannelPluginPromptProvider("test")
	p.setSystemParts(map[string]string{"05_test": "content"})

	var provider ChannelPromptProvider = p
	if name := provider.ChannelPromptName(); name != "test" {
		t.Errorf("expected 'test', got %q", name)
	}
}
