package agent

import (
	"context"
	"testing"
	"time"

	"xbot/bus"
	"xbot/channel"
	"xbot/llm"
	"xbot/protocol"
	"xbot/session"
	"xbot/tools"
)

func TestHandleRunOutputPreservesRequestIDFromRealAskUserMetadata(t *testing.T) {
	toolResult, err := (&tools.AskUserTool{}).Execute(&tools.ToolContext{}, `{"questions":[{"question":"Continue?","options":["yes","no"]}]}`)
	if err != nil {
		t.Fatal(err)
	}
	toolRequestID := toolResult.Metadata["request_id"]
	if toolRequestID == "" {
		t.Fatal("AskUser tool metadata has no request ID")
	}

	a := &Agent{}
	_, sess := newAgentHistorySession(t)
	// AppendAskQuestion requires a preceding AskUser tool result in history.
	toolMsg := llm.NewToolMessage("AskUser", toolResult.Metadata["request_id"], "", toolResult.Summary)
	if err := sess.AddMessage(toolMsg); err != nil {
		t.Fatal(err)
	}
	outbound, err := a.handleRunOutput(
		context.Background(),
		bus.InboundMessage{Channel: "web", ChatID: "chat-1"},
		&RunOutput{OutboundMsg: &channel.OutboundMsg{
			WaitingUser: toolResult.WaitingUser,
			Metadata:    toolResult.Metadata,
		}},
		sess,
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	requestID := outbound.Metadata["request_id"]
	if requestID != toolRequestID {
		t.Fatalf("WaitingUser outbound request ID = %q, want %q", requestID, toolRequestID)
	}
	pending := a.GetPendingAskUser("web", "chat-1")
	if pending == nil || pending.RequestID != requestID {
		t.Fatalf("pending AskUser = %#v, want request ID %q", pending, requestID)
	}
	if len(pending.Questions) != 1 || pending.Questions[0].Question != "Continue?" {
		t.Fatalf("pending AskUser questions = %#v", pending.Questions)
	}
}

func TestWithPendingAskUserBlocksConcurrentClearUntilCallbackReturns(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{
		RequestID: "request-1",
		Questions: []protocol.AskUserQuestion{{Question: "Continue?"}},
	})

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	withDone := make(chan bool, 1)
	go func() {
		withDone <- a.WithPendingAskUser("web", "chat-1", func(pending *protocol.ProgressEvent) bool {
			if pending.RequestID != "request-1" {
				t.Errorf("request ID = %q, want request-1", pending.RequestID)
			}
			close(callbackEntered)
			<-releaseCallback
			return true
		})
	}()
	<-callbackEntered

	clearDone := make(chan struct{})
	go func() {
		a.ClearPendingAskUser("web", "chat-1")
		close(clearDone)
	}()
	select {
	case <-clearDone:
		t.Fatal("ClearPendingAskUser returned while callback held the pending snapshot")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseCallback)
	if ok := <-withDone; !ok {
		t.Fatal("WithPendingAskUser returned false for a pending prompt")
	}
	select {
	case <-clearDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ClearPendingAskUser did not return after callback completed")
	}
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("pending AskUser after clear = %#v", pending)
	}
}

func TestWithPendingAskUserDoesNotBlockUnrelatedSessionMutation(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})
	a.setPendingAskUser("web", "chat-2", &protocol.ProgressEvent{RequestID: "request-2"})

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	withDone := make(chan struct{})
	go func() {
		defer close(withDone)
		a.WithPendingAskUser("web", "chat-1", func(*protocol.ProgressEvent) bool {
			close(callbackEntered)
			<-releaseCallback
			return true
		})
	}()
	<-callbackEntered

	mutationDone := make(chan struct{})
	go func() {
		a.ClearPendingAskUser("web", "chat-2")
		a.setPendingAskUser("web", "chat-3", &protocol.ProgressEvent{RequestID: "request-3"})
		close(mutationDone)
	}()
	select {
	case <-mutationDone:
	case <-time.After(time.Second):
		t.Fatal("unrelated pending AskUser mutation blocked behind callback")
	}
	if pending := a.GetPendingAskUser("web", "chat-2"); pending != nil {
		t.Fatalf("unrelated pending AskUser was not cleared: %#v", pending)
	}
	if pending := a.GetPendingAskUser("web", "chat-3"); pending == nil || pending.RequestID != "request-3" {
		t.Fatalf("unrelated pending AskUser was not set: %#v", pending)
	}

	close(releaseCallback)
	select {
	case <-withDone:
	case <-time.After(2 * time.Second):
		t.Fatal("pending AskUser callback did not finish")
	}
}

func TestWithPendingAskUserReturnsDetachedSnapshot(t *testing.T) {
	a := &Agent{}
	a.setPendingAskUser("cli", "chat-1", &protocol.ProgressEvent{
		RequestID: "request-1",
		Questions: []protocol.AskUserQuestion{{Question: "Original", Options: []string{"yes"}}},
	})

	if ok := a.WithPendingAskUser("web", "chat-1", func(*protocol.ProgressEvent) bool {
		return true
	}); ok {
		t.Fatal("WithPendingAskUser crossed the qualified channel boundary")
	}
	a.ClearPendingAskUser("web", "chat-1")

	if ok := a.WithPendingAskUser("cli", "chat-1", func(pending *protocol.ProgressEvent) bool {
		pending.RequestID = "changed"
		pending.Questions[0].Question = "Changed"
		pending.Questions[0].Options[0] = "no"
		return true
	}); !ok {
		t.Fatal("WithPendingAskUser did not find the qualified session")
	}

	pending := a.GetPendingAskUser("cli", "chat-1")
	if pending == nil || pending.RequestID != "request-1" || pending.Questions[0].Question != "Original" || pending.Questions[0].Options[0] != "yes" {
		t.Fatalf("stored pending AskUser was mutated through snapshot: %#v", pending)
	}
}

func TestPendingAskUserCancelPreventsReplayAndNextTurnCancellation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})
	// A stale queued cancel must also be removed when the pending prompt wins
	// the cancellation race.
	a.pendingCancel.Store(key, true)

	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	if a.WithPendingAskUser("web", "chat-1", func(*protocol.ProgressEvent) bool {
		t.Fatal("cancelled AskUser remained replayable")
		return true
	}) {
		t.Fatal("cancelled AskUser remained pending")
	}
	if _, pending := a.pendingCancel.LoadAndDelete(key); pending {
		t.Fatal("cancelled AskUser armed pendingCancel for the next turn")
	}

	select {
	case ack := <-a.bus.Outbound:
		if ack.Metadata["cancelled"] != "true" {
			t.Fatalf("cancel ack metadata = %#v", ack.Metadata)
		}
	default:
		t.Fatal("pending AskUser cancel produced no acknowledgement")
	}
}

// An AskUser answer sitting in the queue does NOT clear the pending cache:
// clearing is deferred until the ask_answer record is durably persisted
// (processMessage → resolvePendingAskUser "answered"). This pins the
// enqueue-window guarantee — a crash or a dropped queued message must not
// leave the DB pending with an emptied cache (that resurrected the panel on
// reconnect; the old code cleared right at enqueue).
func TestQueuedAskUserAnswerDoesNotClearPendingBeforePersist(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})

	// Enqueue window: the answer message was admitted to the per-session
	// queue, the ask_answer record is NOT persisted yet. The pending cache
	// MUST still be there.
	if pending := a.GetPendingAskUser("web", "chat-1"); pending == nil {
		t.Fatal("pending AskUser was cleared before the ask_answer record was persisted")
	}

	// A generic /cancel arriving in this window resolves the whole
	// interaction (there is no active Run): prompt cleared, ack sent, and
	// NO pendingCancel armed for the user's next message.
	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("pending AskUser remained after cancel: %#v", pending)
	}
	if _, armed := a.pendingCancel.LoadAndDelete("web:chat-1"); armed {
		t.Fatal("AskUser cancel in the answer-enqueue window armed pendingCancel")
	}
	select {
	case ack := <-a.bus.Outbound:
		if ack.Metadata["cancelled"] != "true" {
			t.Fatalf("cancel ack metadata = %#v", ack.Metadata)
		}
	default:
		t.Fatal("pending AskUser cancel produced no acknowledgement")
	}
}

func TestActiveAskUserAnswerCancelSignalsActiveContinuation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	cancelCh := make(chan struct{}, 1)
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	a.chatCancelCh.Store(key, cancelCh)
	// Simulate the narrow handoff window where the old prompt is still visible
	// even though its answer continuation has become active.
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})

	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	select {
	case <-cancelCh:
	default:
		t.Fatal("active AskUser continuation did not receive cancel signal")
	}
	if !a.finishActiveCancelState(key, reqCtx, reqCancel) {
		t.Fatal("active teardown did not observe cancel requested before teardown")
	}
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("old AskUser prompt remained after active cancel: %#v", pending)
	}
	if _, pending := a.pendingCancel.LoadAndDelete(key); pending {
		t.Fatal("active AskUser cancel armed pendingCancel")
	}
	select {
	case ack := <-a.bus.Outbound:
		t.Fatalf("active continuation received premature cancel ack: %#v", ack)
	default:
	}
}

func TestCancelAfterActiveTeardownTargetsNextQueuedContinuation(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	a.chatCancelCh.Store(key, make(chan struct{}, 1))

	if a.finishActiveCancelState(key, reqCtx, reqCancel) {
		t.Fatal("normal active teardown reported cancellation")
	}
	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	nextCtx, nextCancel := context.WithCancel(context.Background())
	defer nextCancel()
	if !a.registerActiveCancelState(key, make(chan struct{}, 1), nextCancel) {
		t.Fatal("cancel arriving after teardown was not preserved for the next queued continuation")
	}
	if nextCtx.Err() != context.Canceled {
		t.Fatal("post-teardown cancel did not cancel the next queued continuation")
	}
	a.finishActiveCancelState(key, nextCtx, nextCancel)
	select {
	case ack := <-a.bus.Outbound:
		t.Fatalf("post-teardown queued cancel received premature ack: %#v", ack)
	default:
	}
}

func TestAskUserCancelAfterPromptResolvedDoesNotArmPendingCancel(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	// The AskUser interaction has FULLY resolved: no active Run (WaitingUser
	// turn finished / answer processed) and no pending prompt (cleared by the
	// answer path). The web panel may still be showing its Cancel button — the
	// user taps it, web.go routes an /cancel tagged with ask_user_cancel.
	// interceptCancel MUST NOT arm pendingCancel here: the very next message
	// the user types would have its Run cancelled the instant it starts
	// (registerActiveCancelState consumes the pending marker and calls
	// reqCancel) — "cancel 掉 AskUser 后发下一条消息被取消了".
	a.interceptCancel(bus.InboundMessage{
		Channel:  "web",
		ChatID:   "chat-1",
		Content:  "/cancel",
		Metadata: map[string]string{"ask_user_cancel": "true"},
	})
	if _, pending := a.pendingCancel.LoadAndDelete(key); pending {
		t.Fatal("AskUser cancel after prompt resolution armed pendingCancel for the next turn")
	}
	select {
	case ack := <-a.bus.Outbound:
		t.Fatalf("no-op AskUser cancel must not emit a cancel ack: %#v", ack)
	default:
	}
}

func TestGenericCancelWithoutActiveRunStillArmsPendingCancel(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	// A generic /cancel (user typing /cancel, MessageInput stop button) with no
	// active Run MUST still arm pendingCancel — that is the documented way to
	// cancel a request still sitting in the queue. Only AskUser cancels are
	// exempt (the tagged path above).
	a.interceptCancel(bus.InboundMessage{Channel: "web", ChatID: "chat-1", Content: "/cancel"})
	if _, pending := a.pendingCancel.LoadAndDelete(key); !pending {
		t.Fatal("generic /cancel with no active Run must arm pendingCancel for the queued request")
	}
}

func TestAskUserCancelResetsWaitingUserBusy(t *testing.T) {
	a := &Agent{bus: bus.NewMessageBus()}
	key := "web:chat-1"
	// Simulate a WaitingUser turn: chatProcessLoop keeps ss.busy=true while the
	// AskUser panel is showing (notification-drain semantics), and the pending
	// AskUser prompt exists. The user taps Cancel (web.go routes /cancel tagged
	// with ask_user_cancel). interceptCancel clears the pending prompt — but it
	// MUST also reset the WaitingUser busy state, otherwise the session stays
	// busy forever: no session(idle) is ever emitted, the frontend sidebar
	// keeps running=true and the user can't do anything (reported: "后台 web
	// 会话用 askuser 取消后永远卡 busy，cancel 无效，什么事情都做不了").
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	ss.busy.Store(true)
	a.bgSessionStates.Store(key, ss)
	a.setPendingAskUser("web", "chat-1", &protocol.ProgressEvent{RequestID: "request-1"})

	a.interceptCancel(bus.InboundMessage{
		Channel:  "web",
		ChatID:   "chat-1",
		Content:  "/cancel",
		Metadata: map[string]string{"ask_user_cancel": "true"},
	})
	if ss.busy.Load() {
		t.Fatal("AskUser cancel did not reset the WaitingUser busy state (session stuck busy)")
	}
	if pending := a.GetPendingAskUser("web", "chat-1"); pending != nil {
		t.Fatalf("AskUser prompt remained after cancel: %#v", pending)
	}
	select {
	case ack := <-a.bus.Outbound:
		if ack.Metadata["cancelled"] != "true" {
			t.Fatalf("cancel ack metadata = %#v", ack.Metadata)
		}
	default:
		t.Fatal("pending AskUser cancel produced no acknowledgement")
	}
}

// ─── 权威校验：持久化的 ask_question/ask_answer 是唯一真相源 ─────────────

// seedAskQuestion appends the minimal valid AskUser exchange (assistant tool
// call + tool result) and the ask_question control record that Replay folds
// into PendingAskUser.
func seedAskQuestion(t *testing.T, sess *session.TenantSession, requestID string) {
	t.Helper()
	if _, err := sess.AppendMessage(llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendMessage(llm.NewToolMessage("AskUser", "ask", `{}`, "waiting")); err != nil {
		t.Fatal(err)
	}
	if _, err := sess.AppendAskQuestion(map[string]string{"request_id": requestID}); err != nil {
		t.Fatal(err)
	}
}

// T1: 陈旧内存项 + DB 已答（最新控制记录 = ask_answer）⇒ GetPendingAskUser
// 返回 nil、HasPendingAskUserFast 返回 false，且陈旧缓存项被真正剔除。
func TestStaleMemoryEntryDroppedWhenPersistedAnswered(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	seedAskQuestion(t, sess, "req-1")
	if _, err := sess.AppendAskAnswer("yes"); err != nil {
		t.Fatal(err)
	}

	a := &Agent{multiSession: mt}
	a.setPendingAskUser("test", "chat", &protocol.ProgressEvent{RequestID: "req-1"})

	if pending := a.GetPendingAskUser("test", "chat"); pending != nil {
		t.Fatalf("stale in-memory pending survived a persisted ask_answer: %+v", pending)
	}
	if a.HasPendingAskUserFast("test", "chat") {
		t.Fatal("HasPendingAskUserFast disagrees with GetPendingAskUser (stale cache trusted)")
	}
	if _, ok := a.waitingUserSessions.Load("test:chat"); ok {
		t.Fatal("stale in-memory entry was not dropped from the registry")
	}
}

// T2: DB pending + 内存空 ⇒ 两个查询入口都由持久化记录恢复并一致返回 true。
func TestPendingEntriesAgreeFromDBWithEmptyMemory(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	seedAskQuestion(t, sess, "req-2")

	a := &Agent{multiSession: mt}
	if !a.HasPendingAskUserFast("test", "chat") {
		t.Fatal("HasPendingAskUserFast missed a DB-pending question with empty memory")
	}
	pending := a.GetPendingAskUser("test", "chat")
	if pending == nil || pending.RequestID != "req-2" {
		t.Fatalf("GetPendingAskUser = %+v, want pending req-2", pending)
	}
}
