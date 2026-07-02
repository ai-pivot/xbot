package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// formatErrorForUser
// ---------------------------------------------------------------------------

func TestFormatErrorForUser(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "nil error returns empty",
			err:  nil,
			want: "",
		},
		{
			name: "wrapped ErrLLMGenerate returns LLM-specific message",
			err:  fmt.Errorf("wrapped: %w", ErrLLMGenerate),
			want: fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查配置。\n错误详情: wrapped: %v", ErrLLMGenerate),
		},
		{
			name: "bare ErrLLMGenerate",
			err:  ErrLLMGenerate,
			want: fmt.Sprintf("LLM 服务调用失败，请稍后重试或检查配置。\n错误详情: %v", ErrLLMGenerate),
		},
		{
			name: "generic error returns generic message",
			err:  errors.New("something broke"),
			want: "处理消息时发生错误: something broke",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := formatErrorForUser(tc.err)
			if got != tc.want {
				t.Errorf("formatErrorForUser() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// sessionKey
// ---------------------------------------------------------------------------

func TestQualifyChatID(t *testing.T) {
	tests := []struct {
		name    string
		channel string
		chatID  string
		want    string
	}{
		{name: "normal case", channel: "telegram", chatID: "12345", want: "telegram:12345"},
		{name: "empty channel", channel: "", chatID: "12345", want: ":12345"},
		{name: "empty chatID", channel: "telegram", chatID: "", want: "telegram:"},
		{name: "both empty", channel: "", chatID: "", want: ":"},
		{name: "channel contains colon", channel: "some:channel", chatID: "abc", want: "some:channel:abc"},
		{name: "chatID contains colon", channel: "tg", chatID: "group:thread", want: "tg:group:thread"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := qualifyChatID(tc.channel, tc.chatID)
			if got != tc.want {
				t.Errorf("qualifyChatID(%q, %q) = %q, want %q", tc.channel, tc.chatID, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolveMemoryProvider
// ---------------------------------------------------------------------------

func TestResolveMemoryProvider(t *testing.T) {
	tests := []struct {
		name string
		cfg  string
		want string
	}{
		{name: "empty defaults to flat", cfg: "", want: "flat"},
		{name: "flat stays flat", cfg: "flat", want: "flat"},
		{name: "letta stays letta", cfg: "letta", want: "letta"},
		{name: "arbitrary value passed through", cfg: "custom", want: "custom"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveMemoryProvider(tc.cfg)
			if got != tc.want {
				t.Errorf("resolveMemoryProvider(%q) = %q, want %q", tc.cfg, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// resolveGlobalSkillsDirs
// ---------------------------------------------------------------------------

func TestResolveGlobalSkillsDirs(t *testing.T) {
	// Detect if ~/.agents/skills exists on this machine (auto-detection target)
	agentsSkillsExists := false
	var agentsSkillsAbs string
	if home, err := os.UserHomeDir(); err == nil {
		dir := filepath.Join(home, ".agents", "skills")
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			agentsSkillsExists = true
			agentsSkillsAbs, _ = filepath.Abs(dir)
		}
	}

	tests := []struct {
		name      string
		skillsDir string
		wantLen   int
	}{
		{
			name:      "empty returns at least auto-detected dirs",
			skillsDir: "",
			wantLen:   map[bool]int{true: 1, false: 0}[agentsSkillsExists],
		},
		{
			name:      "non-empty includes configured dir plus auto-detected",
			skillsDir: filepath.Join("path", "to", "skills"),
			wantLen:   map[bool]int{true: 2, false: 1}[agentsSkillsExists],
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveGlobalSkillsDirs(tc.skillsDir)
			if len(got) != tc.wantLen {
				t.Fatalf("length mismatch: got %d, want %d (got=%v)", len(got), tc.wantLen, got)
			}

			// If a skillsDir was provided, it should be first
			if tc.skillsDir != "" && tc.wantLen > 0 {
				wantAbs, _ := filepath.Abs(tc.skillsDir)
				if got[0] != wantAbs {
					t.Errorf("got[0] = %q, want %q", got[0], wantAbs)
				}
			}

			// If ~/.agents/skills exists, it should be in the result
			if agentsSkillsExists {
				found := false
				for _, d := range got {
					if d == agentsSkillsAbs {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected ~/.agents/skills (%s) to be in result, got %v", agentsSkillsAbs, got)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// copyMessages
// ---------------------------------------------------------------------------

func TestCopyMessages(t *testing.T) {
	t.Run("returns different slice with same content", func(t *testing.T) {
		original := []llm.ChatMessage{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		}
		cpy := copyMessages(original)

		// Different backing array
		if &cpy[0] == &original[0] {
			t.Error("copyMessages returned same backing array")
		}
		// Same length and content
		if len(cpy) != len(original) {
			t.Fatalf("len = %d, want %d", len(cpy), len(original))
		}
		for i := range cpy {
			if !reflect.DeepEqual(cpy[i], original[i]) {
				t.Errorf("cpy[%d] = %v, want %v", i, cpy[i], original[i])
			}
		}
	})

	t.Run("empty slice", func(t *testing.T) {
		original := []llm.ChatMessage{}
		cpy := copyMessages(original)
		if len(cpy) != 0 {
			t.Errorf("expected empty slice, got %d elements", len(cpy))
		}
	})

	t.Run("nil input", func(t *testing.T) {
		cpy := copyMessages(nil)
		if len(cpy) != 0 {
			t.Errorf("expected empty slice for nil input, got %d elements", len(cpy))
		}
	})
}

// ---------------------------------------------------------------------------
// assertNoSystemPersist — comprehensive coverage supplement
// (basic test also exists in persist_bridge_test.go)
// ---------------------------------------------------------------------------

func TestAssertNoSystemPersistHelpers(t *testing.T) {
	t.Run("system message returns error", func(t *testing.T) {
		err := assertNoSystemPersist(llm.ChatMessage{Role: "system", Content: "sys prompt"})
		if err == nil {
			t.Error("expected error for system message, got nil")
		}
	})

	t.Run("user message returns nil", func(t *testing.T) {
		err := assertNoSystemPersist(llm.ChatMessage{Role: "user", Content: "hello"})
		if err != nil {
			t.Errorf("expected nil for user message, got %v", err)
		}
	})

	t.Run("assistant message returns nil", func(t *testing.T) {
		err := assertNoSystemPersist(llm.ChatMessage{Role: "assistant", Content: "hi"})
		if err != nil {
			t.Errorf("expected nil for assistant message, got %v", err)
		}
	})
}
