package sqlite

import (
	"database/sql"
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

func TestResolveRewindTimestampRequiresUniqueUserAndInvalidTargetIsFailClosed(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	stamp := time.Unix(200, 0)
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "one", Timestamp: stamp}); err != nil {
		t.Fatal(err)
	}
	assistantID, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "answer", Timestamp: stamp})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "two", Timestamp: stamp}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveRewindTimestamp(tenantID, stamp); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected timestamp ambiguity, got %v", err)
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

func TestMigrationV46KeepsExistingRowsAsBaseline(t *testing.T) {
	path := t.TempDir() + "/v45.db"
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
		INSERT INTO schema_version(version) VALUES (45);
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
