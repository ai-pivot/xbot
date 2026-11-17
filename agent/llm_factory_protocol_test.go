package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/storage/common"
)

// 用户要求（2026-11-17）：模型级 api_type 必须能选 anthropic / google（不止 OpenAI
// 的 chat_completions / responses），并且协议选择要看 api_type 而不是 provider。
//
// 覆盖矩阵：api_type 非空时以它为准；为空时回落 provider；未知值走 OpenAI 兼容层。
func TestCreateClient_ProtocolSelection(t *testing.T) {
	f := &LLMFactory{}
	cases := []struct {
		name     string
		apiType  string
		provider string
		want     func(llm.LLM) bool
		wantType string
	}{
		{
			name:    "explicit google wins over openai provider",
			apiType: "google", provider: "openai",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.GoogleLLM); return ok },
			wantType: "GoogleLLM",
		},
		{
			name:    "gemini alias also routes to google client",
			apiType: "gemini", provider: "openai",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.GoogleLLM); return ok },
			wantType: "GoogleLLM",
		},
		{
			name:    "explicit anthropic wins over openai provider",
			apiType: "anthropic", provider: "openai",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.AnthropicLLM); return ok },
			wantType: "AnthropicLLM",
		},
		{
			name:    "explicit chat_completions stays openai client",
			apiType: "chat_completions", provider: "openai",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.OpenAILLM); return ok },
			wantType: "OpenAILLM",
		},
		{
			name:    "empty api_type falls back to anthropic provider",
			apiType: "", provider: "anthropic",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.AnthropicLLM); return ok },
			wantType: "AnthropicLLM",
		},
		{
			name:    "empty api_type falls back to openai provider",
			apiType: "", provider: "openai",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.OpenAILLM); return ok },
			wantType: "OpenAILLM",
		},
		{
			name:    "jev routes to the explicit placeholder (never silently openai)",
			apiType: "jev", provider: "openai",
			want:     func(c llm.LLM) bool { _, ok := c.(*llm.JEVLLM); return ok },
			wantType: "JEVLLM",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &common.UserLLMConfig{
				Provider: tc.provider, BaseURL: "https://example.invalid/v1", APIKey: "k",
				Model: "m", MaxOutputTokens: 128, APIType: tc.apiType,
			}
			client, model := f.createClient(cfg, nil)
			if client == nil || model != "m" {
				t.Fatalf("createClient returned (%v, %q)", client, model)
			}
			if !tc.want(client) {
				t.Fatalf("api_type=%q provider=%q → %T, want %s", tc.apiType, tc.provider, client, tc.wantType)
			}
		})
	}
}

// JEV 是占位协议：必须显式报"未实现"，不能回落到 OpenAI 兼容层发出错误请求。
func TestJEVPlaceholderFailsLoudly(t *testing.T) {
	c := llm.NewJEVLLM("jev-x")
	_, err := c.Generate(context.Background(), "jev-x", nil, nil, "")
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("JEV must fail loudly, got %v", err)
	}
}
