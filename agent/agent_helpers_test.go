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
	// Use a temp HOME directory so tests are deterministic and don't depend on
	// the host machine's ~/.agents/skills directory.
	tmpHome := t.TempDir()
	// Set HOME for Unix/macOS/Linux and USERPROFILE for Windows.
	// os.UserHomeDir() on Windows checks USERPROFILE first, then HOMEDRIVE+HOMEPATH, then HOME.
	// On Unix it only checks HOME. Setting both ensures cross-platform coverage.
	t.Setenv("HOME", tmpHome)
	t.Setenv("USERPROFILE", tmpHome)
	// Also clear HOMEDRIVE/HOMEPATH to prevent them from interfering on Windows.
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")

	// Helper to build absolute paths inside tmpHome
	homePath := func(parts ...string) string {
		all := append([]string{tmpHome}, parts...)
		return filepath.Join(all...)
	}

	t.Run("empty skillsDir, no ~/.agents/skills → empty result", func(t *testing.T) {
		got := resolveGlobalSkillsDirs("")
		if len(got) != 0 {
			t.Fatalf("expected empty slice, got %v", got)
		}
	})

	t.Run("non-empty skillsDir, no ~/.agents/skills → single entry", func(t *testing.T) {
		fakeSkillsDir := homePath("xbot-skills")
		os.MkdirAll(fakeSkillsDir, 0755)
		got := resolveGlobalSkillsDirs(fakeSkillsDir)
		if len(got) != 1 {
			t.Fatalf("expected 1 entry, got %d: %v", len(got), got)
		}
		want := evalRealPath(fakeSkillsDir)
		if got[0] != want {
			t.Errorf("got[0] = %q, want %q", got[0], want)
		}
	})

	t.Run("~/.agents/skills exists without skillsDir → auto-detected", func(t *testing.T) {
		agentsDir := homePath(".agents", "skills")
		os.MkdirAll(agentsDir, 0755)
		defer os.RemoveAll(agentsDir)
		got := resolveGlobalSkillsDirs("")
		if len(got) != 1 {
			t.Fatalf("expected 1 entry (auto-detected), got %d: %v", len(got), got)
		}
		want := evalRealPath(agentsDir)
		if got[0] != want {
			t.Errorf("got[0] = %q, want %q", got[0], want)
		}
	})

	t.Run("both dirs exist → two entries", func(t *testing.T) {
		agentsDir := homePath(".agents", "skills")
		os.MkdirAll(agentsDir, 0755)
		defer os.RemoveAll(agentsDir)
		fakeSkillsDir := homePath("xbot-skills-2")
		os.MkdirAll(fakeSkillsDir, 0755)
		got := resolveGlobalSkillsDirs(fakeSkillsDir)
		if len(got) != 2 {
			t.Fatalf("expected 2 entries, got %d: %v", len(got), got)
		}
		// First entry should be the configured skillsDir
		want0 := evalRealPath(fakeSkillsDir)
		if got[0] != want0 {
			t.Errorf("got[0] = %q, want %q", got[0], want0)
		}
		// Second entry should be ~/.agents/skills
		want1 := evalRealPath(agentsDir)
		if got[1] != want1 {
			t.Errorf("got[1] = %q, want %q", got[1], want1)
		}
	})

	t.Run("symlink deduplication: skillsDir symlinks to ~/.agents/skills", func(t *testing.T) {
		agentsDir := homePath(".agents", "skills")
		os.MkdirAll(agentsDir, 0755)
		defer os.RemoveAll(agentsDir)
		// Create a symlink that points to agentsDir
		symlinkPath := homePath("skills-link")
		if err := os.Symlink(agentsDir, symlinkPath); err != nil {
			t.Skipf("symlink not supported on this platform: %v", err)
		}
		defer os.Remove(symlinkPath)
		got := resolveGlobalSkillsDirs(symlinkPath)
		// Should deduplicate to a single entry because symlink resolves to the same real path
		if len(got) != 1 {
			t.Fatalf("expected 1 entry after symlink dedup, got %d: %v", len(got), got)
		}
	})
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
