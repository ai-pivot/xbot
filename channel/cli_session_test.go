package channel

import (
	"strings"
	"testing"
)

func TestIsWorkDirPath(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		// Unix paths
		{"/home/user", true},
		{"/tmp", true},
		{".", true},
		{"./relative", true},
		{"~/home", true},
		// Windows paths
		{`C:\Users\foo`, true},
		{`D:\`, true},
		{`C:/Users/foo`, true},
		{`e:\path\to\dir`, true},
		// Invalid
		{"", false},
		{"just-a-string", false},
		{"Agent-brave-fox", false},
		{"1:\\invalid", false},
		{"_:\\nope", false},
		{"C:", false}, // no separator after colon
		{`C?bad`, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := isWorkDirPath(tt.input); got != tt.want {
				t.Errorf("isWorkDirPath(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseChatID_Unix(t *testing.T) {
	tests := []struct {
		chatID      string
		wantSession string
	}{
		// No session name → default
		{"/home/user/project", "default"},
		// With session name
		{"/home/user/project:Agent-brave-fox", "Agent-brave-fox"},
		{"/home/user/project:my-session", "my-session"},
		// Tilde
		{"~/project:my-session", "my-session"},
		// No colon at all
		{"/tmp", "default"},
	}

	for _, tt := range tests {
		t.Run(tt.chatID, func(t *testing.T) {
			_, sessionName := ParseChatID(tt.chatID)
			// Only verify sessionName; workDir resolution is OS-dependent
			// (filepath.Abs behaves differently on Windows vs Linux).
			if sessionName != tt.wantSession {
				t.Errorf("sessionName = %q, want %q", sessionName, tt.wantSession)
			}
		})
	}
}

func TestParseChatID_Windows(t *testing.T) {
	tests := []struct {
		name        string
		chatID      string
		wantWorkDir string
		wantSession string
	}{
		{
			name:        "windows backslash with session",
			chatID:      `C:\Users\foo\project:Agent-brave-fox`,
			wantWorkDir: `C:\Users\foo\project`,
			wantSession: "Agent-brave-fox",
		},
		{
			name:        "windows forward slash with session",
			chatID:      `C:/Users/foo/project:my-session`,
			wantWorkDir: `C:/Users/foo/project`,
			wantSession: "my-session",
		},
		{
			name:        "windows no session name",
			chatID:      `C:\Users\foo\project`,
			wantWorkDir: `C:\Users\foo\project`,
			wantSession: "default",
		},
		{
			name:        "windows lowercase drive",
			chatID:      `d:\dev\xbot:fix-session`,
			wantWorkDir: `d:\dev\xbot`,
			wantSession: "fix-session",
		},
		{
			name:        "windows root with session",
			chatID:      `C:\:Agent-test`,
			wantWorkDir: `C:\`,
			wantSession: "Agent-test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir, sessionName := ParseChatID(tt.chatID)
			if workDir != tt.wantWorkDir {
				t.Errorf("workDir = %q, want %q", workDir, tt.wantWorkDir)
			}
			if sessionName != tt.wantSession {
				t.Errorf("sessionName = %q, want %q", sessionName, tt.wantSession)
			}
		})
	}
}

func TestParseChatID_Invalid(t *testing.T) {
	tests := []struct {
		name   string
		chatID string
	}{
		{"bare string with colon", "just-a-string:foo"},
		{"agent name only", "Agent-brave-fox"},
		{"colon at end", "foo:"},
		{"single colon", ":"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir, sessionName := ParseChatID(tt.chatID)
			// Invalid chatIDs should return the original chatID as workDir and "default" as sessionName
			if workDir != tt.chatID {
				t.Errorf("workDir = %q, want %q (original chatID)", workDir, tt.chatID)
			}
			if sessionName != "default" {
				t.Errorf("sessionName = %q, want %q", sessionName, "default")
			}
		})
	}
}

// TestParseChatID_WindowsRoundTrip verifies that SessionChatID → ParseChatID round-trips correctly on Windows paths.
func TestParseChatID_WindowsRoundTrip(t *testing.T) {
	workDir := `C:\Users\foo\project`
	sessionName := "Agent-brave-fox"

	chatID := SessionChatID(workDir, sessionName)
	if !strings.Contains(chatID, ":") {
		t.Fatalf("SessionChatID(%q, %q) = %q, expected colon separator", workDir, sessionName, chatID)
	}

	gotWorkDir, gotSession := ParseChatID(chatID)
	if gotWorkDir != workDir {
		t.Errorf("round-trip workDir = %q, want %q", gotWorkDir, workDir)
	}
	if gotSession != sessionName {
		t.Errorf("round-trip sessionName = %q, want %q", gotSession, sessionName)
	}
}

// TestParseChatID_UnixRoundTrip verifies that SessionChatID → ParseChatID round-trips correctly on Unix paths.
func TestParseChatID_UnixRoundTrip(t *testing.T) {
	workDir := "/home/user/project"
	sessionName := "my-cool-session"

	chatID := SessionChatID(workDir, sessionName)
	if !strings.Contains(chatID, ":") {
		t.Fatalf("SessionChatID(%q, %q) = %q, expected colon separator", workDir, sessionName, chatID)
	}

	_, gotSession := ParseChatID(chatID)
	// Only verify sessionName; workDir resolution is OS-dependent
	// (filepath.Abs resolves Unix paths to D:\... on Windows).
	if gotSession != sessionName {
		t.Errorf("round-trip sessionName = %q, want %q", gotSession, sessionName)
	}
}
