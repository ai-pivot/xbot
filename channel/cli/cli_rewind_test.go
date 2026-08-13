package cli

import (
	"testing"
	"time"

	"xbot/channel"
	"xbot/protocol"

	tea "charm.land/bubbletea/v2"
)

// 复现 bug：正常使用中 sendMessage 添加的乐观回显 user 消息 historyID=0
// （DB 持久化后不回填），openRewindPanel 的 `msg.historyID == 0` 检查直接
// 跳过它们 → 本会话发送的消息全部不可 rewind（rewind 面板为空）。
func TestRewindPanelIncludesOptimisticUserMessage(t *testing.T) {
	model := initTestModel()
	// 模拟真实使用：消息来自 sendMessage 的乐观回显（无 historyID），
	// 会话未 reload（不切换会话/不压缩时不会触发历史重新加载）。
	model.messages = []cliMessage{
		{role: "user", content: "first", timestamp: time.Unix(1, 0)},
		{role: "assistant", content: "reply", timestamp: time.Unix(2, 0)},
		{role: "user", content: "second", timestamp: time.Unix(3, 0)},
	}
	model.openRewindPanel()
	if len(model.rewindItems) != 2 {
		t.Fatalf("rewind items=%+v, want 2 (optimistic user messages must be rewindable)", model.rewindItems)
	}
	if model.rewindItems[1].HistoryID != 0 {
		t.Fatalf("rewindItems[1].HistoryID=%d, want 0 (unresolved)", model.rewindItems[1].HistoryID)
	}
}

// 复现 bug：applyRewind 时 HistoryID==0（乐观回显消息），必须先从 history
// reload 解析真实 DB id 再调用 RewindHistoryFn —— 否则传给服务器的 historyID
// 为 0，RewindToHistoryID 报 "history_id is required"。
func TestApplyRewindResolvesDBIDFromReload(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "/chat"
	var gotHistoryID int64
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			DynamicHistoryLoader: func(channelName, chatID string) ([]channel.HistoryMessage, error) {
				return []channel.HistoryMessage{
					{ID: 10, Role: "user", Content: "first", Timestamp: time.Unix(1, 0)},
					{ID: 20, Role: "assistant", Content: "reply", Timestamp: time.Unix(2, 0)},
					{ID: 30, Role: "user", Content: "second", Timestamp: time.Unix(3, 0)},
				}, nil
			},
			RewindHistoryFn: func(channelName, chatID string, historyID int64) (protocol.HistoryRewindResult, error) {
				gotHistoryID = historyID
				return protocol.HistoryRewindResult{HistoryRewound: true, FilesRewound: true, TargetHistoryID: historyID}, nil
			},
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	// 乐观回显：historyID=0。DB 中同 content 的持久化行 id=30。
	model.messages = []cliMessage{{role: "user", content: "second", timestamp: time.Unix(3, 0)}}
	model.openRewindPanel()
	if len(model.rewindItems) != 1 {
		t.Fatalf("rewind items=%+v", model.rewindItems)
	}
	cmd := model.applyRewind()
	if cmd == nil {
		t.Fatal("applyRewind returned nil cmd")
	}
	msg := cmd()
	done, ok := msg.(cliRewindDoneMsg)
	if !ok {
		t.Fatalf("msg type=%T, want cliRewindDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("done.err=%v", done.err)
	}
	if gotHistoryID != 30 {
		t.Fatalf("rewind historyID=%d, want 30 (resolved from reload)", gotHistoryID)
	}
}

// 消息确实未持久化（队列中未处理）：必须报错而不是传 0 给服务器。
func TestApplyRewindUnpersistedMessageFails(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "/chat"
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			DynamicHistoryLoader: func(channelName, chatID string) ([]channel.HistoryMessage, error) {
				// DB 中没有这条消息
				return []channel.HistoryMessage{
					{ID: 10, Role: "user", Content: "other", Timestamp: time.Unix(1, 0)},
				}, nil
			},
			RewindHistoryFn: func(channelName, chatID string, historyID int64) (protocol.HistoryRewindResult, error) {
				t.Fatalf("RewindHistoryFn must not be called for unpersisted message, got historyID=%d", historyID)
				return protocol.HistoryRewindResult{}, nil
			},
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	model.messages = []cliMessage{{role: "user", content: "not persisted yet", timestamp: time.Unix(3, 0)}}
	model.openRewindPanel()
	cmd := model.applyRewind()
	if cmd == nil {
		t.Fatal("applyRewind returned nil cmd")
	}
	msg := cmd()
	done, ok := msg.(cliRewindDoneMsg)
	if !ok {
		t.Fatalf("msg type=%T, want cliRewindDoneMsg", msg)
	}
	if done.err == nil {
		t.Fatal("expected error for unpersisted message")
	}
}

// 已持久化的消息（historyID 已知）不经过 reload 解析，直接 rewind。
func TestApplyRewindKnownHistoryIDDirect(t *testing.T) {
	model := initTestModel()
	model.channelName, model.chatID = "cli", "/chat"
	var gotHistoryID int64
	loaderCalled := false
	model.channel = &CLIChannel{
		config: &CLIChannelConfig{
			DynamicHistoryLoader: func(channelName, chatID string) ([]channel.HistoryMessage, error) {
				loaderCalled = true
				return nil, nil
			},
			RewindHistoryFn: func(channelName, chatID string, historyID int64) (protocol.HistoryRewindResult, error) {
				gotHistoryID = historyID
				return protocol.HistoryRewindResult{HistoryRewound: true, FilesRewound: true, TargetHistoryID: historyID}, nil
			},
		},
		asyncCh: make(chan tea.Msg, 1),
		stopCh:  make(chan struct{}),
	}
	model.messages = []cliMessage{{historyID: 42, role: "user", content: "old", timestamp: time.Unix(1, 0)}}
	model.openRewindPanel()
	cmd := model.applyRewind()
	if cmd == nil {
		t.Fatal("applyRewind returned nil cmd")
	}
	msg := cmd()
	done, ok := msg.(cliRewindDoneMsg)
	if !ok {
		t.Fatalf("msg type=%T, want cliRewindDoneMsg", msg)
	}
	if done.err != nil {
		t.Fatalf("done.err=%v", done.err)
	}
	if gotHistoryID != 42 {
		t.Fatalf("rewind historyID=%d, want 42", gotHistoryID)
	}
	if loaderCalled {
		t.Fatal("DynamicHistoryLoader must not be called when historyID is already known")
	}
}
