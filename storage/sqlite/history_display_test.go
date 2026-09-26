package sqlite

import (
	"fmt"
	"strings"
	"testing"

	"xbot/llm"
)

// TestReplayForDisplayKeepsPreCompressionMessages verifies that the display
// replay shows ALL messages (including pre-compression), unlike Replay()
// which replaces them with the compress snapshot.
//
// Bug: web frontend uses Replay() for history display, which hides
// pre-compression messages even though they're still in the DB
// (session_messages is append-only).
func TestReplayForDisplayKeepsPreCompressionMessages(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Pre-compression messages.
	user1ID, _ := svc.AppendMessage(tenantID, llm.NewUserMessage("old user 1"))
	_ = user1ID
	svc.AppendMessage(tenantID, llm.NewAssistantMessage("old answer 1"))
	svc.AppendMessage(tenantID, llm.NewUserMessage("old user 2"))

	// Compress record — replaces all prior messages with a summary.
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary of old conversation"},
		{Role: "user", Content: "This conversation was compacted from a longer session."},
	}); err != nil {
		t.Fatal(err)
	}

	// Post-compression messages.
	svc.AppendMessage(tenantID, llm.NewUserMessage("new user"))
	svc.AppendMessage(tenantID, llm.NewAssistantMessage("new answer"))

	// Replay (for LLM context) — should only show summary + new messages.
	replay, err := svc.Replay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay.Messages) != 4 {
		t.Fatalf("Replay() should show 4 messages (2 summary + 2 new), got %d: %+v",
			len(replay.Messages), replay.Messages)
	}

	// ReplayForDisplay — should show ALL messages including pre-compression.
	displayReplay, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	// Expected: 3 pre-compression + 1 [Compacted context] marker + 2 post-compression = 6.
	// The instruction message ("This conversation was compacted...") is skipped.
	if len(displayReplay.Messages) != 6 {
		t.Fatalf("ReplayForDisplay() should show 6 messages (3 old + 1 marker + 2 new), got %d: %+v",
			len(displayReplay.Messages), displayReplay.Messages)
	}

	// Verify pre-compression messages are preserved.
	if displayReplay.Messages[0].Content != "old user 1" {
		t.Fatalf("first message should be 'old user 1', got %q", displayReplay.Messages[0].Content)
	}
	if displayReplay.Messages[2].Content != "old user 2" {
		t.Fatalf("third message should be 'old user 2', got %q", displayReplay.Messages[2].Content)
	}

	// Verify [Compacted context] marker is present.
	hasMarker := false
	for _, m := range displayReplay.Messages {
		if strings.HasPrefix(m.Content, "[Compacted context]") {
			hasMarker = true
			break
		}
	}
	if !hasMarker {
		t.Fatal("ReplayForDisplay() should include [Compacted context] marker")
	}

	// Verify post-compression messages are present.
	if displayReplay.Messages[5].Content != "new answer" {
		t.Fatalf("last message should be 'new answer', got %q", displayReplay.Messages[5].Content)
	}
}

// TestReplayForDisplay_CompactMarkerCarriesTimestamp —— 压缩标记必须带**非零**
// Timestamp（= compress 记录的 created_at）。
//
// 为什么（2026-09-26 复查抓到的真实链路 bug）：`llm.ChatMessage.Timestamp` 的 tag 是
// `json:"-"` ⇒ snapshot JSON **不持久化**它 ⇒ 从 snapshot 反序列化出来的标记 Timestamp
// 是零值。而下游 `channel.compactionIteration` 用「标记时刻 vs 迭代 created_at」定位
// 「压缩发生在哪个迭代之后」（渲染在迭代之间）—— 零值会因 `ts.IsZero()` 永远回落成
// 独立行 ⇒ **内联渲染永不生效**（单测里手动构造 msgs 会掩盖它，所以必须守护真实链路）。
func TestReplayForDisplay_CompactMarkerCarriesTimestamp(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	svc.AppendMessage(tenantID, llm.NewUserMessage("old"))
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary"},
	}); err != nil {
		t.Fatal(err)
	}
	svc.AppendMessage(tenantID, llm.NewUserMessage("new"))

	displayReplay, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range displayReplay.Messages {
		if !strings.HasPrefix(m.Content, "[Compacted context]") {
			continue
		}
		found = true
		if m.Timestamp.IsZero() {
			t.Fatalf("压缩标记的 Timestamp 是零值 —— compactionIteration 会回落，内联渲染永不生效: %+v", m)
		}
	}
	if !found {
		t.Fatal("ReplayForDisplay 必须包含 [Compacted context] 标记")
	}
}

// TestGetHistoryBeforeForDisplayPagination verifies that GetHistoryBeforeForDisplay
// returns all messages (including pre-compression) and the correct total count
// for has_more pagination.
func TestGetHistoryBeforeForDisplayPagination(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// Add 10 pre-compression messages.
	for i := 0; i < 10; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("old msg"))
	}

	// Compress.
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary"},
	}); err != nil {
		t.Fatal(err)
	}

	// Add 5 post-compression messages.
	for i := 0; i < 5; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("new msg"))
	}

	// GetHistoryBefore (current, uses Replay) — only sees summary + 5 new = 6.
	replayMsgs, err := svc.GetHistoryBefore(tenantID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayMsgs) != 6 {
		t.Fatalf("GetHistoryBefore (Replay) should return 6, got %d", len(replayMsgs))
	}

	// GetHistoryBeforeForDisplay — sees all 10 + 1 marker + 5 = 16.
	displayMsgs, total, err := svc.GetHistoryBeforeForDisplay(tenantID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 16 {
		t.Fatalf("total should be 16, got %d", total)
	}
	if len(displayMsgs) != 16 {
		t.Fatalf("GetHistoryBeforeForDisplay should return 16, got %d", len(displayMsgs))
	}

	// Verify pagination: limit=10 counts MESSAGE rows (the window is the last
	// 10 message rows) and the result also carries the [Compacted context]
	// marker that sits inside that window — 11 fold rows, not 10.
	//
	// The old implementation tail-sliced the fold back to exactly 10 rows,
	// which dropped the OLDEST row of the window. That row could then never be
	// returned again: the next page continues at the returned `oldest_id`
	// (id < before_id), so the dropped row fell outside every subsequent
	// window. Turn-boundary alignment removed that slice (a sliced window also
	// re-splits the oldest turn), so the window is now "at least `limit`
	// message rows".
	displayMsgs2, total2, err := svc.GetHistoryBeforeForDisplay(tenantID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if total2 != 16 {
		t.Fatalf("total should be 16, got %d", total2)
	}
	if len(displayMsgs2) != 11 {
		t.Fatalf("GetHistoryBeforeForDisplay(limit=10) should return 11 (10 message rows + the marker inside the window), got %d", len(displayMsgs2))
	}

	// Paging must not lose rows: the next page continues strictly below the
	// returned cursor, and the two pages together cover every row below the
	// initial window with no gap and no repeat.
	nextMsgs, nextTotal, err := svc.GetHistoryBeforeForDisplay(tenantID, displayMsgs2[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if nextTotal != 5 {
		t.Fatalf("second page total should be 5, got %d", nextTotal)
	}
	if len(nextMsgs) != 5 {
		t.Fatalf("second page should return the remaining 5 rows, got %d", len(nextMsgs))
	}
	if nextMsgs[0].ID >= displayMsgs2[0].ID {
		t.Fatalf("cursor did not advance: page2 first id=%d, page1 first id=%d", nextMsgs[0].ID, displayMsgs2[0].ID)
	}
}

// TestGetHistoryBeforeForDisplay_WindowContract guards the windowed
// implementation (replayForDisplayWindow) against the full-scan fold oracle
// after turn-boundary alignment: for every (beforeID, limit) in the sweep
//
//  1. total is unchanged — the number of full-fold rows below beforeID;
//  2. the returned rows are a CONTIGUOUS SUFFIX of the full fold below
//     beforeID (no row skipped, reordered or duplicated). This matters: the
//     old tail-slice dropped the window's oldest row, and because the next
//     page continues at the returned cursor (id < before_id) the dropped row
//     fell outside every later window — permanently lost;
//  3. the window holds at least `limit` message rows (turn alignment only ever
//     grows the window);
//  4. the window never starts mid-turn: when the fold below beforeID contains
//     a user message, the first returned row is a user message — i.e. the
//     cursor is always a turn boundary. That is what makes the next page
//     deliver whole, strictly older turns instead of re-emitting a turn the
//     client already rendered.
func TestGetHistoryBeforeForDisplay_WindowContract(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// 10 pre-compression messages + compress marker + 8 post-compression
	// messages + a second compress marker + 4 tail messages, with a
	// display-only message interleaved (fold must skip it).
	for i := 0; i < 10; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("old msg"))
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary 1"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("mid msg"))
	}
	if _, err := svc.AppendContextSnapshot(tenantID, HistoryRecordCompress, []llm.ChatMessage{
		{Role: "user", Content: "[Compacted context]\n\nSummary 2"},
	}); err != nil {
		t.Fatal(err)
	}
	svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "display-only", DisplayOnly: true})
	for i := 0; i < 4; i++ {
		svc.AppendMessage(tenantID, llm.NewUserMessage("tail msg"))
	}

	// Oracle: the full fold (pre-rewrite behavior) cut at beforeID.
	full, err := svc.ReplayForDisplay(tenantID)
	if err != nil {
		t.Fatal(err)
	}
	below := func(beforeID int64) []llm.ChatMessage {
		msgs := full.Messages
		if beforeID > 0 {
			cut := len(msgs)
			for i, m := range msgs {
				if m.ID >= beforeID {
					cut = i
					break
				}
			}
			msgs = msgs[:cut]
		}
		return msgs
	}
	// isMessageRow: the fold emits non-display-only message rows plus the
	// [Compacted context] markers carried by compress/prune records. Markers
	// carry the compress record's id, which is NOT a session_messages
	// 'message' row — look it up so the "limit counts message rows" rule can
	// be checked from the oracle.
	isMessageRow := func(id int64) bool {
		var rt string
		if err := svc.db.Conn().QueryRow(
			`SELECT record_type FROM session_messages WHERE tenant_id = ? AND id = ?`,
			tenantID, id).Scan(&rt); err != nil {
			t.Fatalf("record_type lookup id=%d: %v", id, err)
		}
		return rt == "message"
	}

	var ids []int64
	for _, m := range full.Messages {
		ids = append(ids, m.ID)
	}

	limitCases := []int{3, 5, 100}
	for _, beforeID := range ids {
		for _, limit := range limitCases {
			wantAll := below(beforeID)
			wantTotal := len(wantAll)

			gotMsgs, gotTotal, err := svc.GetHistoryBeforeForDisplay(tenantID, beforeID, limit)
			if err != nil {
				t.Fatalf("beforeID=%d limit=%d: %v", beforeID, limit, err)
			}
			if gotTotal != wantTotal {
				t.Fatalf("beforeID=%d limit=%d: total want %d, got %d", beforeID, limit, wantTotal, gotTotal)
			}

			// (2) contiguous suffix of the oracle: find the offset and compare
			// the whole tail row by row.
			off := -1
			if len(gotMsgs) > 0 {
				for i, m := range wantAll {
					if m.ID == gotMsgs[0].ID {
						off = i
						break
					}
				}
			}
			if len(gotMsgs) > 0 && off < 0 {
				t.Fatalf("beforeID=%d limit=%d: first row id=%d is not in the full fold below beforeID (%s)",
					beforeID, limit, gotMsgs[0].ID, dumpIDs(wantAll))
			}
			wantSuffix := []llm.ChatMessage{}
			if off >= 0 {
				wantSuffix = wantAll[off:]
			}
			if len(gotMsgs) != len(wantSuffix) {
				t.Fatalf("beforeID=%d limit=%d: want suffix of %d rows (from id=%v), got %d",
					beforeID, limit, len(wantSuffix), firstIDOrNil(wantSuffix), len(gotMsgs))
			}
			for i := range wantSuffix {
				if gotMsgs[i].ID != wantSuffix[i].ID || gotMsgs[i].Content != wantSuffix[i].Content {
					t.Fatalf("beforeID=%d limit=%d: row %d want (id=%d %q), got (id=%d %q)",
						beforeID, limit, i, wantSuffix[i].ID, wantSuffix[i].Content, gotMsgs[i].ID, gotMsgs[i].Content)
				}
			}

			if len(gotMsgs) == 0 {
				continue
			}

			// (3) at least `limit` message rows (when the fold below beforeID
			// holds that many).
			avail := 0
			for _, m := range wantAll {
				if isMessageRow(m.ID) {
					avail++
				}
			}
			gotMsgsCount := 0
			for _, m := range gotMsgs {
				if isMessageRow(m.ID) {
					gotMsgsCount++
				}
			}
			wantCount := limit
			if avail < limit {
				wantCount = avail
			}
			if gotMsgsCount < wantCount {
				t.Fatalf("beforeID=%d limit=%d: window holds %d message rows, want >= %d",
					beforeID, limit, gotMsgsCount, wantCount)
			}

			// (4) the window starts at a turn boundary: a user message (the
			// first row of the turn that owns the limit-th row from the tail).
			if isMessageRow(gotMsgs[0].ID) && gotMsgs[0].Role != "user" {
				hasUser := false
				for _, m := range wantAll {
					if m.ID < gotMsgs[0].ID && isMessageRow(m.ID) && m.Role == "user" {
						hasUser = true
						break
					}
				}
				if hasUser {
					t.Fatalf("beforeID=%d limit=%d: window starts mid-turn at id=%d role=%s — a user message exists below it: %s",
						beforeID, limit, gotMsgs[0].ID, gotMsgs[0].Role, dumpIDs(wantAll))
				}
			}
		}
	}
}

func dumpIDs(msgs []llm.ChatMessage) string {
	var b strings.Builder
	for _, m := range msgs {
		fmt.Fprintf(&b, "%d/%s ", m.ID, m.Role)
	}
	return strings.TrimSpace(b.String())
}

func firstIDOrNil(msgs []llm.ChatMessage) any {
	if len(msgs) == 0 {
		return nil
	}
	return msgs[0].ID
}

// TestGetHistoryBeforeForDisplay_PagesNeverRereEmitADeliveredTurn is the
// regression test for the user-reported bug "前几次翻页 api 请求没有实际加载更多
// 信息": /api/history answered 200 with a non-empty payload, yet the UI grew by
// nothing for several consecutive loadMore requests.
//
// Root cause: the pagination window was bounded purely by row count, so a long
// turn (one that spans more rows than `limit`) was SPLIT across pages. The
// display fold renders a whole turn as ONE row carrying the turn's COMPLETE
// iteration list, so every page inside that turn re-emitted the turn the client
// had already merged into its slot — a page with payload but zero new turns.
// Measured on the production DB copy: 10 consecutive such pages for one session
// (a 1243-row turn), 41 no-op pages across the 40 most recent tenants.
//
// The invariant asserted here is the one that makes each loadMore page add
// visible content: a page must touch at least one turn that no earlier page
// touched, and must not re-touch any turn an earlier page already delivered.
// Turn-boundary alignment provides it by never splitting a turn.
func TestGetHistoryBeforeForDisplay_PagesNeverRereEmitADeliveredTurn(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)

	// 6 normal turns, then one long turn whose row count dwarfs `limit`.
	for turn := uint64(1); turn <= 6; turn++ {
		svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "q", TurnID: turn})
		svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "a", TurnID: turn})
	}
	const longTurn = uint64(7)
	svc.AppendMessage(tenantID, llm.ChatMessage{Role: "user", Content: "long q", TurnID: longTurn})
	for i := 0; i < 40; i++ {
		svc.AppendMessage(tenantID, llm.ChatMessage{
			Role: "assistant", Content: "", TurnID: longTurn,
			ToolCalls: []llm.ToolCall{{ID: fmt.Sprintf("c%d", i), Name: "Shell"}},
		})
	}
	svc.AppendMessage(tenantID, llm.ChatMessage{Role: "assistant", Content: "long a", TurnID: longTurn})

	const limit = 10
	delivered := map[uint64]bool{}
	seenRows := map[int64]string{}
	beforeID := int64(0)
	pages := 0
	for {
		msgs, total, err := svc.GetHistoryBeforeForDisplay(tenantID, beforeID, limit)
		if err != nil {
			t.Fatalf("page %d: %v", pages+1, err)
		}
		if len(msgs) == 0 {
			break
		}
		pages++
		if pages > 20 {
			t.Fatalf("pagination did not terminate: %d pages", pages)
		}
		// No row may be returned twice, and no row may be skipped: the page
		// must continue exactly where the previous one stopped.
		for _, m := range msgs {
			if prev, dup := seenRows[m.ID]; dup {
				t.Fatalf("page %d re-returned row id=%d (first seen as %q): pagination is not strictly advancing",
					pages, m.ID, prev)
			}
			seenRows[m.ID] = m.Role
		}
		// The page must start at a turn boundary (a user message) — that is
		// what guarantees the next page cannot re-deliver this page's turns.
		if msgs[0].Role != "user" {
			t.Fatalf("page %d starts mid-turn at id=%d role=%s (before_id=%d) — this page re-emits an already delivered turn",
				pages, msgs[0].ID, msgs[0].Role, beforeID)
		}
		// At least one turn on this page must be new (the page must be able to
		// add visible content).
		newTurns := 0
		for _, m := range msgs {
			if m.TurnID > 0 && !delivered[m.TurnID] {
				newTurns++
				delivered[m.TurnID] = true
			}
		}
		if newTurns == 0 {
			t.Fatalf("page %d (before_id=%d) delivered no new turn — a loadMore page that adds nothing to the UI: %s",
				pages, beforeID, dumpIDs(msgs))
		}
		oldest := msgs[0].ID
		if pages > 1 && oldest >= beforeID {
			t.Fatalf("page %d: cursor did not advance (%d >= %d)", pages, oldest, beforeID)
		}

		// has_more must agree with the remaining rows below the new cursor.
		remaining, _, err := svc.GetHistoryBeforeForDisplay(tenantID, oldest, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if hasMore := total > len(msgs); hasMore && len(remaining) == 0 {
			t.Fatalf("page %d: has_more=true but no rows remain below the cursor", pages)
		}
		beforeID = oldest
		if total <= len(msgs) {
			break
		}
	}

	// Every message row of the session must have been delivered exactly once.
	var want int
	if err := svc.db.Conn().QueryRow(
		`SELECT COUNT(*) FROM session_messages WHERE tenant_id = ? AND record_type = 'message' AND display_only = 0`,
		tenantID).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if len(seenRows) != want {
		t.Fatalf("pagination delivered %d rows, session has %d — rows were lost or duplicated", len(seenRows), want)
	}
	if pages < 2 {
		t.Fatalf("expected the long turn to require several pages, got %d", pages)
	}
}
