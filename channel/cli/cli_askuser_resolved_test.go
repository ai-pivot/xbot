package cli

import (
	"testing"

	"xbot/channel"
	"xbot/protocol"
)

// C2 (disk half): DeletePendingAskUserFile must remove the persisted cache so
// a resolved prompt cannot be restored later by checkAndRestorePendingAskUser.
func TestDeletePendingAskUserFileRemovesDiskCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.savePendingAskUser("chat", map[string]string{
		"ask_questions": `[{"question":"q"}]`,
		"request_id":    "req-1",
	})
	if model.loadPendingAskUser("chat") == nil {
		t.Fatal("precondition: pending ask_user file must exist")
	}

	DeletePendingAskUserFile("cli", "chat")

	if pending := model.loadPendingAskUser("chat"); pending != nil {
		t.Fatalf("disk cache survived DeletePendingAskUserFile: %+v", pending)
	}
}

// C2 (panel half): the ask_user_resolved handler sequence used by
// cmd/xbot-cli (delete disk cache + resync_required) must close an open stale
// AskUser panel for the resolved session.
func TestAskUserResolvedSequenceClosesOpenPanel(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.askUserSession = "chat"
	model.savePendingAskUser("chat", map[string]string{
		"ask_questions": `[{"question":"stale question"}]`,
		"request_id":    "req-1",
	})
	model.openAskUserPanel([]askItem{{Question: "stale question"}}, nil, nil)
	if model.panelState.mode != "askuser" {
		t.Fatal("precondition: askuser panel must be open")
	}

	// Same sequence as handleAskUserResolvedBroadcast in cmd/xbot-cli.
	DeletePendingAskUserFile("cli", "chat")
	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "resync_required", Channel: "cli", ChatID: "chat",
	}})

	if model.panelState.mode == "askuser" {
		t.Fatal("stale AskUser panel survived ask_user_resolved reconcile")
	}
	if pending := model.loadPendingAskUser("chat"); pending != nil {
		t.Fatalf("disk cache survived ask_user_resolved reconcile: %+v", pending)
	}
}

// C3: Esc cancel must inform the server with the ask_user_cancel marker —
// without it the pending prompt survives server-side and can be restored by a
// later resync / reconnect.
func TestPendingAskUserEscCancelSendsMarker(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.askUserSession = "chat"

	var got []channel.InboundMsg
	model.sendInboundFn = func(msg channel.InboundMsg) bool {
		got = append(got, msg)
		return true
	}

	model.pendingAskUserOnCancel("req-1")()

	if len(got) != 1 {
		t.Fatalf("cancel sent %d inbound messages, want 1: %#v", len(got), got)
	}
	if got[0].Content != "/cancel" {
		t.Fatalf("cancel content = %q, want %q", got[0].Content, "/cancel")
	}
	if got[0].Metadata["ask_user_cancel"] != "true" {
		t.Fatalf("cancel metadata missing ask_user_cancel=true: %#v", got[0].Metadata)
	}
}
