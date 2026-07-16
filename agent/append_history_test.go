package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"xbot/bus"
	"xbot/llm"
	"xbot/session"
	"xbot/storage/sqlite"
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
	ok, err := bridge.RewriteAfterCompress([]llm.ChatMessage{{Role: "user", Content: "summary"}}, 1)
	if err != nil || !ok {
		t.Fatalf("rewrite ok=%v err=%v", ok, err)
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
	pending := agent.GetPendingAskUser("test", "chat")
	if pending == nil || pending.RequestID != "req-1" || len(pending.Questions) != 1 || pending.Questions[0].Question != "Continue?" {
		t.Fatalf("restored pending=%+v", pending)
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

func TestEagerSavedUserAppearsOnceAndKeepsHistoryID(t *testing.T) {
	history := []llm.ChatMessage{
		{HistoryID: 1, Role: "user", Content: "old"},
		{HistoryID: 2, Role: "assistant", Content: "answer"},
		{HistoryID: 3, Role: "user", Content: "current"},
	}
	msg := bus.InboundMessage{Content: "current", Metadata: map[string]string{"user_msg_eager_saved": "true"}}
	base, historyID, err := detachEagerSavedUser(history, msg)
	if err != nil {
		t.Fatal(err)
	}
	if len(base) != 2 || historyID != 3 {
		t.Fatalf("base=%+v historyID=%d", base, historyID)
	}
	assembled := append(append([]llm.ChatMessage(nil), base...), llm.ChatMessage{Role: "user", Content: "current with middleware"})
	bindEagerSavedUser(assembled, historyID)
	userCount := 0
	for _, item := range assembled {
		if item.Role == "user" && item.HistoryID == 3 {
			userCount++
		}
	}
	if userCount != 1 || assembled[len(assembled)-1].HistoryID != 3 {
		t.Fatalf("assembled=%+v", assembled)
	}
}
