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
