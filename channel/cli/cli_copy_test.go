package cli

import (
	"testing"
	"time"

	ch "xbot/channel"
)

func TestCopyCommand_NoMessages(t *testing.T) {
	model := newCLIModel()
	model.locale = ch.GetLocale("en")
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// No messages at all → should return toast with CopyNoAssistant
	cmd := model.handleSlashCommand("/copy")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for /copy with no messages")
	}
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.icon != IconCross {
		t.Errorf("toast icon = %q, want %q", toast.icon, IconCross)
	}
	if toast.text != "No assistant message to copy" {
		t.Errorf("toast text = %q, want %q", toast.text, "No assistant message to copy")
	}
}

func TestCopyCommand_OnlyPartialAssistant(t *testing.T) {
	model := newCLIModel()
	model.locale = ch.GetLocale("en")
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// Only a partial (streaming) assistant message → should be skipped
	model.messages = append(model.messages, cliMessage{
		role:      "assistant",
		content:   "streaming...",
		timestamp: time.Now(),
		isPartial: true,
		dirty:     true,
		turnID:    1,
	})

	cmd := model.handleSlashCommand("/copy last")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for /copy last with only partial messages")
	}
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.icon != IconCross {
		t.Errorf("toast icon = %q, want %q", toast.icon, IconCross)
	}
}

func TestCopyCommand_LastAssistantSuccess(t *testing.T) {
	model := newCLIModel()
	model.locale = ch.GetLocale("en")
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// Two assistant messages, second is the latest completed one
	model.messages = append(model.messages,
		cliMessage{
			role:      "assistant",
			content:   "first reply",
			timestamp: time.Now().Add(-time.Minute),
			isPartial: false,
			dirty:     true,
			turnID:    1,
		},
		cliMessage{
			role:      "assistant",
			content:   "second reply",
			timestamp: time.Now(),
			isPartial: false,
			dirty:     true,
			turnID:    2,
		},
	)

	// Should take the async path (returns nil cmd, toast goes through channel.SendToast)
	// Without a real CLIChannel, the toast is dropped — but the handler must not
	// return an error toast.
	cmd := model.handleSlashCommand("/copy")
	if cmd != nil {
		msg := cmd()
		toast, ok := msg.(cliToastMsg)
		if ok && toast.icon == IconCross {
			t.Fatalf("unexpected error toast: %s", toast.text)
		}
	}
}

func TestCopyCommand_AllEmptyMessages(t *testing.T) {
	model := newCLIModel()
	model.locale = ch.GetLocale("en")
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// Only system messages → no copyable content
	model.messages = append(model.messages, cliMessage{
		role:      "system",
		content:   "system message",
		timestamp: time.Now(),
		dirty:     true,
	})

	cmd := model.handleSlashCommand("/copy all")
	if cmd == nil {
		t.Fatal("expected non-nil cmd for /copy all with no copyable messages")
	}
	msg := cmd()
	toast, ok := msg.(cliToastMsg)
	if !ok {
		t.Fatalf("expected cliToastMsg, got %T", msg)
	}
	if toast.icon != IconCross {
		t.Errorf("toast icon = %q, want %q", toast.icon, IconCross)
	}
	if toast.text != "No messages to copy" {
		t.Errorf("toast text = %q, want %q", toast.text, "No messages to copy")
	}
}

func TestCopyCommand_UsageHint(t *testing.T) {
	model := newCLIModel()
	model.locale = ch.GetLocale("en")
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	// Unknown subcommand → should show usage hint
	model.handleSlashCommand("/copy bogus")

	// Check that a system message was added with the usage text
	if len(model.messages) == 0 {
		t.Fatal("expected a system message with usage hint")
	}
	lastMsg := model.messages[len(model.messages)-1]
	if lastMsg.role != "system" {
		t.Errorf("expected system message, got role %q", lastMsg.role)
	}
	if lastMsg.content != "Usage: /copy [last|all]" {
		t.Errorf("usage text = %q, want %q", lastMsg.content, "Usage: /copy [last|all]")
	}
}

func TestCopyCommand_UsageHintJapanese(t *testing.T) {
	model := newCLIModel()
	model.locale = ch.GetLocale("ja")
	model.handleResize(80, 24)
	model.channelName = "cli"
	model.chatID = "/test"

	model.handleSlashCommand("/copy bogus")

	if len(model.messages) == 0 {
		t.Fatal("expected a system message with usage hint")
	}
	lastMsg := model.messages[len(model.messages)-1]
	if lastMsg.content != "使い方: /copy [last|all]" {
		t.Errorf("ja usage text = %q, want %q", lastMsg.content, "使い方: /copy [last|all]")
	}
}
