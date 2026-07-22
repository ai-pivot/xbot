package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"xbot/llm"
)

func newHistoryTestService(t *testing.T) (*DB, *SessionService, int64) {
	t.Helper()
	db, err := Open(t.TempDir() + "/history.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	tenantID, err := NewTenantService(db).GetOrCreateTenantID("test", "chat")
	if err != nil {
		t.Fatal(err)
	}
	return db, NewSessionService(db), tenantID
}

func newCrossHandleHistoryServices(t *testing.T) (*DB, *SessionService, *DB, *SessionService, int64) {
	t.Helper()
	path := t.TempDir() + "/shared-history.db"
	dbA, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbA.Close() })
	dbB, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbB.Close() })
	tenantID, err := NewTenantService(dbA).GetOrCreateTenantID("test", "chat")
	if err != nil {
		t.Fatal(err)
	}
	return dbA, NewSessionService(dbA), dbB, NewSessionService(dbB), tenantID
}

// holdCrossHandleRewindDelete holds the destructive phase of a rewind open on
// one DB handle, so another handle can attempt a stale semantic append while
// the deletion is still uncommitted.
func holdCrossHandleRewindDelete(t *testing.T, svc *SessionService, tenantID, historyID int64) (func(), <-chan error) {
	t.Helper()
	ready := make(chan error, 1)
	releaseCh := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- svc.withImmediateHistoryWrite(func(store historyQueryExecer) error {
			result, err := store.Exec(`DELETE FROM session_messages WHERE tenant_id = ? AND id >= ?`, tenantID, historyID)
			if err != nil {
				ready <- err
				return err
			}
			rows, err := result.RowsAffected()
			if err != nil {
				ready <- err
				return err
			}
			if rows == 0 {
				err := fmt.Errorf("held rewind deleted no rows")
				ready <- err
				return err
			}
			ready <- nil
			<-releaseCh
			return nil
		})
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	return func() { once.Do(func() { close(releaseCh) }) }, done
}

func waitHistoryOperation(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("history operation did not finish")
		return nil
	}
}

func TestFullHistoryCompressionMetadataAndCrossBoundaryRewind(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	userID, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "original", Timestamp: time.Unix(100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	answerID, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "answer", Timestamp: time.Unix(101, 0)})
	if err != nil {
		t.Fatal(err)
	}
	compressID, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{{Role: "user", Content: "[Compacted context]\nsummary"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.NewUserMessage("later")); err != nil {
		t.Fatal(err)
	}

	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 4 || records[0].CompactedBy != compressID || records[1].CompactedBy != compressID {
		t.Fatalf("compression annotations=%+v", records)
	}
	rng := records[2].Compression
	if rng == nil || rng.StartHistoryID != userID || rng.EndHistoryID != answerID || len(rng.SourceHistoryIDs) != 2 {
		t.Fatalf("compression range=%+v", rng)
	}

	target, turnIdx, err := svc.RewindToHistoryID(tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if target.Content != "original" || turnIdx != 1 {
		t.Fatalf("target=%+v turn=%d", target, turnIdx)
	}
	remaining, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("cross-boundary rewind left records: %+v", remaining)
	}
	replay, err := svc.Replay(tenantID)
	if err != nil || len(replay.Messages) != 0 || replay.PendingAskUser != nil {
		t.Fatalf("replay after rewind=%+v err=%v", replay, err)
	}
}

func TestFullHistoryCompressionSourcesExcludeHiddenPruneControls(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	if _, err := svc.AppendMessage(tenantID, llm.NewUserMessage("old user")); err != nil {
		t.Fatal(err)
	}
	answerID, err := svc.AppendMessage(tenantID, llm.NewAssistantMessage("old answer"))
	if err != nil {
		t.Fatal(err)
	}
	pruneID, err := svc.AppendContextSnapshot(tenantID, HistoryRecordPrune, []llm.ChatMessage{
		{Role: "assistant", Content: "truncation notice"},
		{HistoryID: answerID, Role: "assistant", Content: "old answer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	laterID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("later"))
	if err != nil {
		t.Fatal(err)
	}
	compressID, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{HistoryID: laterID, Role: "user", Content: "later"},
	})
	if err != nil {
		t.Fatal(err)
	}

	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var compression *CompressionRange
	publicIDs := make(map[int64]bool)
	for _, record := range records {
		if record.Type == HistoryRecordMessage || record.Type == HistoryRecordCompress {
			publicIDs[record.HistoryID] = true
		}
		if record.HistoryID == compressID {
			compression = record.Compression
		}
	}
	if compression == nil {
		t.Fatalf("compression %d has no source metadata: %+v", compressID, records)
	}
	if len(compression.SourceHistoryIDs) != 1 || compression.SourceHistoryIDs[0] != answerID {
		t.Fatalf("compression sources=%v, want public answer %d only (hidden prune=%d)", compression.SourceHistoryIDs, answerID, pruneID)
	}
	for _, sourceID := range compression.SourceHistoryIDs {
		if !publicIDs[sourceID] {
			t.Fatalf("compression source %d has no public history row", sourceID)
		}
	}
}

func TestRewindAtomicallyRestoresTokenState(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	previousID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("previous"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`UPDATE session_messages SET context_tokens = 321 WHERE id = ?`, previousID); err != nil {
		t.Fatal(err)
	}
	targetID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("rewrite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.NewAssistantMessage("future")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`
		INSERT INTO tenant_state (tenant_id, last_consolidated, last_prompt_tokens, last_completion_tokens)
		VALUES (?, 7, 999, 88)
	`, tenantID); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.RewindToHistoryID(tenantID, targetID); err != nil {
		t.Fatal(err)
	}
	var promptTokens, completionTokens, lastConsolidated int64
	if err := db.Conn().QueryRow(`
		SELECT last_prompt_tokens, last_completion_tokens, last_consolidated
		FROM tenant_state WHERE tenant_id = ?
	`, tenantID).Scan(&promptTokens, &completionTokens, &lastConsolidated); err != nil {
		t.Fatal(err)
	}
	if promptTokens != 321 || completionTokens != 0 || lastConsolidated != 7 {
		t.Fatalf("token state=(%d,%d) consolidated=%d, want (321,0) consolidated=7", promptTokens, completionTokens, lastConsolidated)
	}
}

func TestRewindRollsBackHistoryWhenTokenStateUpdateFails(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	if _, err := svc.AppendMessage(tenantID, llm.NewUserMessage("previous")); err != nil {
		t.Fatal(err)
	}
	targetID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("rewrite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.NewAssistantMessage("future")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`
		INSERT INTO tenant_state (tenant_id, last_consolidated, last_prompt_tokens, last_completion_tokens)
		VALUES (?, 0, 999, 88)
	`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`
		CREATE TRIGGER fail_rewind_token_state
		BEFORE UPDATE OF last_prompt_tokens ON tenant_state
		BEGIN SELECT RAISE(ABORT, 'injected token state failure'); END
	`); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.RewindToHistoryID(tenantID, targetID); err == nil || !strings.Contains(err.Error(), "injected token state failure") {
		t.Fatalf("rewind error=%v", err)
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 3 || records[1].HistoryID != targetID || records[2].Message.Content != "future" {
		t.Fatalf("failed token-state update truncated history: %+v", records)
	}
	var promptTokens, completionTokens int64
	if err := db.Conn().QueryRow(`
		SELECT last_prompt_tokens, last_completion_tokens FROM tenant_state WHERE tenant_id = ?
	`, tenantID).Scan(&promptTokens, &completionTokens); err != nil {
		t.Fatal(err)
	}
	if promptTokens != 999 || completionTokens != 88 {
		t.Fatalf("failed rewind changed token state to (%d,%d)", promptTokens, completionTokens)
	}
}

func TestRewindInvalidTargetIsFailClosed(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	if _, err := svc.AppendMessage(tenantID, llm.NewUserMessage("one")); err != nil {
		t.Fatal(err)
	}
	assistantID, err := svc.AppendMessage(tenantID, llm.NewAssistantMessage("answer"))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := svc.GetFullHistory(tenantID)
	if _, _, err := svc.RewindToHistoryID(tenantID, assistantID); err == nil || !strings.Contains(err.Error(), "not a rewindable user") {
		t.Fatalf("expected invalid target error, got %v", err)
	}
	after, _ := svc.GetFullHistory(tenantID)
	if len(after) != len(before) {
		t.Fatalf("invalid target modified history: before=%d after=%d", len(before), len(after))
	}
	otherTenant, err := NewTenantService(db).GetOrCreateTenantID("test", "other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.RewindToHistoryID(otherTenant, before[0].HistoryID); err == nil {
		t.Fatal("cross-tenant history_id unexpectedly rewound")
	}
}

func TestRewindTransactionRollsBackOnDeleteFailure(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	targetID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("rewrite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.NewAssistantMessage("future")); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`
		CREATE TRIGGER fail_history_rewind BEFORE DELETE ON session_messages
		BEGIN SELECT RAISE(ABORT, 'injected rewind failure'); END;
	`); err != nil {
		t.Fatal(err)
	}

	if _, _, err := svc.RewindToHistoryID(tenantID, targetID); err == nil || !strings.Contains(err.Error(), "injected rewind failure") {
		t.Fatalf("rewind error=%v", err)
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].HistoryID != targetID || records[1].Message.Content != "future" {
		t.Fatalf("failed rewind changed history: %+v", records)
	}
}

func TestRewindSerializesWithConcurrentAppend(t *testing.T) {
	db, svc, _ := newHistoryTestService(t)
	tenants := NewTenantService(db)
	for i := 0; i < 24; i++ {
		tenantID, err := tenants.GetOrCreateTenantID("test", fmt.Sprintf("rewind-race-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		targetID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("rewrite"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.AppendMessage(tenantID, llm.NewAssistantMessage("future")); err != nil {
			t.Fatal(err)
		}

		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			<-start
			_, _, err := svc.RewindToHistoryID(tenantID, targetID)
			errs <- err
		}()
		go func() {
			<-start
			_, err := svc.AppendMessage(tenantID, llm.NewUserMessage("concurrent"))
			errs <- err
		}()
		close(start)
		if first, second := <-errs, <-errs; first != nil || second != nil {
			t.Fatalf("iteration %d concurrent rewind/append errors: %v / %v", i, first, second)
		}

		records, err := svc.GetFullHistory(tenantID)
		if err != nil {
			t.Fatal(err)
		}
		if len(records) > 1 || (len(records) == 1 && records[0].Message.Content != "concurrent") {
			t.Fatalf("iteration %d non-serial history: %+v", i, records)
		}
		if _, err := svc.Replay(tenantID); err != nil {
			t.Fatalf("iteration %d replay after concurrent rewind: %v", i, err)
		}
	}
}

func TestHistoryAppendIDsAreStableAndMonotonicUnderConcurrency(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const count = 32
	ids := make([]int64, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id, err := svc.AppendMessage(tenantID, llm.NewUserMessage(fmt.Sprintf("m%d", i)))
			if err != nil {
				t.Errorf("append %d: %v", i, err)
				return
			}
			ids[i] = id
		}(i)
	}
	wg.Wait()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("history IDs not strictly monotonic: %v", ids)
		}
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != count {
		t.Fatalf("records=%d want %d", len(records), count)
	}
	for i, record := range records {
		if record.HistoryID != ids[i] {
			t.Fatalf("record %d history_id=%d want %d", i, record.HistoryID, ids[i])
		}
	}
}

func TestReplayPreservesRawToolResultAcrossMaskAndRestart(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	toolID, err := svc.AppendMessage(tenantID, llm.NewToolMessage("Shell", "call-1", `{}`, "raw secret output"))
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.AppendMasks(tenantID, []MaskMutation{{TargetHistoryID: toolID, Content: "[masked]"}}); err != nil {
		t.Fatal(err)
	}

	replayed, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got := replayed.Messages[0].Content; got != "[masked]" {
		t.Fatalf("active content=%q", got)
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got := records[0].Message.Content; got != "raw secret output" {
		t.Fatalf("raw history overwritten: %q", got)
	}

	path := db.path
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	replayed, err = NewSessionService(reopened).Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if got := replayed.Messages[0].Content; got != "[masked]" {
		t.Fatalf("restart content=%q", got)
	}
}

func TestReplayCompressionContextEditAndAskUser(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	userID, _ := svc.AppendMessage(tenantID, llm.NewUserMessage("old user"))
	answerID, _ := svc.AppendMessage(tenantID, llm.NewAssistantMessage("old answer"))
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "summary"},
		{HistoryID: answerID, Role: "assistant", Content: "old answer"},
	}); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Messages) != 2 || replay.Messages[0].Content != "summary" || replay.Messages[1].HistoryID != answerID {
		t.Fatalf("compressed replay=%+v", replay.Messages)
	}

	summary := replay.Messages[0]
	summary.Content = "edited summary"
	if _, err := svc.AppendControl(tenantID, HistoryRecordContextEdit, summary.HistoryID,
		MessageMutations{Mutations: []MessageMutation{{TargetHistoryID: summary.HistoryID, Message: summary}}}); err != nil {
		t.Fatal(err)
	}
	assistantID, _ := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}})
	_ = assistantID
	toolID, _ := svc.AppendMessage(tenantID, llm.NewToolMessage("AskUser", "ask", `{}`, "waiting"))
	questionID, err := svc.AppendAskQuestion(tenantID, map[string]string{"request_id": "r1", "ask_questions": `[{"question":"Continue?"}]`})
	if err != nil {
		t.Fatal(err)
	}
	replay, err = svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.PendingAskUser == nil || replay.PendingAskUser.HistoryID != questionID {
		t.Fatalf("pending=%+v", replay.PendingAskUser)
	}
	if _, err := svc.AppendAskAnswer(tenantID, "yes"); err != nil {
		t.Fatal(err)
	}
	replay, err = svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.PendingAskUser != nil {
		t.Fatalf("pending answer survived: %+v", replay.PendingAskUser)
	}
	idx := activeMessageIndex(replay.Messages, toolID)
	if idx < 0 || replay.Messages[idx].Content != "yes" {
		t.Fatalf("AskUser answer not replayed: %+v", replay.Messages)
	}
	if _, err := svc.AppendAskAnswer(tenantID, "late"); err == nil || !strings.Contains(err.Error(), "no longer pending") {
		t.Fatalf("late answer error=%v", err)
	}

	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].HistoryID != userID || records[0].Message.Content != "old user" {
		t.Fatalf("compression changed baseline: %+v", records[0])
	}
}

func TestReplayRejectsAskAnswerForDifferentToolTarget(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "AskUser", Arguments: `{}`}}}); err != nil {
		t.Fatal(err)
	}
	firstToolID, err := svc.AppendMessage(tenantID, llm.NewToolMessage("AskUser", "ask-1", `{}`, "waiting one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask-2", Name: "AskUser", Arguments: `{}`}}}); err != nil {
		t.Fatal(err)
	}
	secondToolID, err := svc.AppendMessage(tenantID, llm.NewToolMessage("AskUser", "ask-2", `{}`, "waiting two"))
	if err != nil {
		t.Fatal(err)
	}
	questionID, err := svc.AppendAskQuestion(tenantID, map[string]string{"request_id": "r2"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(AskAnswerRecord{Answer: "wrong target", ToolHistoryID: firstToolID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Conn().Exec(`
		INSERT INTO session_messages
			(tenant_id, role, content, display_only, record_type, target_history_id, record_data)
		VALUES (?, 'control', '', 1, 'ask_answer', ?, ?)
	`, tenantID, questionID, string(payload)); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Replay(tenantID); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("does not match pending target %d", secondToolID)) {
		t.Fatalf("mismatched AskAnswer replay error=%v", err)
	}
}

func TestReplayRejectsCorruptControlWithHistoryID(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	result, err := db.Conn().Exec(`INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, record_data)
		VALUES (?, 'control', '', 1, 'future_control', '{}')`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	historyID, _ := result.LastInsertId()
	_, err = svc.Replay(tenantID)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("history_id %d", historyID)) {
		t.Fatalf("replay error=%v, want history_id %d", err, historyID)
	}
}

func TestReplayRejectsSnapshotFutureReference(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	const futureID = int64(1000)
	payload := fmt.Sprintf(`{"messages":[{"role":"user","content":"from future"}],"history_ids":[%d]}`, futureID)
	result, err := db.Conn().Exec(`INSERT INTO session_messages
		(tenant_id, role, content, display_only, record_type, record_data)
		VALUES (?, 'control', '', 1, 'compress', ?)`, tenantID, payload)
	if err != nil {
		t.Fatal(err)
	}
	controlID, _ := result.LastInsertId()
	if _, err := db.Conn().Exec(`INSERT INTO session_messages
		(id, tenant_id, role, content, display_only, record_type) VALUES (?, ?, 'user', 'future', 0, 'message')`, futureID, tenantID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Replay(tenantID)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("history_id %d", controlID)) || !strings.Contains(err.Error(), "unknown history_id") {
		t.Fatalf("future reference replay error=%v", err)
	}
}

func TestReplayRejectsStructurallyInvalidControlData(t *testing.T) {
	for _, recordType := range []HistoryRecordType{
		HistoryRecordCompress, HistoryRecordPrune, HistoryRecordContextEdit,
		HistoryRecordMask, HistoryRecordAskQuestion, HistoryRecordAskAnswer,
	} {
		t.Run(string(recordType), func(t *testing.T) {
			db, svc, tenantID := newHistoryTestService(t)
			result, err := db.Conn().Exec(`INSERT INTO session_messages
				(tenant_id, role, content, display_only, record_type, record_data)
				VALUES (?, 'control', '', 1, ?, '{}')`, tenantID, recordType)
			if err != nil {
				t.Fatal(err)
			}
			historyID, _ := result.LastInsertId()
			_, err = svc.Replay(tenantID)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("history_id %d", historyID)) {
				t.Fatalf("invalid %s replay error=%v", recordType, err)
			}
		})
	}
}

func TestReplayTargetsSyntheticSnapshotMessageByOccurrence(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	if _, err := svc.AppendMessage(tenantID, llm.NewUserMessage("baseline")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "summary one"}, {Role: "assistant", Content: "summary two"},
	}); err != nil {
		t.Fatal(err)
	}
	replay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	target := replay.Messages[0]
	target.Content = "edited first"
	if _, err := svc.AppendControl(tenantID, HistoryRecordContextEdit, target.HistoryID, MessageMutations{Mutations: []MessageMutation{
		{TargetHistoryID: target.HistoryID, TargetOccurrence: 0, Message: target},
	}}); err != nil {
		t.Fatal(err)
	}
	replay, err = svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Messages[0].Content != "edited first" || replay.Messages[1].Content != "summary two" {
		t.Fatalf("occurrence replay=%+v", replay.Messages)
	}
}

func TestAppendAskAnswerConcurrentLateAnswerRejected(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	_, _ = svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}})
	_, _ = svc.AppendMessage(tenantID, llm.NewToolMessage("AskUser", "ask", `{}`, "waiting"))
	if _, err := svc.AppendAskQuestion(tenantID, map[string]string{"request_id": "r"}); err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for _, answer := range []string{"one", "two"} {
		go func(answer string) { _, err := svc.AppendAskAnswer(tenantID, answer); errs <- err }(answer)
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-errs == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful answers=%d want 1", successes)
	}
	if _, err := svc.Replay(tenantID); err != nil {
		t.Fatalf("concurrent answer corrupted replay: %v", err)
	}
}

func TestAppendAskQuestionConcurrentDuplicateRejected(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	_, _ = svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask", Name: "AskUser", Arguments: `{}`}}})
	_, _ = svc.AppendMessage(tenantID, llm.NewToolMessage("AskUser", "ask", `{}`, "waiting"))
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			_, err := svc.AppendAskQuestion(tenantID, map[string]string{"request_id": fmt.Sprint(i)})
			errs <- err
		}(i)
	}
	successes := 0
	for i := 0; i < 2; i++ {
		if <-errs == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful questions=%d want 1", successes)
	}
	if _, err := svc.Replay(tenantID); err != nil {
		t.Fatalf("concurrent question corrupted replay: %v", err)
	}
}

func TestMigrationV47KeepsExistingRowsAsBaseline(t *testing.T) {
	path := t.TempDir() + "/v46.db"
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(`
		CREATE TABLE tenants (id INTEGER PRIMARY KEY AUTOINCREMENT, channel TEXT NOT NULL, chat_id TEXT NOT NULL, UNIQUE(channel, chat_id));
		INSERT INTO tenants(id, channel, chat_id) VALUES (1, 'test', 'chat');
		CREATE TABLE session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT, tenant_id INTEGER NOT NULL, role TEXT NOT NULL,
			content TEXT NOT NULL, tool_call_id TEXT, tool_name TEXT, tool_arguments TEXT,
			tool_calls TEXT, detail TEXT, display_only INTEGER DEFAULT 0,
			reasoning_content TEXT DEFAULT '', context_tokens INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO session_messages(id, tenant_id, role, content) VALUES (7, 1, 'user', 'survives');
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version(version) VALUES (46);
	`)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	records, err := NewSessionService(db).GetFullHistory(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].HistoryID != 7 || records[0].Type != HistoryRecordMessage || records[0].Message.Content != "survives" {
		t.Fatalf("migrated baseline=%+v", records)
	}
}

func TestAppendMasksIsAtomicWhenTargetMissing(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	id, _ := svc.AppendMessage(tenantID, llm.NewToolMessage("Shell", "1", `{}`, "raw"))
	err := svc.AppendMasks(tenantID, []MaskMutation{{TargetHistoryID: id, Content: "masked"}, {TargetHistoryID: id + 999, Content: "bad"}})
	if err == nil {
		t.Fatal("expected invalid mask target error")
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("partial mask append: %+v", records)
	}
}

func TestAppendContextSnapshotRejectsUnknownOrInactiveHistoryID(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	first, _ := svc.AppendMessage(tenantID, llm.NewUserMessage("first"))
	second, _ := svc.AppendMessage(tenantID, llm.NewUserMessage("second"))
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordPrune, []llm.ChatMessage{
		{HistoryID: second + 999, Role: "user", Content: "unknown"},
	}); err == nil {
		t.Fatal("snapshot accepted unknown history ID")
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordPrune, []llm.ChatMessage{
		{HistoryID: second, Role: "user", Content: "second"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordPrune, []llm.ChatMessage{
		{HistoryID: first, Role: "user", Content: "stale"},
	}); err == nil {
		t.Fatal("snapshot accepted inactive history ID")
	}
	if _, err := svc.Replay(tenantID); err != nil {
		t.Fatalf("replay corrupted after rejected snapshots: %v", err)
	}
}

func TestConcurrentSnapshotAndMutationsRemainReplayable(t *testing.T) {
	db, _, _ := newHistoryTestService(t)
	const iterations = 40
	for i := 0; i < iterations; i++ {
		tenantID, err := NewTenantService(db).GetOrCreateTenantID("race", fmt.Sprintf("chat-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		writerA, writerB := NewSessionService(db), NewSessionService(db)
		oldID, _ := writerA.AppendMessage(tenantID, llm.NewUserMessage("old"))
		keepID, _ := writerA.AppendMessage(tenantID, llm.NewUserMessage("keep"))
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func(iteration int) {
			<-start
			if iteration%2 == 0 {
				_, err := writerA.AppendControl(tenantID, HistoryRecordContextEdit, oldID, MessageMutations{Mutations: []MessageMutation{{TargetHistoryID: oldID, Message: llm.ChatMessage{Role: "user", Content: "edited"}}}})
				errs <- err
				return
			}
			errs <- writerA.AppendMasks(tenantID, []MaskMutation{{TargetHistoryID: oldID, Content: "masked"}})
		}(i)
		go func() {
			<-start
			_, err := writerB.AppendContextSnapshot(tenantID, HistoryRecordPrune, []llm.ChatMessage{{HistoryID: keepID, Role: "user", Content: "keep"}})
			errs <- err
		}()
		close(start)
		firstErr, secondErr := <-errs, <-errs
		if firstErr != nil && secondErr != nil {
			t.Fatalf("iteration %d: both serialized operations failed: %v / %v", i, firstErr, secondErr)
		}
		if _, err := writerA.Replay(tenantID); err != nil {
			t.Fatalf("iteration %d: concurrent append corrupted replay: %v", i, err)
		}
	}
}

func TestCrossHandleRewindCannotBeFollowedByStaleContextEdit(t *testing.T) {
	_, writer, _, rewinder, tenantID := newCrossHandleHistoryServices(t)
	targetID, err := writer.AppendMessage(tenantID, llm.NewUserMessage("remove me"))
	if err != nil {
		t.Fatal(err)
	}

	release, rewindDone := holdCrossHandleRewindDelete(t, rewinder, tenantID, targetID)
	defer release()
	started := make(chan struct{})
	appendDone := make(chan error, 1)
	go func() {
		close(started)
		_, err := writer.AppendControl(tenantID, HistoryRecordContextEdit, targetID, MessageMutations{Mutations: []MessageMutation{{
			TargetHistoryID: targetID,
			Message:         llm.ChatMessage{Role: "user", Content: "stale edit"},
		}}})
		appendDone <- err
	}()
	<-started
	// Give the competing handle time to reach the database lock. With a
	// deferred transaction this is enough to read the old committed target.
	time.Sleep(100 * time.Millisecond)
	release()
	if err := waitHistoryOperation(t, rewindDone); err != nil {
		t.Fatalf("held rewind: %v", err)
	}
	if err := waitHistoryOperation(t, appendDone); err == nil {
		t.Fatal("stale context edit succeeded after cross-handle rewind")
	}

	records, err := writer.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("stale control survived rewind: %+v", records)
	}
	replay, err := writer.Replay(tenantID)
	if err != nil || len(replay.Messages) != 0 {
		t.Fatalf("rewound history was revived: replay=%+v err=%v", replay, err)
	}
}

func TestCrossHandleRewindCannotBeFollowedByStaleCheckpoint(t *testing.T) {
	_, writer, _, rewinder, tenantID := newCrossHandleHistoryServices(t)
	targetID, err := writer.AppendMessage(tenantID, llm.NewUserMessage("remove me"))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := writer.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}

	release, rewindDone := holdCrossHandleRewindDelete(t, rewinder, tenantID, targetID)
	defer release()
	started := make(chan struct{})
	appendDone := make(chan error, 1)
	go func() {
		close(started)
		_, err := writer.AppendContextSnapshot(tenantID, HistoryRecordPrune, stale.Messages)
		appendDone <- err
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	release()
	if err := waitHistoryOperation(t, rewindDone); err != nil {
		t.Fatalf("held rewind: %v", err)
	}
	if err := waitHistoryOperation(t, appendDone); err == nil {
		t.Fatal("stale checkpoint succeeded after cross-handle rewind")
	}

	records, err := writer.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("stale checkpoint survived rewind: %+v", records)
	}
	replay, err := writer.Replay(tenantID)
	if err != nil || len(replay.Messages) != 0 {
		t.Fatalf("checkpoint revived rewound history: replay=%+v err=%v", replay, err)
	}
}

func TestCrossHandleControlFailureRollsBackTriggerWrites(t *testing.T) {
	_, writer, triggerDB, _, tenantID := newCrossHandleHistoryServices(t)
	targetID, err := writer.AppendMessage(tenantID, llm.NewUserMessage("original"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := triggerDB.Conn().Exec(`
		CREATE TRIGGER fail_context_edit BEFORE INSERT ON session_messages
		WHEN NEW.record_type = 'context_edit'
		BEGIN
			UPDATE session_messages SET content = 'trigger mutation'
			WHERE tenant_id = NEW.tenant_id AND id = NEW.target_history_id;
			SELECT RAISE(FAIL, 'injected context edit failure');
		END;
	`); err != nil {
		t.Fatal(err)
	}

	_, err = writer.AppendControl(tenantID, HistoryRecordContextEdit, targetID, MessageMutations{Mutations: []MessageMutation{{
		TargetHistoryID: targetID,
		Message:         llm.ChatMessage{Role: "user", Content: "edited"},
	}}})
	if err == nil || !strings.Contains(err.Error(), "injected context edit failure") {
		t.Fatalf("context edit error=%v", err)
	}
	records, err := writer.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Message.Content != "original" {
		t.Fatalf("failed control transaction was not fully rolled back: %+v", records)
	}
}

func TestAppendMessagesAndAskQuestionRollsBackAsOneUnit(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	messages := []llm.ChatMessage{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "AskUser", Arguments: `{}`}}},
		llm.NewToolMessage("AskUser", "ask-1", `{}`, "waiting"),
	}
	if _, err := db.Conn().Exec(`
		CREATE TRIGGER fail_ask_control BEFORE INSERT ON session_messages
		WHEN NEW.record_type = 'ask_question' BEGIN SELECT RAISE(ABORT, 'injected failure'); END;
	`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.AppendMessagesAndAskQuestion(tenantID, messages, map[string]string{"request_id": "r1"}); err == nil {
		t.Fatal("expected injected AskUser control failure")
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("failed AskUser transaction left records: %+v", records)
	}
	if _, err := db.Conn().Exec(`DROP TRIGGER fail_ask_control`); err != nil {
		t.Fatal(err)
	}
	ids, questionID, err := svc.AppendMessagesAndAskQuestion(tenantID, messages, map[string]string{"request_id": "r1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == 0 || ids[1] == 0 || questionID == 0 {
		t.Fatalf("AskUser transaction IDs=%v question=%d", ids, questionID)
	}
	replay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Messages) != 2 || replay.PendingAskUser == nil || replay.PendingAskUser.HistoryID != questionID || replay.PendingAskUser.ToolHistoryID != ids[1] {
		t.Fatalf("AskUser replay=%+v", replay)
	}
}

func TestVersionedCheckpointRestoresPendingAskUserFromSuffix(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	baselineID, err := svc.AppendMessage(tenantID, llm.NewUserMessage("baseline"))
	if err != nil {
		t.Fatal(err)
	}
	_, questionID, err := svc.AppendMessagesAndAskQuestion(tenantID, []llm.ChatMessage{
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "ask-1", Name: "AskUser", Arguments: `{}`}}},
		llm.NewToolMessage("AskUser", "ask-1", `{}`, "waiting"),
	}, map[string]string{"request_id": "r1"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	checkpointID, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, before.Messages)
	if err != nil {
		t.Fatal(err)
	}
	records, err := svc.GetFullHistory(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot ContextSnapshot
	if err := json.Unmarshal(records[len(records)-1].Data, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != contextSnapshotVersion || snapshot.PendingAskUser == nil || snapshot.PendingAskUser.HistoryID != questionID {
		t.Fatalf("checkpoint %d snapshot=%+v", checkpointID, snapshot)
	}
	// A self-contained checkpoint must make corrupt, inactive prefix rows
	// irrelevant to prompt replay.
	if _, err := db.Conn().Exec(`UPDATE session_messages SET record_type = 'future_control' WHERE tenant_id = ? AND id = ?`, tenantID, baselineID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendAskAnswer(tenantID, "yes"); err != nil {
		t.Fatalf("answer through checkpoint suffix: %v", err)
	}
	after, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PendingAskUser != nil || len(after.Messages) != len(before.Messages) || after.Messages[len(after.Messages)-1].Content != "yes" {
		t.Fatalf("checkpoint suffix replay=%+v", after)
	}
}

func TestHistorySemanticLocksAreTenantScoped(t *testing.T) {
	db, svc, tenantA := newHistoryTestService(t)
	tenantB, err := NewTenantService(db).GetOrCreateTenantID("test", "other")
	if err != nil {
		t.Fatal(err)
	}
	lockA := db.historyLock(tenantA)
	lockA.Lock()
	defer lockA.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := svc.AppendMessage(tenantB, llm.NewUserMessage("independent"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tenant B history operation blocked behind tenant A semantic lock")
	}
}

func TestHistorySemanticLocksUseBoundedStripes(t *testing.T) {
	db, _, _ := newHistoryTestService(t)
	locks := make(map[*sync.Mutex]struct{})
	for tenantID := int64(0); tenantID < historyLockStripes*4; tenantID++ {
		locks[db.historyLock(tenantID)] = struct{}{}
	}
	if len(locks) != historyLockStripes {
		t.Fatalf("history lock stripes=%d, want %d", len(locks), historyLockStripes)
	}
	if db.historyLock(1) != db.historyLock(1+historyLockStripes) {
		t.Fatal("tenant IDs in the same stripe did not share the bounded lock")
	}
}
