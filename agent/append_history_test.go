package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"xbot/llm"
	"xbot/protocol"
	"xbot/session"
	"xbot/storage/sqlite"
	"xbot/tools"
)

func newAgentHistorySession(t *testing.T) (*session.MultiTenantSession, *session.TenantSession) {
	t.Helper()
	mt, err := session.NewMultiTenant(t.TempDir() + "/agent-history.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mt.Close() })
	sess, err := mt.GetOrCreateSession("test", "chat")
	if err != nil {
		t.Fatal(err)
	}
	return mt, sess
}

func TestPersistenceBridgeCompressionAppendsAndReplays(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	if err := sess.AddMessage(llm.NewUserMessage("raw user")); err != nil {
		t.Fatal(err)
	}
	if err := sess.AddMessage(llm.NewAssistantMessage("raw answer")); err != nil {
		t.Fatal(err)
	}
	loaded, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewPersistenceBridge(sess, len(loaded))
	historyID, err := bridge.RewriteAfterCompress([]llm.ChatMessage{{Role: "user", Content: "summary"}}, 1)
	if err != nil || historyID == 0 {
		t.Fatalf("rewrite historyID=%d err=%v", historyID, err)
	}
	active, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 || active[0].Content != "summary" {
		t.Fatalf("active=%+v", active)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[0].Message.Content != "raw user" || records[1].Message.Content != "raw answer" || records[2].Type != sqlite.HistoryRecordCompress {
		t.Fatalf("full history=%+v", records)
	}
}

func TestRunWaitingUserPersistsToolPairAndControlAtomically(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	userID, err := sess.AppendMessage(llm.NewUserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	mock := &mockLLM{responses: []llm.LLMResponse{{
		FinishReason: llm.FinishReasonToolCalls,
		ToolCalls:    []llm.ToolCall{{ID: "ask-1", Name: "AskUser", Arguments: `{}`}},
	}}}
	out := Run(context.Background(), RunConfig{
		LLMClient: mock, Model: "test", Session: sess, AgentID: "main",
		Tools: newTestRegistry(&mockTool{name: "AskUser"}),
		Messages: []llm.ChatMessage{
			llm.NewSystemMessage("system"), {HistoryID: userID, Role: "user", Content: "hello"},
		},
		ToolExecutor: func(context.Context, llm.ToolCall) (*tools.ToolResult, error) {
			return &tools.ToolResult{Summary: "waiting", WaitingUser: true, Metadata: map[string]string{"request_id": "r1"}}, nil
		},
	})
	if out.Error != nil || !out.WaitingUser {
		t.Fatalf("Run output=%+v", out)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[1].Type != sqlite.HistoryRecordMessage || records[2].Message.ToolName != "AskUser" || records[3].Type != sqlite.HistoryRecordAskQuestion {
		t.Fatalf("AskUser history=%+v", records)
	}
	replay, err := sess.Replay()
	if err != nil {
		t.Fatal(err)
	}
	if replay.PendingAskUser == nil || replay.PendingAskUser.Metadata["request_id"] != "r1" {
		t.Fatalf("pending AskUser=%+v", replay.PendingAskUser)
	}
}

func TestCheckpointOutcomeReportsEmbeddedFileErrors(t *testing.T) {
	ok, message := checkpointOutcome(&protocol.RewindResult{Errors: []string{"restore failed"}}, nil)
	if ok || message == "" {
		t.Fatalf("embedded checkpoint errors were not surfaced: ok=%v message=%q", ok, message)
	}
	ok, message = checkpointOutcome(nil, errors.New("checkpoint unavailable"))
	if ok || message != "checkpoint unavailable" {
		t.Fatalf("checkpoint error=%v message=%q", ok, message)
	}
}

func TestApplyCompressAssignsSyntheticHistoryIDForSameRunEdit(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	for _, content := range []string{"old-0", "old-1", "old-2", "old-3"} {
		if _, err := sess.AppendMessage(llm.NewUserMessage(content)); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	cm := &mockContextManager{compressFn: func(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
		view := []llm.ChatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "synthetic summary that can be truncated"}}
		view = append(view, loaded...)
		return &CompressResult{LLMView: view, CompressedTokens: 10}, nil
	}}
	editor := NewContextEditor(NewContextEditStore(10))
	editor.BindSession(sess)
	compressed, err := ApplyCompress(context.Background(), CompressPipelineParams{
		CM: cm, Messages: loaded, LLMClient: &mockLLM{}, Model: "test",
		Persistence: NewPersistenceBridge(sess, len(loaded)), SyncMessages: func(messages []llm.ChatMessage) []llm.ChatMessage {
			editor.SetMessages(messages)
			return messages
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if compressed.NewMessages[1].HistoryID == 0 {
		t.Fatalf("synthetic summary has no history ID: %+v", compressed.NewMessages)
	}
	if _, err := editor.HandleRequest("truncate", map[string]any{
		"message_idx": float64(0), "max_chars": float64(9), "reason": "same run",
	}); err != nil {
		t.Fatalf("same-run context edit failed: %v", err)
	}
	replayed, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if replayed[0].HistoryID != compressed.NewMessages[1].HistoryID || replayed[0].Content == "synthetic summary that can be truncated" {
		t.Fatalf("same-run edit was not replayed: %+v", replayed[0])
	}
}

func TestContextEditToolUsesRunScopedHandlerConcurrently(t *testing.T) {
	tool := &tools.ContextEditTool{}
	newEditor := func(prefix string) *ContextEditor {
		messages := []llm.ChatMessage{{Role: "user", Content: prefix + "-0123456789"}}
		for i := 0; i < 4; i++ {
			messages = append(messages, llm.NewUserMessage(fmt.Sprintf("%s-protected-%d", prefix, i)))
		}
		editor := NewContextEditor(NewContextEditStore(10))
		editor.SetMessages(messages)
		return editor
	}
	editors := []*ContextEditor{newEditor("chat-a"), newEditor("chat-b")}
	var wg sync.WaitGroup
	for _, editor := range editors {
		editor := editor
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := tools.WithContextEditHandler(context.Background(), editor)
			toolCtx := &tools.ToolContext{Ctx: ctx}
			if _, err := tool.Execute(toolCtx, `{"action":"truncate","message_idx":0,"max_chars":6}`); err != nil {
				t.Errorf("run-scoped edit failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := editors[0].messages[0].Content; len(got) < 6 || got[:6] != "chat-a" {
		t.Fatalf("chat A edited through wrong handler: %q", got)
	}
	if got := editors[1].messages[0].Content; len(got) < 6 || got[:6] != "chat-b" {
		t.Fatalf("chat B edited through wrong handler: %q", got)
	}
}

func TestIncrementalPersistBatchFailureRetriesWithoutDuplicates(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	if _, err := mt.DB().Conn().Exec(`
		CREATE TRIGGER fail_history_message BEFORE INSERT ON session_messages
		WHEN NEW.content = 'fail' BEGIN SELECT RAISE(ABORT, 'injected failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	messages := []llm.ChatMessage{
		llm.NewUserMessage("first"), llm.NewAssistantMessage("fail"), llm.NewUserMessage("third"),
	}
	bridge := NewPersistenceBridge(sess, 0)
	if err := bridge.IncrementalPersist(messages); err == nil {
		t.Fatal("expected injected batch failure")
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 || bridge.LastPersistedCount() != 0 {
		t.Fatalf("failed batch partially persisted: records=%+v watermark=%d", records, bridge.LastPersistedCount())
	}
	if _, err := mt.DB().Conn().Exec(`DROP TRIGGER fail_history_message`); err != nil {
		t.Fatal(err)
	}
	if err := bridge.IncrementalPersist(messages); err != nil {
		t.Fatal(err)
	}
	records, err = sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || bridge.LastPersistedCount() != 3 {
		t.Fatalf("retry result: records=%+v watermark=%d", records, bridge.LastPersistedCount())
	}
	for i, msg := range messages {
		if msg.HistoryID == 0 || records[i].HistoryID != msg.HistoryID {
			t.Fatalf("message %d ID mismatch: msg=%+v record=%+v", i, msg, records[i])
		}
	}
}

func TestSyntheticToolPairAppendIsAtomic(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	if _, err := mt.DB().Conn().Exec(`
		CREATE TRIGGER fail_synthetic_tool BEFORE INSERT ON session_messages
		WHEN NEW.role = 'tool' AND NEW.tool_name = 'synthetic' BEGIN SELECT RAISE(ABORT, 'injected failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	state := &runState{
		cfg: RunConfig{Session: sess}, persistence: NewPersistenceBridge(sess, 0),
	}
	state.injectSyntheticToolPair(context.Background(), 1, "synthetic", "call-1", "assistant", "tool", "synthetic", 0)
	if state.persistenceErr == nil {
		t.Fatal("expected synthetic pair persistence failure")
	}
	if len(state.messages) != 0 {
		t.Fatalf("failed synthetic pair leaked into memory: %+v", state.messages)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("failed synthetic pair partially persisted: %+v", records)
	}
}

func TestSyntheticNotificationAppendFailureRemainsRetryable(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	if _, err := mt.DB().Conn().Exec(`
		CREATE TRIGGER fail_notification_pair BEFORE INSERT ON session_messages
		WHEN NEW.role = 'tool' AND NEW.tool_name = 'cron_fired' AND NEW.content LIKE '%retry me%'
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END;
	`); err != nil {
		t.Fatal(err)
	}

	a := &Agent{}
	sessionKey := "test:chat"
	ss := &bgSessionState{notifyCh: make(chan struct{}, 1)}
	a.bgSessionStates.Store(sessionKey, ss)
	a.enqueueBgNotifications([]tools.BgNotification{
		&tools.CronFired{Key: sessionKey, Sid: "user-1", Message: "commit once"},
		&tools.CronFired{Key: sessionKey, Sid: "user-1", Message: "retry me"},
	})

	state := &runState{
		cfg: RunConfig{
			Session:                    sess,
			DrainBgNotifications:       a.wireBgNotificationDrain(sessionKey),
			AcknowledgeBgNotifications: a.wireBgNotificationAcknowledge(sessionKey),
		},
		persistence: NewPersistenceBridge(sess, 0),
	}
	if consumed := state.drainAndInjectBgNotifications(context.Background(), 1); consumed != 1 {
		t.Fatalf("consumed=%d after second append failed, want 1", consumed)
	}
	if state.persistenceErr == nil {
		t.Fatal("expected notification pair persistence failure")
	}
	if got := len(ss.snapshotDrainedThisRun()); got != 1 {
		t.Fatalf("unacknowledged notifications=%d, want 1", got)
	}
	if records, err := sess.GetFullHistory(); err != nil || len(records) != 2 || !strings.Contains(records[1].Message.Content, "commit once") {
		t.Fatalf("records before retry=%+v err=%v", records, err)
	}

	a.requeueDrainedBgNotifications(sessionKey)
	if got := len(a.pendingBgNotifications(sessionKey)); got != 1 {
		t.Fatalf("requeued notifications=%d, want 1", got)
	}
	if _, err := mt.DB().Conn().Exec(`DROP TRIGGER fail_notification_pair`); err != nil {
		t.Fatal(err)
	}
	retry := &runState{
		cfg: RunConfig{
			Session:                    sess,
			DrainBgNotifications:       a.wireBgNotificationDrain(sessionKey),
			AcknowledgeBgNotifications: a.wireBgNotificationAcknowledge(sessionKey),
		},
		persistence: NewPersistenceBridge(sess, 0),
	}
	if consumed := retry.drainAndInjectBgNotifications(context.Background(), 2); consumed != 1 {
		t.Fatalf("retry consumed=%d, want 1", consumed)
	}
	if retry.persistenceErr != nil {
		t.Fatal(retry.persistenceErr)
	}
	if got := len(ss.snapshotDrainedThisRun()); got != 0 {
		t.Fatalf("acknowledged notifications left in ledger: %d", got)
	}
	if got := len(a.pendingBgNotifications(sessionKey)); got != 0 {
		t.Fatalf("acknowledged notifications left pending: %d", got)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 ||
		records[1].Message.ToolName != "cron_fired" || !strings.Contains(records[1].Message.Content, "commit once") ||
		records[3].Message.ToolName != "cron_fired" || !strings.Contains(records[3].Message.Content, "retry me") {
		t.Fatalf("retried pair records=%+v", records)
	}
}

func TestPersistenceBridgeAppendFailureDoesNotAdvanceWatermark(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	bridge := NewPersistenceBridge(sess, 0)
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	messages := []llm.ChatMessage{llm.NewUserMessage("must persist")}
	if err := bridge.IncrementalPersist(messages); err == nil {
		t.Fatal("expected append failure")
	}
	if bridge.LastPersistedCount() != 0 {
		t.Fatalf("watermark advanced to %d", bridge.LastPersistedCount())
	}
	if messages[0].HistoryID != 0 {
		t.Fatalf("history ID assigned on failure: %d", messages[0].HistoryID)
	}
}

func TestApplyCompressDoesNotResetTrackerWhenHistoryAppendFails(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	bridge := NewPersistenceBridge(sess, 1)
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	tracker := NewTokenTracker(321, 45)
	cm := &mockContextManager{compressFn: func(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
		return sampleCompressResult(), nil
	}}
	syncCalled := false
	result, err := ApplyCompress(context.Background(), CompressPipelineParams{
		CM: cm, Messages: []llm.ChatMessage{llm.NewUserMessage("raw")},
		LLMClient: &mockLLM{}, Model: "test", TokenTracker: tracker, Persistence: bridge,
		SyncMessages: func(messages []llm.ChatMessage) []llm.ChatMessage { syncCalled = true; return messages },
	})
	if err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if tracker.PromptTokens() != 321 || tracker.CompletionTokens() != 45 {
		t.Fatalf("tracker changed after failed append: prompt=%d completion=%d", tracker.PromptTokens(), tracker.CompletionTokens())
	}
	if syncCalled {
		t.Fatal("ContextEditor sync ran before failed history append")
	}
}

func TestCompressionWatermarkDoesNotDuplicateSnapshotTail(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	userID, err := sess.AppendMessage(llm.NewUserMessage("raw"))
	if err != nil {
		t.Fatal(err)
	}
	messages := []llm.ChatMessage{{Role: "system", Content: "system"}, {HistoryID: userID, Role: "user", Content: "raw"}}
	bridge := NewPersistenceBridge(sess, len(messages))
	cm := &mockContextManager{compressFn: func(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
		return &CompressResult{LLMView: []llm.ChatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "summary"}}, CompressedTokens: 10}, nil
	}}
	compressed, err := ApplyCompress(context.Background(), CompressPipelineParams{
		CM: cm, Messages: messages, LLMClient: &mockLLM{}, Model: "test", Persistence: bridge,
	})
	if err != nil {
		t.Fatal(err)
	}
	compressed.NewMessages = append(compressed.NewMessages,
		llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call", Name: "Shell", Arguments: `{}`}}},
		llm.NewToolMessage("Shell", "call", `{}`, "done"),
	)
	if err := bridge.IncrementalPersist(compressed.NewMessages); err != nil {
		t.Fatal(err)
	}
	active, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 3 || active[0].Content != "summary" || active[1].Role != "assistant" || active[2].Role != "tool" {
		t.Fatalf("duplicate snapshot tail after tool iteration: %+v", active)
	}
}

func TestContextWindowExceededStopsWhenCompressionAppendFails(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	cm := &mockContextManager{compressFn: func(context.Context, []llm.ChatMessage, llm.LLM, string) (*CompressResult, error) {
		return sampleCompressResult(), nil
	}}
	messages := []llm.ChatMessage{{Role: "system", Content: "system"}, {Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}
	state := &runState{
		cfg:      RunConfig{ContextManager: cm, LLMClient: &mockLLM{}, Model: "test", Session: sess},
		messages: messages, persistence: NewPersistenceBridge(sess, len(messages)), tokenTracker: NewTokenTracker(100, 0),
	}
	out, retry := state.handleFinalResponse(context.Background(), &llm.LLMResponse{FinishReason: llm.FinishReasonContextWindowExceeded})
	if retry || out == nil || out.Error == nil {
		t.Fatalf("retry=%v out=%+v", retry, out)
	}
}

func TestAggressiveTruncateAppendFailureKeepsEditorOnOldContext(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	messages := []llm.ChatMessage{{Role: "system", Content: "system"}}
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.ChatMessage{HistoryID: int64(i + 1), Role: "user", Content: fmt.Sprintf("m%d", i)})
	}
	editor := NewContextEditor(NewContextEditStore(10))
	editor.SetMessages(messages)
	state := &runState{cfg: RunConfig{Session: sess, ContextEditor: editor}, messages: messages,
		persistence: NewPersistenceBridge(sess, len(messages)), tokenTracker: NewTokenTracker(100, 0)}
	if state.aggressiveTruncate(context.Background()) {
		t.Fatal("truncate succeeded after append failure")
	}
	if len(state.messages) != len(messages) || len(editor.messages) != len(messages) || editor.messages[1].Content != "m0" {
		t.Fatalf("context changed after failed prune: state=%d editor=%+v", len(state.messages), editor.messages)
	}
}

func TestAggressiveTruncatePersistsReplayEquivalentSingleSystemContext(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	for i := 0; i < 10; i++ {
		if _, err := sess.AppendMessage(llm.ChatMessage{Role: "user", Content: fmt.Sprintf("m%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	messages := append([]llm.ChatMessage{{Role: "system", Content: "system"}}, loaded...)
	state := &runState{
		cfg: RunConfig{Session: sess}, messages: messages,
		persistence: NewPersistenceBridge(sess, len(messages)), tokenTracker: NewTokenTracker(100, 0),
	}
	if !state.aggressiveTruncate(context.Background()) {
		t.Fatal("aggressive truncate was not applied")
	}
	systemCount := 0
	for _, message := range state.messages {
		if message.Role == "system" {
			systemCount++
		}
	}
	if systemCount != 1 || len(state.messages) != 8 || state.messages[1].Role != "assistant" || !strings.HasPrefix(state.messages[1].Content, "[System notice:") {
		t.Fatalf("truncated context=%+v", state.messages)
	}
	replayed, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != len(state.messages)-1 {
		t.Fatalf("replayed=%+v state=%+v", replayed, state.messages)
	}
	for i := range replayed {
		if replayed[i].Role != state.messages[i+1].Role || replayed[i].Content != state.messages[i+1].Content {
			t.Fatalf("replay mismatch at %d: replay=%+v state=%+v", i, replayed[i], state.messages[i+1])
		}
	}
}

func TestContextEditorRollsBackWhenHistoryAppendFails(t *testing.T) {
	messages := []llm.ChatMessage{
		{HistoryID: 1, Role: "user", Content: "0123456789"},
		{HistoryID: 2, Role: "assistant", Content: "old"},
		{HistoryID: 3, Role: "user", Content: "protected1"},
		{HistoryID: 4, Role: "assistant", Content: "protected2"},
		{HistoryID: 5, Role: "user", Content: "protected3"},
	}
	editor := NewContextEditor(NewContextEditStore(10))
	editor.SetMessages(messages)
	editor.PersistFn = func([]int) error { return errors.New("disk full") }
	_, err := editor.HandleRequest("truncate", map[string]any{"message_idx": float64(0), "max_chars": float64(3)})
	if err == nil {
		t.Fatal("expected persistence error")
	}
	if got := messages[0].Content; got != "0123456789" {
		t.Fatalf("in-memory edit survived failed append: %q", got)
	}
}

func TestContextEditorBoundSessionReplaysAfterRestart(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	for _, content := range []string{"0123456789", "old answer", "protected1", "protected2", "protected3"} {
		if err := sess.AddMessage(llm.NewUserMessage(content)); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	editor := NewContextEditor(NewContextEditStore(10))
	editor.SetMessages(messages)
	editor.BindSession(sess)
	if _, err := editor.HandleRequest("truncate", map[string]any{"message_idx": float64(0), "max_chars": float64(3)}); err != nil {
		t.Fatal(err)
	}
	path := mt.DBPath()
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.NewMultiTenant(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := reopened.GetOrCreateSession("test", "chat")
	if err != nil {
		t.Fatal(err)
	}
	active, err := restarted.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if active[0].Content == "0123456789" {
		t.Fatalf("context edit was not replayed: %+v", active[0])
	}
	records, err := restarted.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Message.Content != "0123456789" {
		t.Fatalf("context edit overwrote raw row: %+v", records[0])
	}
}

func TestPersistenceBridgePruneReplaysAfterRestart(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	for _, content := range []string{"one", "two", "three"} {
		if err := sess.AddMessage(llm.NewUserMessage(content)); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	bridge := NewPersistenceBridge(sess, len(loaded))
	if err := bridge.AppendPrune(loaded[1:], 2); err != nil {
		t.Fatal(err)
	}
	path := mt.DBPath()
	if err := mt.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := session.NewMultiTenant(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := reopened.GetOrCreateSession("test", "chat")
	if err != nil {
		t.Fatal(err)
	}
	active, err := restarted.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 || active[0].Content != "two" || active[1].Content != "three" {
		t.Fatalf("restart active=%+v", active)
	}
}

func TestGetPendingAskUserRestoresFromHistory(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	_, _ = sess.AppendMessage(llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}})
	_, _ = sess.AppendMessage(llm.NewToolMessage("AskUser", "ask", `{}`, "waiting"))
	if _, err := sess.AppendAskQuestion(map[string]string{
		"request_id": "req-1", "ask_questions": `[{"question":"Continue?"}]`,
	}); err != nil {
		t.Fatal(err)
	}
	agent := &Agent{multiSession: mt}
	if pending := agent.GetPendingAskUser("other", "chat"); pending != nil {
		t.Fatalf("restored pending AskUser across channel boundary: %+v", pending)
	}
	pending := agent.GetPendingAskUser("test", "chat")
	if pending == nil || pending.RequestID != "req-1" || len(pending.Questions) != 1 || pending.Questions[0].Question != "Continue?" {
		t.Fatalf("restored pending=%+v", pending)
	}
	if _, ok := agent.waitingUserSessions.Load("test:chat"); !ok {
		t.Fatal("DB replay did not cache pending AskUser under its qualified key")
	}
	if _, ok := agent.waitingUserSessions.Load(""); ok {
		t.Fatal("DB replay cached pending AskUser under an empty key")
	}
}

func TestInteractiveInterruptionIsPersistedWithoutSystemMessage(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	appended, err := appendInteractiveInterruption(sess, "partial")
	if err != nil {
		t.Fatal(err)
	}
	if len(appended) != 3 {
		t.Fatalf("appended=%+v", appended)
	}
	for _, msg := range appended {
		if msg.HistoryID == 0 || msg.Role == "system" {
			t.Fatalf("invalid interruption message=%+v", msg)
		}
	}
	replayed, err := sess.GetMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 3 || replayed[2].ToolName != "user_cancelled" {
		t.Fatalf("replayed interruption=%+v", replayed)
	}
}

func TestInteractiveInterruptionBatchRollsBackOnFailure(t *testing.T) {
	mt, sess := newAgentHistorySession(t)
	if _, err := mt.DB().Conn().Exec(`
		CREATE TRIGGER fail_interruption_tool BEFORE INSERT ON session_messages
		WHEN NEW.role = 'tool' AND NEW.tool_name = 'user_cancelled'
		BEGIN SELECT RAISE(ABORT, 'injected failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if appended, err := appendInteractiveInterruption(sess, "partial"); err == nil || appended != nil {
		t.Fatalf("append result=%+v err=%v", appended, err)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("interruption batch partially persisted: %+v", records)
	}
}

func TestAgentOwnedUserAppendIsNotDuplicatedByRun(t *testing.T) {
	_, sess := newAgentHistorySession(t)
	userID, err := sess.AppendMessage(llm.NewUserMessage("current"))
	if err != nil {
		t.Fatal(err)
	}
	out := Run(context.Background(), RunConfig{
		LLMClient: &mockLLM{responses: []llm.LLMResponse{{Content: "done"}}},
		Model:     "test",
		Session:   sess,
		AgentID:   "main",
		Tools:     newTestRegistry(),
		Messages: []llm.ChatMessage{
			llm.NewSystemMessage("system"),
			{HistoryID: userID, Role: "user", Content: "current"},
		},
	})
	if out.Error != nil {
		t.Fatal(out.Error)
	}
	records, err := sess.GetFullHistory()
	if err != nil {
		t.Fatal(err)
	}
	userCount := 0
	for _, record := range records {
		if record.Type == sqlite.HistoryRecordMessage && record.Message.Role == "user" {
			userCount++
			if record.HistoryID != userID {
				t.Fatalf("user history_id=%d want %d", record.HistoryID, userID)
			}
		}
	}
	if userCount != 1 {
		t.Fatalf("user rows=%d history=%+v", userCount, records)
	}
}
