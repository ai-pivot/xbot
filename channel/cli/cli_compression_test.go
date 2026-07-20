package cli

import (
	"errors"
	"strings"
	"testing"
	"time"
	"xbot/channel"
	"xbot/protocol"

	tea "charm.land/bubbletea/v2"
)

// TestHistoryCompactedClearsPendingUserMsg verifies that the HistoryCompacted
// handler clears pendingUserMsg. Without this, the reload from DB adds the
// user message (with system guide text), and handleHistoryReload's content
// comparison fails (raw "继续" vs DB version with prepended text), causing
// pendingUserMsg to be appended again → duplicate user message.
func TestHistoryCompactedClearsPendingUserMsg(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Simulate user message added by sendMessage
	userMsg := cliMessage{
		role:      "user",
		content:   "继续",
		timestamp: time.Now(),
		dirty:     true,
	}
	model.messages = append(model.messages, userMsg)
	model.pendingUserMsg = &userMsg

	// Send HistoryCompacted progress event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// pendingUserMsg MUST be cleared — the reload will fetch the
	// authoritative version from DB.
	if model.pendingUserMsg != nil {
		t.Fatal("pendingUserMsg should be nil after HistoryCompacted — " +
			"keeping it causes duplicate user messages when reload completes")
	}
}

func TestRewindPanelIncludesCompactedUserHistoryID(t *testing.T) {
	model := initTestModel()
	model.messages = []cliMessage{
		{historyID: 11, recordType: "message", compactedBy: 20, role: "user", content: "old", timestamp: time.Unix(1, 0)},
		{historyID: 20, recordType: "compress", role: "system", content: "[Compacted context]", timestamp: time.Unix(2, 0)},
		{historyID: 21, recordType: "message", role: "user", content: "new", timestamp: time.Unix(3, 0)},
	}
	model.openRewindPanel()
	if len(model.rewindItems) != 2 || model.rewindItems[0].HistoryID != 11 || model.rewindItems[1].HistoryID != 21 {
		t.Fatalf("rewind items=%+v", model.rewindItems)
	}
	if rendered := model.renderMessage(&model.messages[0]); rendered == "" {
		t.Fatal("compacted source should remain visible in the TUI transcript")
	}
}

func TestAppendOnlyHistoryRowsStayVisible(t *testing.T) {
	compacted := toCLIMessage(protocol.HistoryMessage{
		HistoryID: 11, RecordType: "message", CompactedBy: 20, Role: "user", Content: "old",
	})
	marker := toCLIMessage(protocol.HistoryMessage{
		HistoryID: 20, RecordType: "compress", Role: "system", Content: "[Compacted context]\nsummary",
	})
	internal := toCLIMessage(protocol.HistoryMessage{
		HistoryID: 21, RecordType: "mask", Role: "control",
	})
	displayOnly := toCLIMessage(protocol.HistoryMessage{
		HistoryID: 22, RecordType: "message", Role: "user", Content: "original", DisplayOnly: true,
	})
	if compacted.hidden || marker.hidden {
		t.Fatalf("append-only display rows hidden: compacted=%v marker=%v", compacted.hidden, marker.hidden)
	}
	if displayOnly.hidden {
		t.Fatal("display-only original message should remain visible")
	}
	if !internal.hidden {
		t.Fatal("internal control row should stay hidden")
	}
}

func TestHistoryProjectionRendersRawToolMessages(t *testing.T) {
	model := initTestModel()
	msg := toCLIMessage(protocol.HistoryMessage{
		HistoryID:  30,
		RecordType: "message",
		Role:       "tool",
		Content:    "command output",
		ToolCallID: "call-1",
		ToolName:   "Shell",
		Timestamp:  time.Unix(1, 0),
	})
	if msg.hidden {
		t.Fatal("raw tool message should remain visible")
	}
	rendered := stripAnsi(model.renderMessage(&msg))
	if !strings.Contains(rendered, "Tool result · Shell") || !strings.Contains(rendered, "command output") {
		t.Fatalf("tool message missing tool-style content:\n%s", rendered)
	}
	if strings.Contains(rendered, "Assistant") {
		t.Fatalf("tool message was mislabeled as Assistant:\n%s", rendered)
	}
}

func TestHistoryProjectionRendersEmptyAssistantToolCall(t *testing.T) {
	model := initTestModel()
	msg := toCLIMessage(protocol.HistoryMessage{
		HistoryID:  31,
		RecordType: "message",
		Role:       "assistant",
		ToolCalls: []protocol.HistoryToolCall{{
			ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`,
		}},
		Timestamp: time.Unix(1, 0),
	})
	if msg.hidden || len(msg.tools) != 1 {
		t.Fatalf("assistant tool-call node was lost: %+v", msg)
	}
	rendered := stripAnsi(model.renderMessage(&msg))
	if !strings.Contains(rendered, "Assistant") || !strings.Contains(rendered, "Read") || !strings.Contains(rendered, "README.md") {
		t.Fatalf("assistant tool-call node not rendered:\n%s", rendered)
	}

	empty := toCLIMessage(protocol.HistoryMessage{HistoryID: 32, RecordType: "message", Role: "assistant"})
	if !empty.hidden {
		t.Fatal("fully empty assistant shell should not occupy a visual row")
	}
}

func TestHistoryRewoundReloadsOnlyExactSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{{role: "user", content: "keep"}}
	model.typing = true
	model.progressState.current = &protocol.ProgressEvent{Phase: "thinking"}
	model.progressState.twActive = true
	model.progressState.twVisible = 4
	model.rewindMode = true
	model.rewindItems = []rewindItem{{HistoryID: 1}}
	model.askUserSession = "chat"
	model.savePendingAskUser("chat", map[string]string{
		"ask_questions": `[{"question":"stale question"}]`,
		"request_id":    "stale-request",
	})
	model.openAskUserPanel([]askItem{{Question: "stale question"}}, nil, nil)
	model.typing = true
	reloaded := make(chan string, 1)
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{DynamicHistoryLoader: func(channelName, chatID string) ([]channel.HistoryMessage, error) {
			reloaded <- channelName + ":" + chatID
			return nil, nil
		}},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}

	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "history_rewound", Channel: "web", ChatID: "chat",
	}})
	if len(model.messages) != 1 {
		t.Fatal("same chat ID from another channel should not clear this transcript")
	}
	if model.panelState.mode != "askuser" || !model.rewindMode {
		t.Fatal("non-matching rewind closed active overlays")
	}

	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "history_rewound", Channel: "cli", ChatID: "chat",
	}})
	if len(model.messages) != 0 {
		t.Fatal("matching rewind event should clear stale transcript before reload")
	}
	if model.typing || model.progressState.current != nil || model.progressState.twActive || model.progressState.twVisible != 0 {
		t.Fatal("matching rewind event should clear live progress")
	}
	if model.panelState.mode == "askuser" || model.rewindMode || model.loadPendingAskUser("chat") != nil {
		t.Fatal("matching rewind event should clear AskUser, rewind, and persisted pending state")
	}
	select {
	case target := <-reloaded:
		if target != "cli:chat" {
			t.Fatalf("reload target=%s", target)
		}
	case <-time.After(time.Second):
		t.Fatal("matching rewind event did not force a history reload")
	}
}

func TestRewindWarningSurvivesResultThenResetAndReload(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{
		{historyID: 11, recordType: "message", role: "user", content: "rewrite"},
		{historyID: 12, recordType: "message", role: "assistant", content: "future"},
	}
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "rewrite"}}
	model.rewindMode = true
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
				return protocol.HistoryRewindResult{
					TargetHistoryID: 11, HistoryRewound: true, FilesRewound: false, CheckpointError: "restore failed",
				}, nil
			},
			DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) { return nil, nil },
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}

	runRewindCmd(t, model)
	assertRewindWarningCount(t, model.messages, 0)
	if model.rewindSync.warning == "" || !model.rewindPending || model.textarea.Value() != "" {
		t.Fatalf("result-first rewind did not hold warning/draft until reload: sync=%+v pending=%v draft=%q", model.rewindSync, model.rewindPending, model.textarea.Value())
	}

	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "history_rewound", Channel: "cli", ChatID: "chat", TargetHistoryID: 11,
	}})
	assertRewindWarningCount(t, model.messages, 0)
	if model.rewindSync.warning == "" || !model.rewindPending {
		t.Fatalf("matching reset released rewind before reload: sync=%+v pending=%v", model.rewindSync, model.rewindPending)
	}

	reload := receiveHistoryReload(t, model.channel.asyncCh)
	model.handleHistoryReload(reload)
	assertRewindWarningCount(t, model.messages, 1)
	if model.rewindPending || model.textarea.Value() != "rewrite" {
		t.Fatalf("reload did not unlock/populate composer: pending=%v draft=%q", model.rewindPending, model.textarea.Value())
	}
	if model.rewindSync.generation != 0 {
		t.Fatalf("rewind sync state survived reload: %+v", model.rewindSync)
	}
}

func TestRewindWarningSurvivesResetThenResultAndReload(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{
		{historyID: 11, recordType: "message", role: "user", content: "rewrite"},
		{historyID: 12, recordType: "message", role: "assistant", content: "future"},
	}
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "rewrite"}}
	model.rewindMode = true
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) { return nil, nil },
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	model.channel.config.RewindHistoryFn = func(string, string, int64) (protocol.HistoryRewindResult, error) {
		model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
			Action: "history_rewound", Channel: "cli", ChatID: "chat", TargetHistoryID: 11,
		}})
		return protocol.HistoryRewindResult{
			TargetHistoryID: 11, HistoryRewound: true, FilesRewound: false, CheckpointError: "restore failed",
		}, nil
	}

	// The reset clears messages before RewindHistoryFn returns. applyRewind must
	// not slice with the stale MsgIndex when it resumes.
	runRewindCmd(t, model)
	assertRewindWarningCount(t, model.messages, 0)
	if model.rewindSync.warning == "" || !model.rewindSync.resetSeen || !model.rewindPending {
		t.Fatalf("unexpected reset-first sync state: %+v", model.rewindSync)
	}

	reload := receiveHistoryReload(t, model.channel.asyncCh)
	model.handleHistoryReload(reload)
	assertRewindWarningCount(t, model.messages, 1)
	if model.rewindPending || model.textarea.Value() != "rewrite" {
		t.Fatalf("reload did not unlock/populate composer: pending=%v draft=%q", model.rewindPending, model.textarea.Value())
	}
	if model.rewindSync.generation != 0 {
		t.Fatalf("rewind sync state survived reload: %+v", model.rewindSync)
	}
}

func TestRewindEventThenRPCErrorStillCompletesAfterReload(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.textarea.SetValue("old draft")
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "rewrite"}}
	model.rewindMode = true
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
				return []channel.HistoryMessage{{HistoryID: 11, RecordType: "message", Role: "user", Content: "rewrite"}}, nil
			},
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	model.channel.config.RewindHistoryFn = func(string, string, int64) (protocol.HistoryRewindResult, error) {
		model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
			Action: "history_rewound", Channel: "cli", ChatID: "chat", TargetHistoryID: 11,
		}})
		return protocol.HistoryRewindResult{}, errors.New("connection closed")
	}

	runRewindCmd(t, model)
	if !model.rewindPending || !model.rewindSync.resultSeen || !model.rewindSync.resetSeen {
		t.Fatalf("committed rewind was discarded after RPC error: pending=%v sync=%+v", model.rewindPending, model.rewindSync)
	}
	model.handleHistoryReload(receiveHistoryReload(t, model.channel.asyncCh))
	if model.rewindPending || model.textarea.Value() != "rewrite" {
		t.Fatalf("reload did not finish committed rewind: pending=%v draft=%q", model.rewindPending, model.textarea.Value())
	}
	foundWarning := false
	for _, message := range model.messages {
		if strings.Contains(message.content, "file restore status is unknown") {
			foundWarning = true
		}
	}
	if !foundWarning {
		t.Fatalf("missing response-loss warning: %+v", model.messages)
	}
}

func TestRewindRPCErrorThenLateEventStillRestoresDraft(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{{historyID: 11, recordType: "message", role: "user", content: "rewrite"}}
	model.textarea.SetValue("old draft")
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "rewrite"}}
	model.rewindMode = true
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
				return protocol.HistoryRewindResult{}, errors.New("connection closed")
			},
			DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
				return []channel.HistoryMessage{{HistoryID: 11, RecordType: "message", Role: "user", Content: "rewrite"}}, nil
			},
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}

	runRewindCmd(t, model)
	if model.rewindPending || model.rewindSync.generation == 0 || !model.rewindSync.resultSeen {
		t.Fatalf("RPC error did not leave a late-event tombstone: pending=%v sync=%+v", model.rewindPending, model.rewindSync)
	}
	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "history_rewound", Channel: "cli", ChatID: "chat", TargetHistoryID: 11,
	}})
	model.handleHistoryReload(receiveHistoryReload(t, model.channel.asyncCh))
	if model.rewindPending || model.textarea.Value() != "rewrite" || model.rewindSync.generation != 0 {
		t.Fatalf("late reset did not complete the committed rewind: pending=%v draft=%q sync=%+v", model.rewindPending, model.textarea.Value(), model.rewindSync)
	}
}

func assertRewindWarningCount(t *testing.T, messages []cliMessage, want int) {
	t.Helper()
	count := 0
	for _, msg := range messages {
		if strings.HasPrefix(msg.content, rewindFilesWarningPrefix) {
			count++
		}
	}
	if count != want {
		t.Fatalf("rewind warning count=%d want=%d messages=%+v", count, want, messages)
	}
}

func receiveHistoryReload(t *testing.T, asyncCh <-chan tea.Msg) cliHistoryReloadMsg {
	t.Helper()
	select {
	case msg := <-asyncCh:
		reload, ok := msg.(cliHistoryReloadMsg)
		if !ok {
			t.Fatalf("reload message type=%T", msg)
		}
		return reload
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rewind history reload")
		return cliHistoryReloadMsg{}
	}
}

func TestRewindFailureLeavesTranscriptAndDraftUnchanged(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{{historyID: 11, recordType: "message", role: "user", content: "keep", timestamp: time.Unix(1, 0)}}
	model.textarea.SetValue("existing draft")
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "keep", Time: time.Unix(1, 0)}}
	model.rewindMode = true
	model.channel = &CLIChannel{config: &CLIChannelConfig{RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
		return protocol.HistoryRewindResult{}, errors.New("db failed")
	}}}
	runRewindCmd(t, model)
	if model.messages[0].historyID != 11 || model.messages[0].content != "keep" || model.textarea.Value() != "existing draft" {
		t.Fatalf("failed rewind mutated transcript/draft: messages=%+v draft=%q", model.messages, model.textarea.Value())
	}
	if model.rewindPending {
		t.Fatal("failed rewind left the composer locked")
	}
}

func TestRewindLocksComposerUntilAuthoritativeReload(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.inputReady = true
	model.textarea.SetValue("draft")
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "edit me"}}
	model.rewindMode = true
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
				return protocol.HistoryRewindResult{TargetHistoryID: 11, HistoryRewound: true, FilesRewound: true}, nil
			},
			DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) { return nil, nil },
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}

	cmd := model.applyRewind()
	if cmd == nil || !model.rewindPending {
		t.Fatal("rewind did not lock the composer before starting the RPC")
	}
	_, _, handled := model.handleEnterKey()
	if !handled || model.textarea.Value() != "draft" || len(model.messageQueue) != 0 {
		t.Fatalf("locked composer changed draft or queued input: draft=%q queue=%+v", model.textarea.Value(), model.messageQueue)
	}
	done := cmd().(cliRewindDoneMsg)
	model.handleRewindDoneMsg(done)
	if !model.rewindPending || model.textarea.Value() != "draft" {
		t.Fatalf("RPC result released rewind before reload: pending=%v draft=%q", model.rewindPending, model.textarea.Value())
	}
	select {
	case raw := <-model.channel.asyncCh:
		t.Fatalf("RPC result started reload before history_rewound: %T", raw)
	default:
	}
	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "history_rewound", Channel: "cli", ChatID: "chat", TargetHistoryID: 11,
	}})
	model.handleHistoryReload(receiveHistoryReload(t, model.channel.asyncCh))
	if model.rewindPending || model.textarea.Value() != "edit me" {
		t.Fatalf("authoritative reload did not unlock/populate composer: pending=%v draft=%q", model.rewindPending, model.textarea.Value())
	}
}

func TestMouseRewindReturnsAsyncCommand(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.rewindMode = true
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "edit me"}}
	called := false
	model.channel = &CLIChannel{config: &CLIChannelConfig{
		RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
			called = true
			return protocol.HistoryRewindResult{TargetHistoryID: 11, HistoryRewound: true, FilesRewound: true}, nil
		},
	}}

	handled, _, cmd := model.clickRewindItem(0)
	if !handled || cmd == nil {
		t.Fatal("mouse rewind did not return its asynchronous RPC command")
	}
	if called {
		t.Fatal("mouse rewind executed RPC on the Bubble Tea update goroutine")
	}
	if _, ok := cmd().(cliRewindDoneMsg); !ok || !called {
		t.Fatal("mouse rewind command did not execute the rewind RPC")
	}
}

func TestHistoryIDRewindBeforeCompressionKeepsVisibleSource(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{
		{historyID: 10, recordType: "message", compactedBy: 30, role: "user", content: "earlier", timestamp: time.Unix(1, 0)},
		{historyID: 20, recordType: "message", role: "user", content: "edit me", timestamp: time.Unix(2, 0)},
		{historyID: 30, recordType: "compress", role: "system", content: "[Compacted context]", timestamp: time.Unix(3, 0)},
	}
	model.rewindItems = []rewindItem{{HistoryID: 20, MsgIndex: 1, Content: "edit me", Time: time.Unix(2, 0)}}
	model.rewindMode = true
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
				return protocol.HistoryRewindResult{TargetHistoryID: 20, HistoryRewound: true, FilesRewound: true}, nil
			},
			DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
				return []channel.HistoryMessage{{HistoryID: 10, RecordType: "message", CompactedBy: 30, Role: "user", Content: "earlier", Timestamp: time.Unix(1, 0)}}, nil
			},
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	runRewindCmd(t, model)
	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "history_rewound", Channel: "cli", ChatID: "chat", TargetHistoryID: 20,
	}})
	model.handleHistoryReload(receiveHistoryReload(t, model.channel.asyncCh))
	if len(model.messages) != 1 || model.messages[0].hidden || model.messages[0].compactedBy != 30 {
		t.Fatalf("rewind changed surviving history metadata: %+v", model.messages)
	}
	if model.textarea.Value() != "edit me" {
		t.Fatalf("draft=%q", model.textarea.Value())
	}
}

func runRewindCmd(t *testing.T, model *cliModel) {
	t.Helper()
	cmd := model.applyRewind()
	if cmd == nil {
		t.Fatal("rewind command was nil")
	}
	done, ok := cmd().(cliRewindDoneMsg)
	if !ok {
		t.Fatalf("rewind command result type = %T", cmd())
	}
	model.handleRewindDoneMsg(done)
}

func TestRewindSlowRPCRunsOutsideUpdatePath(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.messages = []cliMessage{{historyID: 11, recordType: "message", role: "user", content: "edit"}}
	model.rewindItems = []rewindItem{{HistoryID: 11, MsgIndex: 0, Content: "edit"}}
	model.rewindMode = true
	started := make(chan struct{})
	release := make(chan struct{})
	model.channel = &CLIChannel{config: &CLIChannelConfig{RewindHistoryFn: func(string, string, int64) (protocol.HistoryRewindResult, error) {
		close(started)
		<-release
		return protocol.HistoryRewindResult{HistoryRewound: true, FilesRewound: true}, nil
	}}}

	handled, cmd := model.handleRewindKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if !handled || cmd == nil {
		t.Fatalf("rewind key result = handled %v, cmd %v", handled, cmd)
	}
	select {
	case <-started:
		t.Fatal("slow rewind RPC ran synchronously in the key Update path")
	default:
	}
	doneCh := make(chan tea.Msg, 1)
	go func() { doneCh <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("rewind command did not start RPC")
	}
	close(release)
	select {
	case raw := <-doneCh:
		done, ok := raw.(cliRewindDoneMsg)
		if !ok {
			t.Fatalf("rewind result type = %T", raw)
		}
		model.handleRewindDoneMsg(done)
	case <-time.After(time.Second):
		t.Fatal("rewind command did not finish")
	}
}

func TestAgentHistoryLoadUsesCanonicalHistoryLoader(t *testing.T) {
	const chatID = "cli:/repo:Agent-main/review:1"
	loaderCalled := false
	model := &cliModel{
		channelName: "agent",
		chatID:      chatID,
		channel: &CLIChannel{config: &CLIChannelConfig{
			DynamicHistoryLoader: func(channelName, gotChatID string) ([]channel.HistoryMessage, error) {
				loaderCalled = true
				if channelName != "agent" || gotChatID != chatID {
					t.Fatalf("history target=(%q,%q)", channelName, gotChatID)
				}
				return []channel.HistoryMessage{{
					HistoryID: 11, RecordType: "compress", Role: "system", Content: "summary",
				}}, nil
			},
			AgentSessionLLMStateFn: func(gotChatID string) (string, string, int64, int64, float64, int64, int64) {
				if gotChatID != chatID {
					t.Fatalf("LLM state chat ID=%q", gotChatID)
				}
				return "model", "sub", 1000, 100, 0.8, 50, 5
			},
		}},
	}

	msg, ok := model.suLoadHistoryCmd()().(suHistoryLoadMsg)
	if !ok {
		t.Fatal("suLoadHistoryCmd did not return suHistoryLoadMsg")
	}
	if !loaderCalled || msg.err != nil || len(msg.history) != 1 || msg.history[0].HistoryID != 11 {
		t.Fatalf("canonical history result=%+v", msg)
	}
	if msg.modelName != "model" || msg.subscriptionID != "sub" || msg.tokenPrompt != 50 {
		t.Fatalf("agent runtime state=%+v", msg)
	}
}

func TestHiddenCompressionSourcesDoNotCreateViewportBlankLines(t *testing.T) {
	ts := time.Now()
	base := initTestModel()
	base.messages = []cliMessage{{role: "user", content: "visible", timestamp: ts, dirty: true}}
	base.fullRebuild()
	want := strings.Join(base.rc.histLines, "\n")

	model := initTestModel()
	model.messages = []cliMessage{{role: "user", content: "visible", timestamp: ts, dirty: true}}
	for i := 0; i < 40; i++ {
		model.messages = append(model.messages, cliMessage{role: "user", content: "hidden", hidden: true, dirty: true})
	}
	model.fullRebuild()
	if got := strings.Join(model.rc.histLines, "\n"); got != want {
		t.Fatalf("hidden sources changed viewport output: got %d lines, want %d", len(model.rc.histLines), len(base.rc.histLines))
	}
}

// TestHistoryCompactedDoesNotCreateStreamingMessage verifies that the
// HistoryCompacted handler does NOT create a streaming message. The
// streaming target is restored from DB history by handleHistoryReload.
// Creating a streaming message here would produce duplicate assistants
// (one from here, one from DB) — the root cause of the double-assistant bug.
func TestHistoryCompactedDoesNotCreateStreamingMessage(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Add some messages
	model.messages = append(model.messages, cliMessage{
		role: "user", content: "hello", timestamp: time.Now(),
	})
	model.messages = append(model.messages, cliMessage{
		role: "assistant", content: "hi there", timestamp: time.Now(),
	})

	// Send HistoryCompacted progress event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// streamingMsgIdx MUST be -1 — no streaming message during compReloading.
	// handleHistoryReload will restore it from DB history.
	if model.streamingMsgIdx >= 0 {
		t.Fatal("streamingMsgIdx should be -1 after HistoryCompacted — " +
			"creating a streaming message here causes duplicate assistants")
	}
	// compReloading MUST be true — blocks auto-start during async reload.
	if !model.splashState.compReloading {
		t.Fatal("compReloading should be true after HistoryCompacted")
	}
}

// TestHistoryCompactedRestoresStreamingFromDBAfterReload verifies that after
// HistoryCompacted (which does NOT create a streaming message), the subsequent
// handleHistoryReload finds the DB assistant and marks it as the streaming
// target. This guarantees exactly ONE assistant — no dedup needed.
func TestHistoryCompactedRestoresStreamingFromDBAfterReload(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Send HistoryCompacted — clears messages, sets compReloading
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// No streaming message during compReloading
	if model.streamingMsgIdx >= 0 {
		t.Fatal("streamingMsgIdx should be -1 during compReloading")
	}

	// Reload completes with DB history containing an assistant
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "hello", Timestamp: time.Now()},
			{Role: "assistant", Content: "compacted context summary", Timestamp: time.Now()},
		},
	})

	// compReloading must be cleared
	if model.splashState.compReloading {
		t.Fatal("compReloading should be cleared after reload")
	}
	// Streaming target must be the DB assistant (not a newly created message)
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be >= 0 after reload — DB assistant should be streaming target")
	}
	if model.streamingMsgIdx >= len(model.messages) {
		t.Fatalf("streamingMsgIdx %d out of range (messages: %d)", model.streamingMsgIdx, len(model.messages))
	}
	streaming := model.messages[model.streamingMsgIdx]
	if streaming.role != "assistant" || !streaming.isPartial {
		t.Fatalf("DB assistant should be marked as streaming target: role=%s isPartial=%v",
			streaming.role, streaming.isPartial)
	}
	if streaming.content != "compacted context summary" {
		t.Fatalf("streaming target should be DB assistant with DB content, got %q", streaming.content)
	}

	// Exactly ONE assistant — by design, not by dedup
	assistantCount := 0
	for _, msg := range model.messages {
		if msg.role == "assistant" {
			assistantCount++
		}
	}
	if assistantCount != 1 {
		t.Fatalf("expected exactly 1 assistant (by design), got %d", assistantCount)
	}
}

// TestPostCompressionProgressUpdatesViewport verifies that after compression
// and reload, subsequent progress events render correctly. The streaming
// target is the DB assistant (restored by handleHistoryReload), not a
// separately created streaming message.
func TestPostCompressionProgressUpdatesViewport(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Compression event
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	// During compReloading, no streaming target
	if model.streamingMsgIdx >= 0 {
		t.Fatal("streamingMsgIdx should be -1 during compReloading")
	}

	// Reload completes with DB history
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "hello", Timestamp: time.Now()},
			{Role: "assistant", Content: "partial response", Timestamp: time.Now()},
		},
	})

	// After reload, streaming target is the DB assistant
	if model.streamingMsgIdx < 0 {
		t.Fatal("streamingMsgIdx should be valid after reload")
	}

	// Post-compression progress event with tool call
	sendProgress(model, &protocol.ProgressEvent{
		Phase:     "tool_exec",
		Iteration: 6,
		ActiveTools: []protocol.ToolProgress{
			{Name: "Shell", Status: "running", Label: "ls -la"},
		},
	})

	model.handleTickMsg()

	// The progress state should reflect the new iteration
	if model.progressState.current == nil {
		t.Fatal("progressState.current should not be nil after progress event")
	}
	if model.progressState.current.Iteration != 6 {
		t.Fatalf("expected iteration 6, got %d", model.progressState.current.Iteration)
	}

	// Verify the viewport actually has content (not empty/frozen)
	vpContent := model.viewport.View()
	if len(vpContent) == 0 {
		t.Fatal("viewport should have content after progress event — TUI is frozen")
	}
}

// TestHistoryCompactedNoDuplicateUserAfterReload verifies the end-to-end
// scenario: compression clears pendingUserMsg, reload loads DB history with
// the user message (with system guide), and no duplicate appears.
func TestHistoryCompactedNoDuplicateUserAfterReload(t *testing.T) {
	model := initTestModel()
	model.typing = true
	model.agentTurnID = 5

	// Simulate user message from sendMessage
	userMsg := cliMessage{
		role:      "user",
		content:   "继续",
		timestamp: time.Now(),
		dirty:     true,
	}
	model.messages = append(model.messages, userMsg)
	model.pendingUserMsg = &userMsg

	// HistoryCompacted — should clear pendingUserMsg
	sendProgress(model, &protocol.ProgressEvent{
		Phase:            "thinking",
		Iteration:        5,
		HistoryCompacted: true,
	})

	if model.pendingUserMsg != nil {
		t.Fatal("pendingUserMsg should be cleared by HistoryCompacted")
	}

	// Reload completes with DB history (user message has system guide text)
	model.handleHistoryReload(cliHistoryReloadMsg{
		channelName:      model.channelName,
		chatID:           model.chatID,
		forceFullRebuild: true,
		history: []channel.HistoryMessage{
			{Role: "user", Content: "[2026-06-22 16:44:09 CST] [CLI User]\n继续\n\n[System Guide]\n...", Timestamp: time.Now()},
			{Role: "assistant", Content: "compacted summary", Timestamp: time.Now()},
		},
	})

	// Count user messages — should be exactly 1 (from DB)
	userCount := 0
	for _, msg := range model.messages {
		if msg.role == "user" {
			userCount++
		}
	}
	if userCount != 1 {
		t.Fatalf("expected exactly 1 user message after reload, got %d — "+
			"pendingUserMsg was not cleared, causing duplicate", userCount)
	}
}
