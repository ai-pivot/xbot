package agent

import (
	"context"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/session"
)

func TestSplitRoleInstance(t *testing.T) {
	tests := []struct {
		in           string
		wantRole     string
		wantInstance string
		wantOK       bool
	}{
		{"explore:mem-1", "explore", "mem-1", true},
		{"explore/mem-1", "explore", "mem-1", true},
		{"explore", "", "", false},
		{"", "", "", false},
		{"role:", "", "", false},
		{":instance", "", "", false},
	}
	for _, tt := range tests {
		role, instance, ok := splitRoleInstance(tt.in)
		if role != tt.wantRole || instance != tt.wantInstance || ok != tt.wantOK {
			t.Errorf("splitRoleInstance(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.in, role, instance, ok, tt.wantRole, tt.wantInstance, tt.wantOK)
		}
	}
}

func TestCopyForkMessages(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/fork-copy.db")
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	defer mt.Close()

	src, _ := mt.GetOrCreateSession("web", "chat_src")
	dst, _ := mt.GetOrCreateSession("web", "chat_dst")

	// Seed source: user + assistant + a dangling tool_call (no tool result).
	src.AppendMessage(llm.NewUserMessage("hello"))
	src.AppendMessage(llm.NewAssistantMessage("hi"))
	// Dangling: assistant with tool_calls but no matching tool result.
	// SanitizeMessages must strip this (trailing unpaired tool_calls).
	src.AppendMessage(llm.ChatMessage{
		Role:      "assistant",
		Content:   "",
		ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "Shell", Arguments: "{}"}},
	})

	copied, err := copyForkMessages(src, dst)
	if err != nil {
		t.Fatalf("copyForkMessages: %v", err)
	}

	// SanitizeMessages strips the dangling assistant → copied = [user, assistant].
	if len(copied) != 2 {
		t.Fatalf("copied %d messages, want 2 (dangling tool_call stripped)", len(copied))
	}
	if copied[0].Role != "user" || copied[0].Content != "hello" {
		t.Errorf("copied[0] = %q/%q, want user/hello", copied[0].Role, copied[0].Content)
	}
	if copied[1].Role != "assistant" || copied[1].Content != "hi" {
		t.Errorf("copied[1] = %q/%q, want assistant/hi", copied[1].Role, copied[1].Content)
	}

	// IDs must be reset (DB re-assigns in destination).
	for _, m := range copied {
		if m.ID != 0 {
			t.Errorf("copied message ID = %d, want 0 (reset for destination)", m.ID)
		}
	}

	// Destination session must have the copied messages.
	dstMsgs, _ := dst.GetMessages()
	if len(dstMsgs) != 2 {
		t.Fatalf("dst GetMessages = %d, want 2", len(dstMsgs))
	}
}

func TestForkContextNote(t *testing.T) {
	note := forkContextNote("web:chat_abc", 42)
	if !strings.Contains(note, "web:chat_abc") {
		t.Errorf("note should contain source label %q", "web:chat_abc")
	}
	if !strings.Contains(note, "42") {
		t.Errorf("note should contain message count 42")
	}
	if !strings.Contains(note, "Inherited Context") {
		t.Errorf("note should contain 'Inherited Context' section header")
	}
	if !strings.Contains(note, "identity") {
		t.Errorf("note should mention identity (anti-confusion contract)")
	}
}

func TestForkSessionMessages(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/fork-session.db")
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	defer mt.Close()

	a := &Agent{multiSession: mt}

	src, _ := mt.GetOrCreateSession("web", "chat_src")
	src.AppendMessage(llm.NewUserMessage("original question"))
	src.AppendMessage(llm.NewAssistantMessage("original answer"))

	err = a.ForkSessionMessages("web", "chat_src", "web", "chat_dst")
	if err != nil {
		t.Fatalf("ForkSessionMessages: %v", err)
	}

	dst, _ := mt.GetOrCreateSession("web", "chat_dst")
	msgs, _ := dst.GetMessages()
	if len(msgs) != 2 {
		t.Fatalf("dst messages = %d, want 2", len(msgs))
	}
	if msgs[0].Content != "original question" {
		t.Errorf("dst[0] = %q, want 'original question'", msgs[0].Content)
	}
	if msgs[1].Content != "original answer" {
		t.Errorf("dst[1] = %q, want 'original answer'", msgs[1].Content)
	}
}

func TestResolveForkSourceSession_Me_MainAgent(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/fork-me.db")
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	defer mt.Close()

	a := &Agent{multiSession: mt}
	// Main agent session in cache.
	mt.GetOrCreateSession("cli", "/path/to/repo")

	// "me" with no bgParentKey → main agent session (originChannel, originChatID).
	ctx := context.Background()
	sess, label, err := a.resolveForkSourceSession(ctx, "cli", "/path/to/repo", "me")
	if err != nil {
		t.Fatalf("resolveForkSourceSession me: %v", err)
	}
	if sess == nil {
		t.Fatal("sess is nil")
	}
	if label != "cli:/path/to/repo" {
		t.Errorf("label = %q, want 'cli:/path/to/repo'", label)
	}
}

func TestResolveForkSourceSession_ChannelChatID(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/fork-chid.db")
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	defer mt.Close()

	a := &Agent{multiSession: mt}
	mt.GetOrCreateSession("web", "chat_abc")

	ctx := context.Background()
	sess, label, err := a.resolveForkSourceSession(ctx, "web", "chat_default", "web:chat_abc")
	if err != nil {
		t.Fatalf("resolveForkSourceSession channel:chatID: %v", err)
	}
	if sess == nil {
		t.Fatal("sess is nil")
	}
	if label != "web:chat_abc" {
		t.Errorf("label = %q, want 'web:chat_abc'", label)
	}
}

func TestResolveForkSourceSession_NotFound(t *testing.T) {
	mt, err := session.NewMultiTenant(t.TempDir() + "/fork-nf.db")
	if err != nil {
		t.Fatalf("NewMultiTenant: %v", err)
	}
	defer mt.Close()

	a := &Agent{multiSession: mt}
	ctx := context.Background()

	// Non-existent session → error.
	_, _, err = a.resolveForkSourceSession(ctx, "web", "chat_default", "web:nonexistent")
	if err == nil {
		t.Error("resolveForkSourceSession should error for non-existent session")
	}
}
