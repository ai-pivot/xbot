package llm

import "testing"

// 实测依据（mint 网关 / gemini-4-argon，2026-11-17）：
//
//	usageMetadata = {"promptTokenCount":3400,"candidatesTokenCount":5,
//	                 "totalTokenCount":3405,"cachedContentTokenCount":1346,
//	                 "thoughtsTokenCount":784}
//
// 即 prompt 含 cached、candidates 不含 thoughts、total = prompt+candidates+thoughts。
func TestGeminiUsageToTokenUsage(t *testing.T) {
	tests := []struct {
		name string
		in   geminiUsage
		want TokenUsage
	}{
		{
			name: "cache hit and thoughts",
			in: geminiUsage{
				PromptTokenCount:        3400,
				CandidatesTokenCount:    5,
				TotalTokenCount:         3405,
				CachedContentTokenCount: 1346,
				ThoughtsTokenCount:      784,
			},
			want: TokenUsage{
				PromptTokens:     3400,
				CompletionTokens: 789, // 5 candidates + 784 thoughts
				TotalTokens:      3405,
				CacheHitTokens:   1346,
			},
		},
		{
			name: "no cache, no thinking",
			in:   geminiUsage{PromptTokenCount: 60, CandidatesTokenCount: 431, TotalTokenCount: 491},
			want: TokenUsage{PromptTokens: 60, CompletionTokens: 431, TotalTokens: 491},
		},
		{
			name: "total missing falls back to prompt+completion",
			in:   geminiUsage{PromptTokenCount: 10, CandidatesTokenCount: 2, ThoughtsTokenCount: 3},
			want: TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.toTokenUsage(); got != tc.want {
				t.Fatalf("toTokenUsage() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// gemini-3/4 只接受 thinkingLevel（传 thinkingBudget 会 400），gemini-2 只接受
// thinkingBudget。默认（空串）必须显式请求思考，否则上游不思考——这正是
// "gemini 好像不会思考"的根因。
func TestGeminiThinkingFor(t *testing.T) {
	tests := []struct {
		model, mode  string
		wantNil      bool
		wantLevel    string
		wantBudget   int
		wantThoughts bool
	}{
		{model: "gemini-4-argon", mode: "", wantLevel: "HIGH", wantThoughts: true},
		{model: "gemini-4-argon", mode: "auto", wantLevel: "HIGH", wantThoughts: true},
		{model: "gemini-4-argon", mode: "enabled", wantLevel: "HIGH", wantThoughts: true},
		{model: "gemini-4-argon", mode: "think", wantLevel: "HIGH", wantThoughts: true},
		{model: "gemini-4-argon", mode: "high", wantLevel: "HIGH", wantThoughts: true},
		{model: "gemini-4-argon", mode: "medium", wantLevel: "MEDIUM", wantThoughts: true},
		{model: "gemini-4-argon", mode: "low", wantLevel: "LOW", wantThoughts: true},
		{model: "gemini-4-argon", mode: "disabled", wantNil: true}, // 3/4 代无 0 档
		{model: "gemini-4-argon", mode: "off", wantNil: true},
		{model: "gemini-2.5-pro", mode: "", wantBudget: 8192, wantThoughts: true},
		{model: "gemini-2.5-pro", mode: "medium", wantBudget: 4096, wantThoughts: true},
		{model: "gemini-2.5-flash", mode: "low", wantBudget: 1024, wantThoughts: true},
		{model: "gemini-2.5-flash", mode: "disabled", wantBudget: 0},
	}
	for _, tc := range tests {
		t.Run(tc.model+"/"+tc.mode, func(t *testing.T) {
			got := geminiThinkingFor(tc.model, tc.mode)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("want nil (no thinkingConfig), got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("want config, got nil")
			}
			if tc.wantLevel != "" && got.ThinkingLevel != tc.wantLevel {
				t.Fatalf("ThinkingLevel = %q, want %q", got.ThinkingLevel, tc.wantLevel)
			}
			if got.ThinkingBudget != nil && *got.ThinkingBudget != tc.wantBudget {
				t.Fatalf("ThinkingBudget = %d, want %d", *got.ThinkingBudget, tc.wantBudget)
			}
			if got.ThinkingLevel != "" && got.ThinkingBudget != nil {
				t.Fatalf("must not send both thinkingLevel and thinkingBudget: %+v", got)
			}
			// 没有 includeThoughts，上游只回 thoughtSignature，UI 就看不到思考过程。
			if got.IncludeThoughts != tc.wantThoughts {
				t.Fatalf("IncludeThoughts = %v, want %v", got.IncludeThoughts, tc.wantThoughts)
			}
		})
	}
}
