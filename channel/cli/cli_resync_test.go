package cli

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"xbot/channel"
	"xbot/protocol"
)

func TestResyncRequiredRestoresAuthoritativeSessionSnapshot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.todoManager = newCliTodoManager()
	model.messages = []cliMessage{{historyID: 99, role: "user", content: "stale future"}}
	model.todos = []protocol.TodoItem{{ID: 99, Text: "stale todo"}}
	model.todoManager.SetTodos(model.sessionKey(), model.todos)
	model.typing = true
	model.inputReady = true
	model.progressState.current = &protocol.ProgressEvent{Phase: "thinking", Iteration: 9}
	model.rewindMode = true
	model.rewindItems = []rewindItem{{HistoryID: 99}}
	model.askUserSession = "chat"
	model.savePendingAskUser("chat", map[string]string{
		"ask_questions": `[{"question":"stale question"}]`,
		"request_id":    "stale-request",
	})
	model.openAskUserPanel([]askItem{{Question: "stale question"}}, nil, nil)

	model.channel = &CLIChannel{config: &CLIChannelConfig{
		DynamicHistoryLoader: func(channelName, chatID string) ([]channel.HistoryMessage, error) {
			if channelName != "cli" || chatID != "chat" {
				t.Fatalf("history target = (%q, %q)", channelName, chatID)
			}
			return []channel.HistoryMessage{{HistoryID: 1, RecordType: "message", Role: "user", Content: "canonical"}}, nil
		},
		GetActiveProgressFn: func(channelName, chatID string, fetch protocol.ProgressFetch) *protocol.ProgressEvent {
			if channelName != "cli" || chatID != "chat" || fetch.ToFromIter() != -1 {
				t.Fatalf("progress target = (%q, %q, %d)", channelName, chatID, fetch.ToFromIter())
			}
			return &protocol.ProgressEvent{ChatID: "cli:chat", Phase: "thinking", Iteration: 2}
		},
		GetTodosFn: func(channelName, chatID string) []protocol.TodoItem {
			return []protocol.TodoItem{{ID: 1, Text: "canonical todo"}}
		},
		GetPendingAskUserFn: func(channelName, chatID string) *protocol.ProgressEvent {
			return nil
		},
	}}

	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "resync_required", Channel: "cli", ChatID: "chat",
	}})

	if len(model.messages) != 0 || model.progressState.current != nil || len(model.todos) != 0 {
		t.Fatal("resync did not immediately discard stale projected state")
	}
	if model.inputReady || !model.splashState.suLoading {
		t.Fatal("input must stay disabled while the authoritative snapshot is loading")
	}
	if model.panelState.mode == "askuser" || model.rewindMode {
		t.Fatal("resync did not close stale AskUser/rewind overlays")
	}
	if pending := model.loadPendingAskUser("chat"); pending != nil {
		t.Fatalf("stale pending AskUser survived reset: %+v", pending)
	}
	if len(model.pendingCmds) != 1 {
		t.Fatalf("authoritative reload commands = %d, want 1", len(model.pendingCmds))
	}

	raw := model.pendingCmds[0]()
	model.pendingCmds = nil
	snapshot, ok := raw.(suHistoryLoadMsg)
	if !ok || !snapshot.authoritative || !snapshot.todosKnown || !snapshot.pendingAskUserKnown {
		t.Fatalf("authoritative snapshot = %#v", raw)
	}
	model.handleSuHistoryLoad(snapshot)

	if len(model.messages) != 2 || model.messages[0].historyID != 1 || model.messages[0].content != "canonical" {
		t.Fatalf("restored messages = %+v", model.messages)
	}
	if !model.typing || model.inputReady || model.progressState.current == nil || model.progressState.current.Iteration != 2 {
		t.Fatalf("restored processing state: typing=%v inputReady=%v progress=%+v", model.typing, model.inputReady, model.progressState.current)
	}
	if len(model.todos) != 1 || model.todos[0].Text != "canonical todo" {
		t.Fatalf("restored todos = %+v", model.todos)
	}
	if model.panelState.mode == "askuser" || model.loadPendingAskUser("chat") != nil {
		t.Fatal("authoritative empty pending AskUser reopened stale local state")
	}
}

func TestResyncRequiredRestoresServerPendingAskUser(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	model.savePendingAskUser("chat", map[string]string{
		"ask_questions": `[{"question":"old question"}]`,
		"request_id":    "old-request",
	})
	model.channel = &CLIChannel{config: &CLIChannelConfig{
		DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
			return []channel.HistoryMessage{{HistoryID: 1, RecordType: "message", Role: "user", Content: "canonical"}}, nil
		},
		GetActiveProgressFn: func(string, string, protocol.ProgressFetch) *protocol.ProgressEvent { return nil },
		GetTodosFn:          func(string, string) []protocol.TodoItem { return nil },
		GetPendingAskUserFn: func(string, string) *protocol.ProgressEvent {
			return &protocol.ProgressEvent{
				RequestID: "new-request",
				Questions: []protocol.AskUserQuestion{{Question: "new question", Options: []string{"yes", "no"}}},
			}
		},
	}}

	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{Action: "resync_required", Channel: "cli", ChatID: "chat"}})
	snapshot := model.pendingCmds[0]().(suHistoryLoadMsg)
	model.pendingCmds = nil
	model.handleSuHistoryLoad(snapshot)

	if model.panelState.mode != "askuser" || model.askUserSession != "chat" {
		t.Fatal("server pending AskUser was not restored")
	}
	items := model.panelState.askUser.askItems
	if len(items) != 1 || items[0].Question != "new question" || len(items[0].Options) != 2 {
		t.Fatalf("restored AskUser items = %+v", items)
	}
	pending := model.loadPendingAskUser("chat")
	if pending == nil || pending.RequestID != "new-request" {
		t.Fatalf("persisted authoritative AskUser = %+v", pending)
	}
	if len(model.todos) != 0 {
		t.Fatalf("authoritative empty todos did not clear local state: %+v", model.todos)
	}
}

func TestResyncRequiredIgnoresSameChatIDFromAnotherChannel(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "shared"
	model.messages = []cliMessage{{historyID: 1, role: "user", content: "keep"}}

	model.handleSessionStateMsg(cliSessionStateMsg{event: protocol.SessionEvent{
		Action: "resync_required", Channel: "web", ChatID: "shared",
	}})

	if len(model.messages) != 1 || model.messages[0].content != "keep" {
		t.Fatalf("foreign-channel resync changed active session: %+v", model.messages)
	}
}

func TestResyncRetriesSnapshotWhenFinalReplyArrivesDuringLoad(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	loads := 0
	model.channel = &CLIChannel{config: &CLIChannelConfig{
		DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
			loads++
			if loads == 1 {
				return []channel.HistoryMessage{{HistoryID: 1, RecordType: "message", Role: "user", Content: "question"}}, nil
			}
			return []channel.HistoryMessage{
				{HistoryID: 1, RecordType: "message", Role: "user", Content: "question"},
				{HistoryID: 2, RecordType: "message", Role: "assistant", Content: "durable final"},
			}, nil
		},
	}}

	first := model.authoritativeSessionReloadCmd()().(suHistoryLoadMsg)
	model.splashState.suLoading = true
	model.handleAgentMessage(channel.OutboundMsg{Channel: "cli", ChatID: "chat", Content: "durable final"})
	cmds := model.handleSuHistoryLoad(first)
	if len(cmds) != 1 || !model.splashState.suLoading {
		t.Fatalf("stale resync snapshot was applied instead of retried: cmds=%d loading=%v", len(cmds), model.splashState.suLoading)
	}
	second := cmds[0]().(suHistoryLoadMsg)
	model.handleSuHistoryLoad(second)
	if loads != 2 || len(model.messages) != 2 || model.messages[1].content != "durable final" {
		t.Fatalf("retry did not restore final reply: loads=%d messages=%+v", loads, model.messages)
	}
}

func TestCompressionReloadRetriesWhenFinalReplyArrivesDuringLoad(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "chat"
	loads := 0
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
			loads++
			if loads == 1 {
				return []channel.HistoryMessage{{HistoryID: 1, RecordType: "message", Role: "user", Content: "question"}}, nil
			}
			return []channel.HistoryMessage{
				{HistoryID: 1, RecordType: "message", Role: "user", Content: "question"},
				{HistoryID: 2, RecordType: "message", Role: "assistant", Content: "durable final"},
			}, nil
		}},
		asyncCh: make(chan tea.Msg, 2),
		stopCh:  make(chan struct{}),
	}

	model.reloadMessagesFromSession(true)
	first := receiveHistoryReload(t, model.channel.asyncCh)
	model.handleAgentMessage(channel.OutboundMsg{Channel: "cli", ChatID: "chat", Content: "durable final"})
	model.handleHistoryReload(first)
	second := receiveHistoryReload(t, model.channel.asyncCh)
	model.handleHistoryReload(second)
	if loads != 2 || len(model.messages) != 2 || model.messages[1].content != "durable final" {
		t.Fatalf("retry did not preserve final reply: loads=%d messages=%+v", loads, model.messages)
	}
}

func TestResyncRetriesSnapshotWhenProgressOnlyFinalReplyArrivesDuringLoad(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "agent", "parent/worker:1"
	loads := 0
	model.channel = &CLIChannel{config: &CLIChannelConfig{
		DynamicHistoryLoader: func(string, string) ([]channel.HistoryMessage, error) {
			loads++
			if loads == 1 {
				return []channel.HistoryMessage{{HistoryID: 1, RecordType: "message", Role: "user", Content: "question"}}, nil
			}
			return []channel.HistoryMessage{
				{HistoryID: 1, RecordType: "message", Role: "user", Content: "question"},
				{HistoryID: 2, RecordType: "message", Role: "assistant", Content: "durable final"},
			}, nil
		},
	}}

	first := model.authoritativeSessionReloadCmd()().(suHistoryLoadMsg)
	model.splashState.suLoading = true
	model.applyProgressSnapshot(&protocol.ProgressEvent{Phase: "done", Iteration: 1, Content: "durable final"})
	cmds := model.handleSuHistoryLoad(first)
	if len(cmds) != 1 || !model.splashState.suLoading {
		t.Fatalf("progress-only final reply did not invalidate the stale snapshot: cmds=%d loading=%v", len(cmds), model.splashState.suLoading)
	}
	second := cmds[0]().(suHistoryLoadMsg)
	model.handleSuHistoryLoad(second)
	if loads != 2 || len(model.messages) != 2 || model.messages[1].content != "durable final" {
		t.Fatalf("retry did not restore progress-only final reply: loads=%d messages=%+v", loads, model.messages)
	}
}
