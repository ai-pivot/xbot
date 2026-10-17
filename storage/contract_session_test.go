package storage

import (
	"fmt"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/storage/sqlite"
)

// testSessionStoreContract exercises the SessionStore half of the Storage
// contract. It runs against every backend (see sqlite_contract_test.go and
// pg_contract_test.go) so message and iteration-history semantics stay
// identical across implementations.
//
// Isolation: one tenant is created for the whole suite and every subtest
// starts by Clear-ing the session store, so subtests never observe each
// other's message rows. Iteration-history subtests additionally use turn ids
// that are unique per subtest: not every backend's Clear drops
// iteration_history rows, and a turn (turn_id, iteration) pair is the natural
// key of that table, so a shared low turn id would let one subtest's record
// bleed into another's.
func testSessionStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()
	tenantID := createTestTenant(t, s, uniqueChatID(t))

	// ── P0 ──

	t.Run("AddMessage_GetHistory_RoundTrip", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "hello")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "hi there")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		msgs, err := ss.GetHistory(tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		if len(msgs) < 2 {
			t.Fatalf("GetHistory returned %d messages, want >= 2", len(msgs))
		}
		if msgs[0].Role != "user" || msgs[0].Content != "hello" {
			t.Errorf("msgs[0] = {role:%q content:%q}, want {user hello}", msgs[0].Role, msgs[0].Content)
		}
		if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
			t.Errorf("msgs[1] = {role:%q content:%q}, want {assistant hi there}", msgs[1].Role, msgs[1].Content)
		}
	})

	t.Run("AddMessageWithID_ReturnsID", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		id, err := ss.AddMessageWithID(tenantID, dummyMsg("user", "with-id"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		if id <= 0 {
			t.Fatalf("AddMessageWithID returned id %d, want > 0", id)
		}
	})

	t.Run("GetHistoryBeforeForDisplay_beforeID_zero", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "d1")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "d2")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		// beforeID=0 means "unbounded" and must be normalized to MaxInt64 so
		// the newest rows are still matched (a raw `id < 0` would return
		// nothing).
		msgs, total, err := ss.GetHistoryBeforeForDisplay(tenantID, 0, 10)
		if err != nil {
			t.Fatalf("GetHistoryBeforeForDisplay(beforeID=0): %v", err)
		}
		if total < 2 {
			t.Errorf("total = %d, want >= 2 (beforeID=0 must be unbounded)", total)
		}
		if len(msgs) < 2 {
			t.Errorf("len(msgs) = %d, want >= 2", len(msgs))
		}
	})

	t.Run("GetHistoryBefore_beforeID_positive", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		ids := make([]int64, 0, 5)
		for i := 1; i <= 5; i++ {
			id, err := ss.AddMessageWithID(tenantID, dummyMsg("user", fmt.Sprintf("m%d", i)))
			if err != nil {
				t.Fatalf("AddMessageWithID(m%d): %v", i, err)
			}
			ids = append(ids, id)
		}
		// Only m1 and m2 have an id strictly below the 3rd message's id.
		beforeID := ids[2]
		msgs, err := ss.GetHistoryBefore(tenantID, beforeID, 10)
		if err != nil {
			t.Fatalf("GetHistoryBefore: %v", err)
		}
		if len(msgs) != 2 {
			t.Fatalf("GetHistoryBefore returned %d messages, want 2: %+v", len(msgs), msgs)
		}
		// Backends may scan DESC and reverse; the contract is chronological
		// (oldest first) order.
		if msgs[0].Content != "m1" || msgs[1].Content != "m2" {
			t.Errorf("GetHistoryBefore contents = [%q %q], want [m1 m2]", msgs[0].Content, msgs[1].Content)
		}
	})

	t.Run("GetAllMessages", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "all-u")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "all-a")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		msgs, err := ss.GetAllMessages(tenantID)
		if err != nil {
			t.Fatalf("GetAllMessages: %v", err)
		}
		if len(msgs) < 2 {
			t.Fatalf("GetAllMessages returned %d messages, want >= 2", len(msgs))
		}
	})

	t.Run("Clear", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "gone")); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear (second): %v", err)
		}
		count, err := ss.GetMessagesCount(tenantID)
		if err != nil {
			t.Fatalf("GetMessagesCount: %v", err)
		}
		if count != 0 {
			t.Errorf("GetMessagesCount after Clear = %d, want 0", count)
		}
	})

	t.Run("GetMessagesCount", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		for i := 1; i <= 5; i++ {
			if err := ss.AddMessage(tenantID, dummyMsg("user", fmt.Sprintf("count-m%d", i))); err != nil {
				t.Fatalf("AddMessage(m%d): %v", i, err)
			}
		}
		count, err := ss.GetMessagesCount(tenantID)
		if err != nil {
			t.Fatalf("GetMessagesCount: %v", err)
		}
		if count < 5 {
			t.Errorf("GetMessagesCount = %d, want >= 5", count)
		}
	})

	t.Run("GetMaxTurnID", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msg5 := dummyMsg("user", "turn-5")
		msg5.TurnID = 5
		if err := ss.AddMessage(tenantID, msg5); err != nil {
			t.Fatalf("AddMessage(turn=5): %v", err)
		}
		msg10 := dummyMsg("assistant", "turn-10")
		msg10.TurnID = 10
		if err := ss.AddMessage(tenantID, msg10); err != nil {
			t.Fatalf("AddMessage(turn=10): %v", err)
		}
		max, err := ss.GetMaxTurnID(tenantID)
		if err != nil {
			t.Fatalf("GetMaxTurnID: %v", err)
		}
		if max < 10 {
			t.Errorf("GetMaxTurnID = %d, want >= 10", max)
		}
	})

	t.Run("IterationHistory", func(t *testing.T) {
		const turn = uint64(9001)
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "iter-content"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		rec := sqlite.IterationRecord{
			Iteration:      1,
			Content:        "iter-content",
			Reasoning:      "iter-reasoning",
			Tokens:         42,
			Model:          "test-model",
			SubscriptionID: "test-sub",
		}
		if err := ss.AppendIterationHistory(tenantID, msgID, turn, rec); err != nil {
			t.Fatalf("AppendIterationHistory: %v", err)
		}
		iters, err := ss.GetIterationHistoryByTurn(tenantID, turn)
		if err != nil {
			t.Fatalf("GetIterationHistoryByTurn: %v", err)
		}
		got := findIterationByContent(iters, "iter-content")
		if got == nil {
			t.Fatalf("no iteration record with content %q in %+v", "iter-content", iters)
		}
		if got.Tokens != 42 {
			t.Errorf("iteration Tokens = %d, want 42", got.Tokens)
		}
		if got.Model != "test-model" {
			t.Errorf("iteration Model = %q, want %q", got.Model, "test-model")
		}
	})

	t.Run("AppendIterationTool", func(t *testing.T) {
		const turn = uint64(9002)
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "tool-iter"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		if err := ss.AppendIterationHistory(tenantID, msgID, turn, sqlite.IterationRecord{Iteration: 1, Content: "tool-iter"}); err != nil {
			t.Fatalf("AppendIterationHistory: %v", err)
		}
		found, err := ss.AppendIterationTool(tenantID, turn, 1, `{"name":"Shell","args":"{}"}`)
		if err != nil {
			t.Fatalf("AppendIterationTool: %v", err)
		}
		if !found {
			t.Fatalf("AppendIterationTool found = false, want true (the iteration record exists)")
		}
		iters, err := ss.GetIterationHistoryByTurn(tenantID, turn)
		if err != nil {
			t.Fatalf("GetIterationHistoryByTurn: %v", err)
		}
		got := findIterationByContent(iters, "tool-iter")
		if got == nil {
			t.Fatalf("no iteration record with content %q in %+v", "tool-iter", iters)
		}
		if !strings.Contains(got.Tools, "Shell") {
			t.Errorf("iteration Tools = %q, want it to contain the appended tool %q", got.Tools, "Shell")
		}
	})

	t.Run("GetIterationHistoryByTurns", func(t *testing.T) {
		const turnA, turnB = uint64(9011), uint64(9012)
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		idA, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "byTurns-A"))
		if err != nil {
			t.Fatalf("AddMessageWithID(A): %v", err)
		}
		if err := ss.AppendIterationHistory(tenantID, idA, turnA, sqlite.IterationRecord{Iteration: 1, Content: "byTurns-A"}); err != nil {
			t.Fatalf("AppendIterationHistory(A): %v", err)
		}
		idB, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "byTurns-B"))
		if err != nil {
			t.Fatalf("AddMessageWithID(B): %v", err)
		}
		if err := ss.AppendIterationHistory(tenantID, idB, turnB, sqlite.IterationRecord{Iteration: 1, Content: "byTurns-B"}); err != nil {
			t.Fatalf("AppendIterationHistory(B): %v", err)
		}
		byTurn, err := ss.GetIterationHistoryByTurns(tenantID, []uint64{turnA, turnB})
		if err != nil {
			t.Fatalf("GetIterationHistoryByTurns: %v", err)
		}
		for _, tc := range []struct {
			turn    uint64
			content string
		}{{turnA, "byTurns-A"}, {turnB, "byTurns-B"}} {
			if findIterationByContent(byTurn[tc.turn], tc.content) == nil {
				t.Errorf("turn %d: missing iteration with content %q (got %+v)", tc.turn, tc.content, byTurn[tc.turn])
			}
		}
	})

	t.Run("GetMaxIterationForTurn", func(t *testing.T) {
		const turn = uint64(9021)
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "max-iter"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		for _, iter := range []int{1, 2} {
			if err := ss.AppendIterationHistory(tenantID, msgID, turn, sqlite.IterationRecord{Iteration: iter, Content: fmt.Sprintf("max-iter-%d", iter)}); err != nil {
				t.Fatalf("AppendIterationHistory(iter=%d): %v", iter, err)
			}
		}
		max, err := ss.GetMaxIterationForTurn(tenantID, turn)
		if err != nil {
			t.Fatalf("GetMaxIterationForTurn: %v", err)
		}
		if max != 2 {
			t.Errorf("GetMaxIterationForTurn = %d, want 2", max)
		}
	})

	t.Run("HasAssistantReplyAfterLastUser", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "question")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		has, err := ss.HasAssistantReplyAfterLastUser(tenantID)
		if err != nil {
			t.Fatalf("HasAssistantReplyAfterLastUser (user only): %v", err)
		}
		if has {
			t.Errorf("HasAssistantReplyAfterLastUser = true with only a user message, want false")
		}
		// A final reply carries no tool calls.
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "answer")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		has, err = ss.HasAssistantReplyAfterLastUser(tenantID)
		if err != nil {
			t.Fatalf("HasAssistantReplyAfterLastUser (with reply): %v", err)
		}
		if !has {
			t.Errorf("HasAssistantReplyAfterLastUser = false after an assistant reply, want true")
		}
	})

	// ── P1 ──

	t.Run("LatestPreview", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "older")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "newest preview")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		preview, err := ss.LatestPreview(tenantID)
		if err != nil {
			t.Fatalf("LatestPreview: %v", err)
		}
		if preview != "newest preview" {
			t.Errorf("LatestPreview = %q, want %q", preview, "newest preview")
		}
	})

	t.Run("HasLegacyRows", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "legacy")); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		has, err := ss.HasLegacyRows(tenantID)
		if err != nil {
			t.Fatalf("HasLegacyRows: %v", err)
		}
		if !has {
			t.Errorf("HasLegacyRows = false after adding a message, want true")
		}
	})

	t.Run("ReplaceToolMessage", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if _, err := ss.AddMessageWithID(tenantID, llm.ChatMessage{
			Role: "tool", ToolName: "Shell", ToolCallID: "call_1", Content: "original output", TurnID: 1,
		}); err != nil {
			t.Fatalf("AddMessageWithID(tool): %v", err)
		}
		if err := ss.ReplaceToolMessage(tenantID, "Shell", "call_1", "replaced output"); err != nil {
			t.Fatalf("ReplaceToolMessage: %v", err)
		}
		msgs, err := ss.GetHistory(tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		var found bool
		for _, m := range msgs {
			if m.Role == "tool" && m.ToolCallID == "call_1" {
				found = true
				if m.Content != "replaced output" {
					t.Errorf("tool message content = %q, want %q", m.Content, "replaced output")
				}
			}
		}
		if !found {
			t.Fatalf("tool message (tool_call_id=call_1) not found in %+v", msgs)
		}
	})

	t.Run("UpdateMessageContent", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "original body")); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if err := ss.UpdateMessageContent(tenantID, 0, "updated body"); err != nil {
			t.Fatalf("UpdateMessageContent: %v", err)
		}
		msgs, err := ss.GetAllMessages(tenantID)
		if err != nil {
			t.Fatalf("GetAllMessages: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatalf("GetAllMessages returned no messages after UpdateMessageContent")
		}
		if msgs[0].Content != "updated body" {
			t.Errorf("msgs[0].Content = %q, want %q", msgs[0].Content, "updated body")
		}
	})

	t.Run("UpdateUserMessageContextTokens", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "token question")); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		if err := ss.UpdateUserMessageContextTokens(tenantID, 999); err != nil {
			t.Fatalf("UpdateUserMessageContextTokens: %v", err)
		}
		tokens, err := ss.GetLastUserMessageContextTokens(tenantID)
		if err != nil {
			t.Fatalf("GetLastUserMessageContextTokens: %v", err)
		}
		if tokens != 999 {
			t.Errorf("GetLastUserMessageContextTokens = %d, want 999", tokens)
		}
	})

	t.Run("GetLastUserMessageContent", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "first question")); err != nil {
			t.Fatalf("AddMessage(user 1): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "an answer")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "second question")); err != nil {
			t.Fatalf("AddMessage(user 2): %v", err)
		}
		content, err := ss.GetLastUserMessageContent(tenantID)
		if err != nil {
			t.Fatalf("GetLastUserMessageContent: %v", err)
		}
		if content != "second question" {
			t.Errorf("GetLastUserMessageContent = %q, want %q", content, "second question")
		}
	})

	t.Run("GetLastUserTurnID", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msg := dummyMsg("user", "turn question")
		msg.TurnID = 3
		if err := ss.AddMessage(tenantID, msg); err != nil {
			t.Fatalf("AddMessage: %v", err)
		}
		turnID, err := ss.GetLastUserTurnID(tenantID)
		if err != nil {
			t.Fatalf("GetLastUserTurnID: %v", err)
		}
		if turnID != 3 {
			t.Errorf("GetLastUserTurnID = %d, want 3", turnID)
		}
	})

	t.Run("AppendMessage", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		id, err := ss.AppendMessage(tenantID, dummyMsg("user", "appended message"))
		if err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
		if id <= 0 {
			t.Fatalf("AppendMessage returned id %d, want > 0", id)
		}
		msgs, err := ss.GetHistory(tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		var found bool
		for _, m := range msgs {
			if m.Content == "appended message" {
				found = true
			}
		}
		if !found {
			t.Errorf("appended message not visible in GetHistory: %+v", msgs)
		}
	})

	// ── P2 ──

	t.Run("AppendMessages", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		ids, err := ss.AppendMessages(tenantID, []llm.ChatMessage{
			dummyMsg("user", "batch-1"),
			dummyMsg("assistant", "batch-2"),
			dummyMsg("user", "batch-3"),
		})
		if err != nil {
			t.Fatalf("AppendMessages: %v", err)
		}
		if len(ids) != 3 {
			t.Fatalf("AppendMessages returned %d ids, want 3", len(ids))
		}
		for i, id := range ids {
			if id <= 0 {
				t.Errorf("AppendMessages ids[%d] = %d, want > 0", i, id)
			}
		}
		count, err := ss.GetMessagesCount(tenantID)
		if err != nil {
			t.Fatalf("GetMessagesCount: %v", err)
		}
		if count < 3 {
			t.Errorf("GetMessagesCount = %d, want >= 3", count)
		}
	})

	t.Run("AppendCommandMessage", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		id, err := ss.AppendCommandMessage(tenantID, "command", "!pwd output")
		if err != nil {
			t.Fatalf("AppendCommandMessage: %v", err)
		}
		if id <= 0 {
			t.Fatalf("AppendCommandMessage returned id %d, want > 0", id)
		}
		// Command rows are display-only (`record_type='command'`, display_only=1)
		// and are deliberately excluded from the LLM-context projection that
		// GetAllMessages/Replay return. The raw append-only history surface is
		// the correct read path for them.
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var found bool
		for _, rec := range records {
			if rec.Type != sqlite.HistoryRecordCommand {
				continue
			}
			found = true
			if rec.Message.Role != "command" {
				t.Errorf("command row role = %q, want %q", rec.Message.Role, "command")
			}
			if rec.Message.Content != "!pwd output" {
				t.Errorf("command row content = %q, want %q", rec.Message.Content, "!pwd output")
			}
			if !rec.Message.CommandRow {
				t.Errorf("command row CommandRow = false, want true")
			}
		}
		if !found {
			t.Fatalf("no record with type %q found in GetFullHistory: %+v", sqlite.HistoryRecordCommand, records)
		}
	})

	t.Run("AppendControl", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("user", "control target"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		// context_edit is the only control type that travels through
		// AppendControl (compress/prune/mask/ask_* own atomic append methods);
		// it carries a MessageMutations payload targeting the message row.
		mutations := sqlite.MessageMutations{Mutations: []sqlite.MessageMutation{{
			TargetHistoryID: msgID,
			Message:         llm.ChatMessage{ID: msgID, Role: "user", Content: "control target"},
		}}}
		id, err := ss.AppendControl(tenantID, sqlite.HistoryRecordContextEdit, msgID, mutations)
		if err != nil {
			t.Fatalf("AppendControl: %v", err)
		}
		if id <= 0 {
			t.Fatalf("AppendControl returned id %d, want > 0", id)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var found bool
		for _, rec := range records {
			if rec.Type == sqlite.HistoryRecordContextEdit {
				found = true
				if rec.Type == "" {
					t.Errorf("control record has an empty record_type")
				}
				if rec.TargetHistoryID != msgID {
					t.Errorf("control record target = %d, want %d", rec.TargetHistoryID, msgID)
				}
			}
		}
		if !found {
			t.Fatalf("no control record with type %q found in GetFullHistory: %+v", sqlite.HistoryRecordContextEdit, records)
		}
	})

	t.Run("GetUserMessageCount", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		for i := 1; i <= 3; i++ {
			if err := ss.AddMessage(tenantID, dummyMsg("user", fmt.Sprintf("user-%d", i))); err != nil {
				t.Fatalf("AddMessage(user %d): %v", i, err)
			}
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "assistant-1")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		count, err := ss.GetUserMessageCount(tenantID)
		if err != nil {
			t.Fatalf("GetUserMessageCount: %v", err)
		}
		if count != 3 {
			t.Errorf("GetUserMessageCount = %d, want 3", count)
		}
	})

	// ── Append-style control writes (snapshots, AskUser, masks, edits) ──
	//
	// Same isolation rules as above: each subtest Clear-s first, and iteration
	// subtests use turn ids unique to the subtest (the 9000xx block). The
	// control-path writes are verified by re-reading through the public read
	// surface (Replay / GetFullHistory / GetAllIterationHistory) — never by
	// poking SQL directly.

	t.Run("AppendContextSnapshot_CompressSnapshotVisible", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		userID, err := ss.AddMessageWithID(tenantID, dummyMsg("user", "snap-user"))
		if err != nil {
			t.Fatalf("AddMessageWithID(user): %v", err)
		}
		assistantID, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "snap-assistant"))
		if err != nil {
			t.Fatalf("AddMessageWithID(assistant): %v", err)
		}
		// Only compress/prune carry a context snapshot; any other record type is
		// rejected before a row is written.
		for _, bad := range []sqlite.HistoryRecordType{
			sqlite.HistoryRecordMessage, sqlite.HistoryRecordContextEdit,
			sqlite.HistoryRecordMask, sqlite.HistoryRecordAskQuestion,
		} {
			if _, err := ss.AppendContextSnapshot(tenantID, bad, nil); err == nil {
				t.Errorf("AppendContextSnapshot(%q) succeeded, want error", bad)
			}
		}
		snapshotID, err := ss.AppendContextSnapshot(tenantID, sqlite.HistoryRecordCompress, []llm.ChatMessage{
			{ID: userID, Role: "user", Content: "snap-user"},
			{ID: assistantID, Role: "assistant", Content: "snap-assistant"},
		})
		if err != nil {
			t.Fatalf("AppendContextSnapshot(compress): %v", err)
		}
		if snapshotID <= 0 {
			t.Fatalf("AppendContextSnapshot returned id %d, want > 0", snapshotID)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var snapshot *sqlite.HistoryRecord
		for i := range records {
			if records[i].HistoryID == snapshotID {
				snapshot = &records[i]
				break
			}
		}
		if snapshot == nil {
			t.Fatalf("snapshot history_id %d missing from GetFullHistory: %+v", snapshotID, records)
		}
		if snapshot.Type != sqlite.HistoryRecordCompress {
			t.Errorf("snapshot type = %q, want %q", snapshot.Type, sqlite.HistoryRecordCompress)
		}
		if len(snapshot.Data) == 0 {
			t.Errorf("snapshot record_data is empty, want the serialized ContextSnapshot payload")
		}
	})

	t.Run("AppendAskQuestion_RequiresActiveAskUserTool", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "ask-user")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		// No active AskUser tool result in the history yet ⇒ nothing to attach
		// the question to, so the append must fail.
		if _, err := ss.AppendAskQuestion(tenantID, map[string]string{"question": "no tool"}); err == nil {
			t.Fatalf("AppendAskQuestion with no AskUser tool result succeeded, want error")
		}
		toolID, err := ss.AddMessageWithID(tenantID, llm.ChatMessage{
			Role: "tool", ToolName: "AskUser", ToolCallID: "call_ask_q", Content: "{}", TurnID: 1,
		})
		if err != nil {
			t.Fatalf("AddMessageWithID(AskUser tool): %v", err)
		}
		questionID, err := ss.AppendAskQuestion(tenantID, map[string]string{"question": "pick one"})
		if err != nil {
			t.Fatalf("AppendAskQuestion: %v", err)
		}
		if questionID <= 0 {
			t.Fatalf("AppendAskQuestion returned id %d, want > 0", questionID)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var question *sqlite.HistoryRecord
		for i := range records {
			if records[i].HistoryID == questionID {
				question = &records[i]
				break
			}
		}
		if question == nil {
			t.Fatalf("ask_question history_id %d missing from GetFullHistory: %+v", questionID, records)
		}
		if question.Type != sqlite.HistoryRecordAskQuestion {
			t.Errorf("question type = %q, want %q", question.Type, sqlite.HistoryRecordAskQuestion)
		}
		if question.TargetHistoryID != toolID {
			t.Errorf("question target = %d, want the AskUser tool row %d", question.TargetHistoryID, toolID)
		}
	})

	t.Run("AppendAskQuestion_PendingGuardAndReplayState", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "ask-user-pending")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		toolID, err := ss.AddMessageWithID(tenantID, llm.ChatMessage{
			Role: "tool", ToolName: "AskUser", ToolCallID: "call_ask_pending", Content: "{}", TurnID: 1,
		})
		if err != nil {
			t.Fatalf("AddMessageWithID(AskUser tool): %v", err)
		}
		questionID, err := ss.AppendAskQuestion(tenantID, map[string]string{"question": "pick one"})
		if err != nil {
			t.Fatalf("AppendAskQuestion: %v", err)
		}
		replay, err := ss.Replay(tenantID)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if replay.PendingAskUser == nil {
			t.Fatalf("Replay().PendingAskUser = nil after an unanswered question, want the pending prompt")
		}
		if replay.PendingAskUser.HistoryID != questionID {
			t.Errorf("pending history_id = %d, want the ask_question row %d", replay.PendingAskUser.HistoryID, questionID)
		}
		if replay.PendingAskUser.ToolHistoryID != toolID {
			t.Errorf("pending tool target = %d, want the AskUser tool row %d", replay.PendingAskUser.ToolHistoryID, toolID)
		}
		if got := replay.PendingAskUser.Metadata["question"]; got != "pick one" {
			t.Errorf("pending metadata[question] = %q, want %q", got, "pick one")
		}
		// A second question while one is still pending must be rejected.
		if _, err := ss.AppendAskQuestion(tenantID, map[string]string{"question": "second"}); err == nil {
			t.Errorf("AppendAskQuestion while a question is pending succeeded, want error")
		}
	})

	t.Run("AppendAskAnswer_ResolvesPendingQuestion", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "answer-user")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		if _, err := ss.AddMessageWithID(tenantID, llm.ChatMessage{
			Role: "tool", ToolName: "AskUser", ToolCallID: "call_ask_answer", Content: "{}", TurnID: 1,
		}); err != nil {
			t.Fatalf("AddMessageWithID(AskUser tool): %v", err)
		}
		questionID, err := ss.AppendAskQuestion(tenantID, map[string]string{"question": "pick one"})
		if err != nil {
			t.Fatalf("AppendAskQuestion: %v", err)
		}
		answerID, err := ss.AppendAskAnswer(tenantID, "choice A")
		if err != nil {
			t.Fatalf("AppendAskAnswer: %v", err)
		}
		if answerID <= 0 {
			t.Fatalf("AppendAskAnswer returned id %d, want > 0", answerID)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var answer *sqlite.HistoryRecord
		for i := range records {
			if records[i].HistoryID == answerID {
				answer = &records[i]
				break
			}
		}
		if answer == nil {
			t.Fatalf("ask_answer history_id %d missing from GetFullHistory: %+v", answerID, records)
		}
		if answer.Type != sqlite.HistoryRecordAskAnswer {
			t.Errorf("answer type = %q, want %q", answer.Type, sqlite.HistoryRecordAskAnswer)
		}
		if answer.TargetHistoryID != questionID {
			t.Errorf("answer target = %d, want the pending question row %d", answer.TargetHistoryID, questionID)
		}
		replay, err := ss.Replay(tenantID)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if replay.PendingAskUser != nil {
			t.Errorf("Replay().PendingAskUser = %+v after the answer, want nil", replay.PendingAskUser)
		}
		// Answering again with nothing pending must fail.
		if _, err := ss.AppendAskAnswer(tenantID, "again"); err == nil {
			t.Errorf("AppendAskAnswer with no pending question succeeded, want error")
		}
	})

	t.Run("AppendAskAnswerWithUserMessage_AddsAnswerRow", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("user", "answer-with-user")); err != nil {
			t.Fatalf("AddMessage(user): %v", err)
		}
		if _, err := ss.AddMessageWithID(tenantID, llm.ChatMessage{
			Role: "tool", ToolName: "AskUser", ToolCallID: "call_ask_pair", Content: "{}", TurnID: 1,
		}); err != nil {
			t.Fatalf("AddMessageWithID(AskUser tool): %v", err)
		}
		questionID, err := ss.AppendAskQuestion(tenantID, map[string]string{"question": "pick one"})
		if err != nil {
			t.Fatalf("AppendAskQuestion: %v", err)
		}
		answerID, err := ss.AppendAskAnswerWithUserMessage(tenantID, "choice B", dummyMsg("user", "choice B"))
		if err != nil {
			t.Fatalf("AppendAskAnswerWithUserMessage: %v", err)
		}
		if answerID <= 0 {
			t.Fatalf("AppendAskAnswerWithUserMessage returned id %d, want > 0", answerID)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var answerControl bool
		var answerUserRow bool
		for _, rec := range records {
			if rec.HistoryID == answerID {
				if rec.Type != sqlite.HistoryRecordAskAnswer {
					t.Errorf("answer control type = %q, want %q", rec.Type, sqlite.HistoryRecordAskAnswer)
				}
				if rec.TargetHistoryID != questionID {
					t.Errorf("answer control target = %d, want the question row %d", rec.TargetHistoryID, questionID)
				}
				answerControl = true
			}
			if rec.Type == sqlite.HistoryRecordMessage && rec.Message.Role == "user" && rec.Message.Content == "choice B" {
				answerUserRow = true
			}
		}
		if !answerControl {
			t.Fatalf("ask_answer control row %d missing from GetFullHistory: %+v", answerID, records)
		}
		if !answerUserRow {
			t.Errorf("the answer user message is missing from GetFullHistory: %+v", records)
		}
		replay, err := ss.Replay(tenantID)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if replay.PendingAskUser != nil {
			t.Errorf("Replay().PendingAskUser = %+v after the answer, want nil", replay.PendingAskUser)
		}
		// With no pending question left the pair-append must fail.
		if _, err := ss.AppendAskAnswerWithUserMessage(tenantID, "stale", dummyMsg("user", "stale")); err == nil {
			t.Errorf("AppendAskAnswerWithUserMessage with no pending question succeeded, want error")
		}
	})

	t.Run("AppendMessagesAndAskQuestion_AtomicBatch", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		// A batch with no AskUser tool result is rejected up-front.
		if _, _, err := ss.AppendMessagesAndAskQuestion(tenantID, []llm.ChatMessage{dummyMsg("user", "orphan")}, nil); err == nil {
			t.Errorf("AppendMessagesAndAskQuestion without an AskUser tool result succeeded, want error")
		}
		batch := []llm.ChatMessage{
			{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "call_ask_batch", Name: "AskUser", Arguments: "{}"}}, TurnID: 1},
			{Role: "tool", ToolName: "AskUser", ToolCallID: "call_ask_batch", Content: "{}", TurnID: 1},
		}
		ids, questionID, err := ss.AppendMessagesAndAskQuestion(tenantID, batch, map[string]string{"question": "batch"})
		if err != nil {
			t.Fatalf("AppendMessagesAndAskQuestion: %v", err)
		}
		if len(ids) != len(batch) {
			t.Fatalf("AppendMessagesAndAskQuestion returned %d ids, want %d", len(ids), len(batch))
		}
		for i, id := range ids {
			if id <= 0 {
				t.Errorf("AppendMessagesAndAskQuestion ids[%d] = %d, want > 0", i, id)
			}
		}
		if questionID <= 0 {
			t.Fatalf("AppendMessagesAndAskQuestion returned question id %d, want > 0", questionID)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		var question *sqlite.HistoryRecord
		for i := range records {
			if records[i].HistoryID == questionID {
				question = &records[i]
				break
			}
		}
		if question == nil {
			t.Fatalf("ask_question history_id %d missing from GetFullHistory: %+v", questionID, records)
		}
		if question.Type != sqlite.HistoryRecordAskQuestion {
			t.Errorf("question type = %q, want %q", question.Type, sqlite.HistoryRecordAskQuestion)
		}
		// The question targets the batch's AskUser tool row, which is the last
		// element of the batch.
		if question.TargetHistoryID != ids[len(ids)-1] {
			t.Errorf("question target = %d, want the batch's AskUser tool row %d", question.TargetHistoryID, ids[len(ids)-1])
		}
		msgs, err := ss.GetHistory(tenantID, 10)
		if err != nil {
			t.Fatalf("GetHistory: %v", err)
		}
		var sawTool bool
		for _, m := range msgs {
			if m.Role == "tool" && m.ToolName == "AskUser" {
				sawTool = true
			}
		}
		if !sawTool {
			t.Errorf("the batch's AskUser tool row is missing from GetHistory: %+v", msgs)
		}
	})

	t.Run("AppendMasks_RewritesReplayContentOnly", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("user", "mask-original"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		// Empty mutation list is a no-op, not an error.
		if err := ss.AppendMasks(tenantID, nil); err != nil {
			t.Errorf("AppendMasks(nil) = %v, want nil (no-op)", err)
		}
		// A mask targeting a non-active history_id must be rejected.
		if err := ss.AppendMasks(tenantID, []sqlite.MaskMutation{{TargetHistoryID: 999999999, Content: "x"}}); err == nil {
			t.Errorf("AppendMasks with an unknown target succeeded, want error")
		}
		if err := ss.AppendMasks(tenantID, []sqlite.MaskMutation{{TargetHistoryID: msgID, Content: "[REDACTED]"}}); err != nil {
			t.Fatalf("AppendMasks: %v", err)
		}
		replay, err := ss.Replay(tenantID)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		var masked bool
		for _, m := range replay.Messages {
			if m.ID == msgID {
				masked = true
				if m.Content != "[REDACTED]" {
					t.Errorf("replayed content = %q, want the masked %q", m.Content, "[REDACTED]")
				}
			}
		}
		if !masked {
			t.Fatalf("masked message %d missing from Replay: %+v", msgID, replay.Messages)
		}
		// The mask is LLM-context only: the append-only/display history keeps
		// the original content.
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		if got := recordContent(records, msgID); got != "mask-original" {
			t.Errorf("display content = %q, want the untouched %q", got, "mask-original")
		}
	})

	t.Run("UpdateMessageContentNonDisplayOnly_EditsLLMContextOnly", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("user", "edit-original"))
		if err != nil {
			t.Fatalf("AddMessageWithID(user): %v", err)
		}
		if err := ss.AddMessage(tenantID, dummyMsg("assistant", "edit-assistant")); err != nil {
			t.Fatalf("AddMessage(assistant): %v", err)
		}
		// messageIndex is 0-based over the non-display-only messages.
		if err := ss.UpdateMessageContentNonDisplayOnly(tenantID, 0, "edit-updated"); err != nil {
			t.Fatalf("UpdateMessageContentNonDisplayOnly: %v", err)
		}
		replay, err := ss.Replay(tenantID)
		if err != nil {
			t.Fatalf("Replay: %v", err)
		}
		if len(replay.Messages) == 0 || replay.Messages[0].ID != msgID || replay.Messages[0].Content != "edit-updated" {
			t.Errorf("replayed first message = %+v, want id %d with content %q", replay.Messages, msgID, "edit-updated")
		}
		// An out-of-range index must be rejected.
		if err := ss.UpdateMessageContentNonDisplayOnly(tenantID, 99, "nope"); err == nil {
			t.Errorf("UpdateMessageContentNonDisplayOnly(index=99) succeeded, want error")
		}
		// The edit is LLM-context only: the append-only/display history keeps
		// the original content.
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		if got := recordContent(records, msgID); got != "edit-original" {
			t.Errorf("display content = %q, want the untouched %q", got, "edit-original")
		}
	})

	t.Run("GetTenantUsageStats_AggregatesIterationHistory", func(t *testing.T) {
		const turnA, turnB = uint64(9041), uint64(9042)
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "usage"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		// Baseline: not every backend's Clear drops iteration_history, so the
		// aggregates are asserted as deltas.
		before, err := ss.GetTenantUsageStats(tenantID, 0)
		if err != nil {
			t.Fatalf("GetTenantUsageStats(baseline): %v", err)
		}
		for _, rec := range []sqlite.IterationRecord{
			{Iteration: 1, Content: "usage-a1", Tokens: 10, InputTokens: 100, CachedTokens: 5, TotalMs: 200, TTFTMs: 50, TPOTMs: 20, TokensPerSec: 30, Model: "m1", SubscriptionID: "s1"},
			{Iteration: 2, Content: "usage-a2", Tokens: 20, InputTokens: 50, TotalMs: 100, TTFTMs: 60, TPOTMs: 25, TokensPerSec: 40, Model: "m1", SubscriptionID: "s1"},
		} {
			if err := ss.AppendIterationHistory(tenantID, msgID, turnA, rec); err != nil {
				t.Fatalf("AppendIterationHistory(turn %d iter %d): %v", turnA, rec.Iteration, err)
			}
		}
		if err := ss.AppendIterationHistory(tenantID, msgID, turnB, sqlite.IterationRecord{
			Iteration: 1, Content: "usage-b1", Tokens: 7, InputTokens: 30, Model: "m2", SubscriptionID: "s2",
		}); err != nil {
			t.Fatalf("AppendIterationHistory(turn %d): %v", turnB, err)
		}
		after, err := ss.GetTenantUsageStats(tenantID, 2)
		if err != nil {
			t.Fatalf("GetTenantUsageStats: %v", err)
		}
		for _, tc := range []struct {
			name      string
			got, want int64
			wantDelta int64
		}{
			{"iteration_count", after.IterationCount, before.IterationCount, 3},
			{"turn_count", after.TurnCount, before.TurnCount, 2},
			{"input_tokens", after.InputTokens, before.InputTokens, 180},
			{"output_tokens", after.OutputTokens, before.OutputTokens, 37},
			{"cached_tokens", after.CachedTokens, before.CachedTokens, 5},
			{"llm_total_ms", after.LLMTotalMs, before.LLMTotalMs, 300},
		} {
			if got := tc.got - tc.want; got != tc.wantDelta {
				t.Errorf("%s delta = %d, want %d (before=%d after=%d)", tc.name, got, tc.wantDelta, tc.want, tc.got)
			}
		}
		// recentLimit caps the RecentIterations detail rows, returned in
		// chronological order (oldest → newest).
		if len(after.RecentIterations) != 2 {
			t.Fatalf("RecentIterations = %d rows (recentLimit=2), want 2: %+v", len(after.RecentIterations), after.RecentIterations)
		}
		newest := after.RecentIterations[len(after.RecentIterations)-1]
		if newest.TurnID != turnB || newest.Iteration != 1 {
			t.Errorf("newest recent iteration = {turn %d iter %d}, want {turn %d iter 1}",
				newest.TurnID, newest.Iteration, turnB)
		}
		// All three rows carry a model, so the per-(subscription, model)
		// breakdown must attribute them (m1: 2 iterations / 150 input tokens).
		var modelM1, modelM2 *sqlite.UsageModelRow
		for i := range after.ByModel {
			switch after.ByModel[i].Model {
			case "m1":
				modelM1 = &after.ByModel[i]
			case "m2":
				modelM2 = &after.ByModel[i]
			}
		}
		if modelM1 == nil {
			t.Fatalf("ByModel is missing the m1 breakdown: %+v", after.ByModel)
		}
		if modelM1.Iterations != 2 || modelM1.Turns != 1 || modelM1.InputTokens != 150 || modelM1.OutputTokens != 30 {
			t.Errorf("m1 breakdown = %+v, want 2 iterations / 1 turn / 150 input / 30 output", *modelM1)
		}
		if modelM2 == nil {
			t.Fatalf("ByModel is missing the m2 breakdown: %+v", after.ByModel)
		}
		if modelM2.Iterations != 1 || modelM2.InputTokens != 30 || modelM2.OutputTokens != 7 {
			t.Errorf("m2 breakdown = %+v, want 1 iteration / 30 input / 7 output", *modelM2)
		}
	})

	t.Run("GetAllIterationHistory_ReturnsEveryTurn", func(t *testing.T) {
		const turnA, turnB = uint64(9051), uint64(9052)
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		msgID, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "all-iters"))
		if err != nil {
			t.Fatalf("AddMessageWithID: %v", err)
		}
		if err := ss.AppendIterationHistory(tenantID, msgID, turnA, sqlite.IterationRecord{Iteration: 1, Content: "all-iters-A"}); err != nil {
			t.Fatalf("AppendIterationHistory(turn %d): %v", turnA, err)
		}
		if err := ss.AppendIterationHistory(tenantID, msgID, turnB, sqlite.IterationRecord{Iteration: 1, Content: "all-iters-B"}); err != nil {
			t.Fatalf("AppendIterationHistory(turn %d): %v", turnB, err)
		}
		recs, err := ss.GetAllIterationHistory(tenantID)
		if err != nil {
			t.Fatalf("GetAllIterationHistory: %v", err)
		}
		indexA, indexB := -1, -1
		for i := range recs {
			switch recs[i].Content {
			case "all-iters-A":
				indexA = i
			case "all-iters-B":
				indexB = i
			}
		}
		if indexA < 0 || indexB < 0 {
			t.Fatalf("GetAllIterationHistory is missing a turn (A=%d B=%d): %+v", indexA, indexB, recs)
		}
		// Ordered by (turn_id, iteration): turnA (9051) precedes turnB (9052).
		if indexA > indexB {
			t.Errorf("iteration order A=%d B=%d, want the lower turn id first", indexA, indexB)
		}
	})

	t.Run("RewindToHistoryID_TruncatesAtUserMessage", func(t *testing.T) {
		ss := getSessionStore(t, s, tenantID)
		if err := ss.Clear(tenantID); err != nil {
			t.Fatalf("Clear: %v", err)
		}
		u1, err := ss.AddMessageWithID(tenantID, dummyMsg("user", "rw-u1"))
		if err != nil {
			t.Fatalf("AddMessageWithID(u1): %v", err)
		}
		a1, err := ss.AddMessageWithID(tenantID, dummyMsg("assistant", "rw-a1"))
		if err != nil {
			t.Fatalf("AddMessageWithID(a1): %v", err)
		}
		// Stamp u1's prompt-token watermark: it becomes the restored watermark
		// once the rewind drops the newer user row (u2).
		if err := ss.UpdateUserMessageContextTokens(tenantID, 456); err != nil {
			t.Fatalf("UpdateUserMessageContextTokens: %v", err)
		}
		u2msg := dummyMsg("user", "rw-u2")
		u2msg.TurnID = 2
		u2, err := ss.AddMessageWithID(tenantID, u2msg)
		if err != nil {
			t.Fatalf("AddMessageWithID(u2): %v", err)
		}
		a2msg := dummyMsg("assistant", "rw-a2")
		a2msg.TurnID = 2
		a2, err := ss.AddMessageWithID(tenantID, a2msg)
		if err != nil {
			t.Fatalf("AddMessageWithID(a2): %v", err)
		}
		// A turn-2 iteration record so the rewind also exercises the
		// iteration_history truncation branch.
		if err := ss.AppendIterationHistory(tenantID, a2, 2, sqlite.IterationRecord{Iteration: 1, Content: "rw-iter-turn2"}); err != nil {
			t.Fatalf("AppendIterationHistory(turn 2): %v", err)
		}
		// Only a non-display-only user message row is a valid rewind target.
		if _, _, _, err := ss.RewindToHistoryID(tenantID, 0); err == nil {
			t.Errorf("RewindToHistoryID(0) succeeded, want error")
		}
		if _, _, _, err := ss.RewindToHistoryID(tenantID, a1); err == nil {
			t.Errorf("RewindToHistoryID(assistant row) succeeded, want error")
		}
		if _, _, _, err := ss.RewindToHistoryID(tenantID, 999999999); err == nil {
			t.Errorf("RewindToHistoryID(unknown history_id) succeeded, want error")
		}
		target, turnIdx, promptTokens, err := ss.RewindToHistoryID(tenantID, u2)
		if err != nil {
			t.Fatalf("RewindToHistoryID: %v", err)
		}
		if target.ID != u2 || target.Role != "user" || target.Content != "rw-u2" {
			t.Errorf("rewind target = %+v, want the u2 user row (id %d)", target, u2)
		}
		if turnIdx != 2 {
			t.Errorf("rewind turn index = %d, want 2 (u1 and u2 are the first two user rows)", turnIdx)
		}
		if promptTokens != 456 {
			t.Errorf("rewind prompt tokens = %d, want the surviving u1 watermark 456", promptTokens)
		}
		records, err := ss.GetFullHistory(tenantID)
		if err != nil {
			t.Fatalf("GetFullHistory: %v", err)
		}
		for _, rec := range records {
			if rec.HistoryID >= u2 {
				t.Errorf("history row %d survived a rewind that truncated at %d", rec.HistoryID, u2)
			}
		}
		if len(records) != 2 {
			t.Fatalf("after rewind %d history rows remain, want 2 (u1 + a1): %+v", len(records), records)
		}
		if got := recordContent(records, u1); got != "rw-u1" {
			t.Errorf("surviving u1 content = %q, want %q", got, "rw-u1")
		}
		iters, err := ss.GetIterationHistoryByTurn(tenantID, 2)
		if err != nil {
			t.Fatalf("GetIterationHistoryByTurn: %v", err)
		}
		if len(iters) != 0 {
			t.Errorf("turn 2 iteration_history rows survived the rewind: %+v", iters)
		}
	})
}

// findIterationByContent returns a pointer to the first iteration record whose
// Content matches, or nil. Subtests share one tenant, so iteration-history
// subtests use turn ids unique per subtest rather than asserting an exact
// record count (not every backend's Clear drops iteration_history).
func findIterationByContent(recs []sqlite.IterationRecord, content string) *sqlite.IterationRecord {
	for i := range recs {
		if recs[i].Content == content {
			return &recs[i]
		}
	}
	return nil
}
