package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestParseMentions(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		{
			input:    "@agent:reviewer/r1 what do you think?",
			expected: []string{"agent:reviewer/r1"},
		},
		{
			input:    "@agent:reviewer/r1 @agent:tester/t1 please review",
			expected: []string{"agent:reviewer/r1", "agent:tester/t1"},
		},
		{
			input:    "No mentions here",
			expected: nil,
		},
		{
			input:    "@agent:reviewer/r1 @agent:reviewer/r1 duplicate",
			expected: []string{"agent:reviewer/r1"}, // dedup
		},
		{
			input:    "@agent:a/b-c@d @agent:x/y more text",
			expected: []string{"agent:a/b-c@d", "agent:x/y"},
		},
		{
			input:    "text @agent:reviewer/r1\nnext line @agent:tester/t2 end",
			expected: []string{"agent:reviewer/r1", "agent:tester/t2"},
		},
	}

	for _, tt := range tests {
		result := parseMentions(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseMentions(%q): expected %v, got %v", tt.input, tt.expected, result)
			continue
		}
		for i, addr := range result {
			if addr != tt.expected[i] {
				t.Errorf("parseMentions(%q)[%d]: expected %q, got %q", tt.input, i, tt.expected[i], addr)
			}
		}
	}
}

func TestParseMentionsBoundaryCases(t *testing.T) {
	tests := []struct {
		input    string
		expected []string
	}{
		// Bare "agent:" without slash — should be rejected
		{"@agent: what", nil},
		// "agent:role" without instance slash — should be rejected
		{"@agent:reviewer what", nil},
		// Trailing colon
		{"@agent:", nil},
		// At end of string, valid format
		{"text @agent:role/r1", []string{"agent:role/r1"}},
		// Multiple valid + invalid mixed
		{"@agent:reviewer/r1 @agent:noslash @agent:tester/t2", []string{"agent:reviewer/r1", "agent:tester/t2"}},
	}

	for _, tt := range tests {
		result := parseMentions(tt.input)
		if len(result) != len(tt.expected) {
			t.Errorf("parseMentions(%q): expected %v, got %v", tt.input, tt.expected, result)
			continue
		}
		for i, addr := range result {
			if addr != tt.expected[i] {
				t.Errorf("parseMentions(%q)[%d]: expected %q, got %q", tt.input, i, tt.expected[i], addr)
			}
		}
	}
}

// blockingSender 模拟"永不回复"的目标 agent（SendMessageCtx 一直阻塞到 ctx 结束）。
type blockingSender struct{}

func (blockingSender) SendMessage(channel, chatID, content string) (string, error) {
	select {}
}

func (blockingSender) SendMessageCtx(ctx context.Context, channel, chatID, content string) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

// TestSendToAgent_DoesNotBlockOnUnresponsiveTarget — 用户 2026-09-14：
// 「sendmessage 工具有可能卡死，必须立刻成功」。旧实现等满 AgentRPCTimeout=30s。
func TestSendToAgent_DoesNotBlockOnUnresponsiveTarget(t *testing.T) {
	tool := &SendMessageTool{}
	ctx := &ToolContext{MessageSender: blockingSender{}}
	start := time.Now()
	res, err := tool.sendToAgent(ctx, "agent:reviewer/r1", "hi")
	if err != nil {
		t.Fatalf("must succeed immediately, got error: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("send_message blocked for %v; must return immediately", d)
	}
	if !strings.Contains(res.Summary+res.Detail, "delivered") {
		t.Fatalf("result should report delivery, got %q", res.Detail)
	}
}
