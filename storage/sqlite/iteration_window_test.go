package sqlite

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// iteration_window_test.go — 渲染镜像窗口化的存储层守护测试。
//
// 判别力（mutation 自证）：
//   - 窗口查询：改坏 run 边界 / head-7 / 计数 / LoadedTop → 对应断言红；
//   - EXPLAIN QUERY PLAN：谓词形式与索引漂移（语法级匹配失败 → 全表扫描）→ 红。

// writeTestIter writes one iteration_history row with the given shape.
func writeTestIter(t *testing.T, svc *SessionService, tenantID int64, turnID uint64, iter int, content, reasoning, tools string) {
	t.Helper()
	if err := svc.AppendIterationHistory(tenantID, 0, turnID, IterationRecord{
		MessageID: 0,
		TurnID:    turnID,
		Iteration: iter,
		Content:   content,
		Reasoning: reasoning,
		Tools:     tools,
	}); err != nil {
		t.Fatal(err)
	}
}

// toolsJSON builds a tools array with n placeholder tools (t0..t{n-1}).
func toolsJSON(n int) string {
	arr := make([]string, n)
	for i := range arr {
		arr[i] = fmt.Sprintf(`{"name":"t%d"}`, i)
	}
	return "[" + strings.Join(arr, ",") + "]"
}

// headToolNames extracts the tool names from a RunSummaryRecord's HeadToolsJSON.
func headToolNames(t *testing.T, s RunSummaryRecord) []string {
	t.Helper()
	var arr []map[string]any
	if err := json.Unmarshal([]byte(s.HeadToolsJSON), &arr); err != nil {
		t.Fatalf("head tools json: %v", err)
	}
	names := make([]string, 0, len(arr))
	for _, m := range arr {
		names = append(names, fmt.Sprint(m["name"]))
	}
	return names
}

// TestIterationWindow_BasicWindow covers the mixed window + the run summaries
// for a turn with text blocks, run heads (mixed rows with tools) and run
// members (tool-only rows):
//
//	1: text block
//	2: run head (content + 2 tools)   3..12: run members (1 tool each)
//	13: text block
//	14: run head (1 tool)              15: run member (1 tool)
//	16: text block
//
// MixedLimit=10 covers the whole turn (5 non-tool-only rows) → LoadedTop=1.
func TestIterationWindow_BasicWindow(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turn = uint64(100)
	writeTestIter(t, svc, tenantID, turn, 1, "text-1", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 2, "head-text", "", toolsJSON(2))
	for i := 3; i <= 12; i++ {
		writeTestIter(t, svc, tenantID, turn, i, "", "", toolsJSON(1))
	}
	writeTestIter(t, svc, tenantID, turn, 13, "text-13", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 14, "", "reason-14", toolsJSON(1))
	writeTestIter(t, svc, tenantID, turn, 15, "", "", toolsJSON(1))
	writeTestIter(t, svc, tenantID, turn, 16, "text-16", "", "[]")

	res, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	// Rows: the text blocks only (1, 13, 16) — run heads are carried by the
	// summaries, not the Rows.
	if len(res.Rows) != 3 {
		t.Fatalf("Rows: want 3 text blocks (1,13,16), got %d: %+v", len(res.Rows), res.Rows)
	}
	for i, want := range []int{1, 13, 16} {
		if res.Rows[i].Iteration != want {
			t.Fatalf("Rows[%d].Iteration = %d, want %d", i, res.Rows[i].Iteration, want)
		}
	}
	// Runs: (2..12) head 2 tools + 10 members = 12 tools; (14..15) 2 tools.
	if len(res.Runs) != 2 {
		t.Fatalf("Runs: want 2, got %d: %+v", len(res.Runs), res.Runs)
	}
	r1, r2 := res.Runs[0], res.Runs[1]
	if r1.StartIter != 2 || r1.EndIter != 12 {
		t.Fatalf("run 1 extent = %d..%d, want 2..12", r1.StartIter, r1.EndIter)
	}
	if r1.HeadContent != "head-text" {
		t.Fatalf("run 1 head content = %q, want %q", r1.HeadContent, "head-text")
	}
	if r1.ToolCount != 12 {
		t.Fatalf("run 1 tool count = %d, want 12 (2 head + 10 members)", r1.ToolCount)
	}
	if names := headToolNames(t, r1); len(names) != 8 {
		t.Fatalf("run 1 head tools = %v, want 8 tools (PILL_INLINE_MAX — ≤8 全显示)", names)
	}
	if r2.StartIter != 14 || r2.EndIter != 15 {
		t.Fatalf("run 2 extent = %d..%d, want 14..15", r2.StartIter, r2.EndIter)
	}
	if r2.HeadReasoning != "reason-14" {
		t.Fatalf("run 2 head reasoning = %q, want reason-14", r2.HeadReasoning)
	}
	if r2.ToolCount != 2 {
		t.Fatalf("run 2 tool count = %d, want 2", r2.ToolCount)
	}
	if names := headToolNames(t, r2); len(names) != 2 {
		t.Fatalf("run 2 head-7 = %v, want 2 tools (run smaller than 7)", names)
	}
	if res.Total != 16 {
		t.Fatalf("Total = %d, want 16", res.Total)
	}
	if res.LoadedTop != 1 {
		t.Fatalf("LoadedTop = %d, want 1 (window covers the whole turn)", res.LoadedTop)
	}
}

// TestIterationWindow_GiantRun covers the headline scenario: a 10k-member
// tool-only run. The window must return ONLY the run summary (head-7 + the
// true count) — no member payloads. The head-7 must be the run's TRUE head
// (the head iteration's tools first), matching the full-fetch rendering.
func TestIterationWindow_GiantRun(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turn = uint64(200)
	// 1: run head (1 tool); 2..10001: members (1 tool each).
	writeTestIter(t, svc, tenantID, turn, 1, "", "", toolsJSON(1))
	for i := 2; i <= 10001; i++ {
		writeTestIter(t, svc, tenantID, turn, i, "", "", toolsJSON(1))
	}

	res, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 0 {
		t.Fatalf("Rows: want 0 (no text blocks), got %d", len(res.Rows))
	}
	if len(res.Runs) != 1 {
		t.Fatalf("Runs: want 1, got %d", len(res.Runs))
	}
	r := res.Runs[0]
	if r.StartIter != 1 || r.EndIter != 10001 {
		t.Fatalf("run extent = %d..%d, want 1..10001", r.StartIter, r.EndIter)
	}
	if r.ToolCount != 10001 {
		t.Fatalf("tool count = %d, want 10001 (the TRUE count — the +N badge)", r.ToolCount)
	}
	if names := headToolNames(t, r); len(names) != 8 {
		t.Fatalf("head tools = %v, want 8 (PILL_INLINE_MAX)", names)
	}
	// The head tools must be the run's TRUE head: the head iteration's tool first
	// (t0 from iteration 1), then the members' tools in order.
	if names := headToolNames(t, r); names[0] != "t0" {
		t.Fatalf("head tools[0] = %q, want t0 (the head iteration's tool — the run renders its front)", names[0])
	}
	if res.Total != 10001 {
		t.Fatalf("Total = %d, want 10001", res.Total)
	}
	if res.LoadedTop != 1 {
		t.Fatalf("LoadedTop = %d, want 1", res.LoadedTop)
	}
}

// TestIterationWindow_CrossingRun covers a run extending below the window's
// top: the window (MixedLimit=2 → iterations 53 and 54) sits above a 50-member
// run headed at iteration 2 whose top member (52) TOUCHES the window's bottom
// edge (53-1). The crossing run's summary must carry the head (iteration 2 —
// a mixed row with tools) + the true count, and LoadedTop must extend down to
// the crossing run's head.
func TestIterationWindow_CrossingRun(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turn = uint64(300)
	writeTestIter(t, svc, tenantID, turn, 1, "text-1", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 2, "cross-head", "", toolsJSON(1))
	for i := 3; i <= 52; i++ {
		writeTestIter(t, svc, tenantID, turn, i, "", "", toolsJSON(1))
	}
	writeTestIter(t, svc, tenantID, turn, 53, "text-53", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 54, "text-54", "", "[]")

	res, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 2 || res.Rows[0].Iteration != 53 || res.Rows[1].Iteration != 54 {
		t.Fatalf("Rows: want iterations 53 and 54, got %+v", res.Rows)
	}
	if len(res.Runs) != 1 {
		t.Fatalf("Runs: want 1 (the crossing run), got %d: %+v", len(res.Runs), res.Runs)
	}
	r := res.Runs[0]
	if r.StartIter != 2 || r.EndIter != 52 {
		t.Fatalf("crossing run extent = %d..%d, want 2..52", r.StartIter, r.EndIter)
	}
	if r.HeadContent != "cross-head" {
		t.Fatalf("crossing head content = %q, want cross-head", r.HeadContent)
	}
	if r.ToolCount != 51 {
		t.Fatalf("crossing tool count = %d, want 51 (1 head + 50 members)", r.ToolCount)
	}
	if res.Total != 54 {
		t.Fatalf("Total = %d, want 54", res.Total)
	}
	// LoadedTop extends to the crossing run's head (2), not the window's
	// first row (53) — the scroll-up cursor must start below the run.
	if res.LoadedTop != 2 {
		t.Fatalf("LoadedTop = %d, want 2 (the crossing run's head)", res.LoadedTop)
	}
}

// TestIterationWindow_NonTouchingRunNotLoaded covers the negative: a run
// separated from the window by an older non-tool-only row is entirely below
// the window — NOT loaded (it would be fetched by a later scroll-up), and
// LoadedTop stays at the window's top.
func TestIterationWindow_NonTouchingRunNotLoaded(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turn = uint64(310)
	writeTestIter(t, svc, tenantID, turn, 1, "text-1", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 2, "head", "", toolsJSON(1))
	for i := 3; i <= 52; i++ {
		writeTestIter(t, svc, tenantID, turn, i, "", "", toolsJSON(1))
	}
	// text-53 separates the run (top member 52) from the window (54).
	writeTestIter(t, svc, tenantID, turn, 53, "text-53", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 54, "text-54", "", "[]")

	res, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Iteration != 54 {
		t.Fatalf("Rows: want only iteration 54, got %+v", res.Rows)
	}
	if len(res.Runs) != 0 {
		t.Fatalf("Runs: want 0 (the run does not touch the window), got %+v", res.Runs)
	}
	if res.LoadedTop != 54 {
		t.Fatalf("LoadedTop = %d, want 54 (the window's top)", res.LoadedTop)
	}
}

// TestIterationWindow_ScrollUpCursor covers the BeforeIter scroll-up fetch:
// the window below a cursor returns the older text blocks + the crossing run
// touching the new window's bottom edge. Members at or above the cursor are
// NOT re-included (the previous window's run summary already covers them).
func TestIterationWindow_ScrollUpCursor(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turn = uint64(400)
	writeTestIter(t, svc, tenantID, turn, 1, "text-1", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 2, "head", "", toolsJSON(1))
	for i := 3; i <= 52; i++ {
		writeTestIter(t, svc, tenantID, turn, i, "", "", toolsJSON(1))
	}
	writeTestIter(t, svc, tenantID, turn, 53, "text-53", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 54, "text-54", "", "[]")

	// Scroll up from iteration 54: the window below 54 (MixedLimit=1) → the
	// text block 53 (the NEW window's top) + the crossing run whose top member
	// (52) touches 53-1 → the run 2..52 is loaded as a crossing summary.
	res, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 1, BeforeIter: 54})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0].Iteration != 53 {
		t.Fatalf("Rows: want only iteration 53, got %+v", res.Rows)
	}
	if len(res.Runs) != 1 || res.Runs[0].StartIter != 2 || res.Runs[0].EndIter != 52 {
		t.Fatalf("Runs: want the crossing run 2..52 (touching the new window's top 53), got %+v", res.Runs)
	}
	if res.LoadedTop != 2 {
		t.Fatalf("LoadedTop = %d, want 2 (the crossing run's head)", res.LoadedTop)
	}

	// Scroll up from 53: the text block 53 was just loaded — the next window
	// below 53 → no non-tool-only rows below 53 except the head@2 → window
	// top 2, members 3..52 (>= 2, < 53) → the run (head@2 + 3..52).
	res2, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 1, BeforeIter: 53})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Runs) != 1 || res2.Runs[0].StartIter != 2 || res2.Runs[0].EndIter != 52 {
		t.Fatalf("Runs: want the run 2..52 (head within the new window), got %+v", res2.Runs)
	}
	if len(res2.Rows) != 0 {
		t.Fatalf("Rows: want 0 (no text blocks below 53 other than text-1... wait MixedLimit=1 → tail 1 = head@2), got %+v", res2.Rows)
	}
	if res2.LoadedTop != 2 {
		t.Fatalf("LoadedTop = %d, want 2", res2.LoadedTop)
	}

	// Scroll up from 2: the text block 1, nothing below → complete.
	res3, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 1, BeforeIter: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res3.Rows) != 1 || res3.Rows[0].Iteration != 1 {
		t.Fatalf("Rows: want only iteration 1, got %+v", res3.Rows)
	}
	// ⚠️ Members above the cursor must NOT be re-assembled: iteration 3..52
	// are >= windowTop(1) but ALSO >= the cursor(2)? No — 3 >= 2 means they
	// are ABOVE the cursor boundary... they are at iterations > 2, and the
	// Q2 upper bound (iteration < BeforeIter=2) excludes them → no phantom
	// headless run 3..52.
	if len(res3.Runs) != 0 {
		t.Fatalf("Runs: want 0 (no members in [1,2) — the Q2 cursor bound excludes 3..52), got %+v", res3.Runs)
	}
	if res3.LoadedTop != 1 {
		t.Fatalf("LoadedTop = %d, want 1 (complete)", res3.LoadedTop)
	}
}

// TestIterationWindow_HeadlessRun covers a run whose head is a tool-only
// iteration (mergeToolRuns: the head only needs tools — text is optional).
func TestIterationWindow_HeadlessRun(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	const turn = uint64(500)
	writeTestIter(t, svc, tenantID, turn, 1, "text-1", "", "[]")
	// 2..4: a tool-only stretch with NO mixed head — the first member (2) is
	// the head (hasTools, no text).
	for i := 2; i <= 4; i++ {
		writeTestIter(t, svc, tenantID, turn, i, "", "", toolsJSON(1))
	}
	writeTestIter(t, svc, tenantID, turn, 5, "text-5", "", "[]")

	res, err := svc.GetIterationWindow(tenantID, turn, IterationWindowOpts{MixedLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Runs) != 1 {
		t.Fatalf("Runs: want 1 (the headless run), got %d: %+v", len(res.Runs), res.Runs)
	}
	r := res.Runs[0]
	if r.StartIter != 2 || r.EndIter != 4 {
		t.Fatalf("headless run extent = %d..%d, want 2..4", r.StartIter, r.EndIter)
	}
	if r.ToolCount != 3 {
		t.Fatalf("headless tool count = %d, want 3", r.ToolCount)
	}
	if r.HeadContent != "" || r.HeadReasoning != "" {
		t.Fatalf("headless run head text = %q/%q, want empty", r.HeadContent, r.HeadReasoning)
	}
	if res.LoadedTop != 1 {
		t.Fatalf("LoadedTop = %d, want 1", res.LoadedTop)
	}
}

// TestIterationWindow_EmptyTurn covers the empty-turn edge (no iterations).
func TestIterationWindow_EmptyTurn(t *testing.T) {
	_, svc, tenantID := newHistoryTestService(t)
	res, err := svc.GetIterationWindow(tenantID, 999, IterationWindowOpts{MixedLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 || res.LoadedTop != 0 || len(res.Rows) != 0 || len(res.Runs) != 0 {
		t.Fatalf("empty turn: want all zero/empty, got %+v", res)
	}
}

// TestIterationWindowUsesPartialIndexes is the EXPLAIN QUERY PLAN guard:
// the tool-only member scan MUST be a COVERING index scan over idx_iter_window
// (no payload page reads — otherwise a 10k-member run's count reads ≈5MB of
// tools JSON), and the mixed-block window must use the index for positioning.
// This guard is what caught the original partial-index design's fatal flaw:
// under modernc.org/sqlite v1.46.1 partial indexes are NEVER covering (the
// WHERE-implication columns force table lookups) — see migrateV70ToV71.
func TestIterationWindowUsesPartialIndexes(t *testing.T) {
	db, _, tenantID := newHistoryTestService(t)
	conn := db.Conn()

	plan := func(t *testing.T, query string, args ...any) string {
		t.Helper()
		rows, err := conn.Query("EXPLAIN QUERY PLAN "+query, args...)
		if err != nil {
			t.Fatalf("explain: %v", err)
		}
		defer rows.Close()
		var sb strings.Builder
		for rows.Next() {
			var seq, from, detail string
			if err := rows.Scan(&seq, &from, &detail); err != nil {
				// Some drivers expose 4 columns (id, parent, notused, detail).
				var id, parent, notused string
				if err2 := rows.Scan(&id, &parent, &notused, &detail); err2 != nil {
					t.Fatalf("explain scan: %v / %v", err, err2)
				}
			}
			sb.WriteString(detail)
			sb.WriteString("\n")
		}
		return sb.String()
	}

	// The tool-only member scan — must be a COVERING scan over idx_iter_window.
	p := plan(t, `SELECT iteration, tool_count FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ? AND iteration >= ? AND `+toolOnlySQL+`
		ORDER BY iteration ASC`, tenantID, uint64(1), 0)
	if !strings.Contains(p, "idx_iter_window") {
		t.Fatalf("tool-only scan does not use idx_iter_window:\n%s", p)
	}
	if !strings.Contains(p, "COVERING") {
		t.Fatalf("tool-only scan is not a COVERING index scan (the count must come from the index — otherwise the +N badge reads payload pages):\n%s", p)
	}

	// The mixed-block window — must use idx_iter_window for positioning (the
	// K payload lookups are expected: those rows' payloads ARE the window).
	p = plan(t, `SELECT iteration FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ? AND `+notToolOnlySQL+`
		ORDER BY iteration DESC LIMIT ?`, tenantID, uint64(1), 10)
	if !strings.Contains(p, "idx_iter_window") {
		t.Fatalf("mixed-rows query does not use idx_iter_window:\n%s", p)
	}
}

// TestMigrateV70ToV71_CreatesPartialIndexes covers the migration: a v70-era
// database (the columns/index absent) gets them back, idempotently, with a
// correct backfill of the stored columns.
func TestMigrateV70ToV71_CreatesPartialIndexes(t *testing.T) {
	db, svc, tenantID := newHistoryTestService(t)
	conn := db.Conn()
	const turn = uint64(700)
	// Seed rows of all three shapes (via the write path — computes the columns).
	writeTestIter(t, svc, tenantID, turn, 1, "text", "", "[]")
	writeTestIter(t, svc, tenantID, turn, 2, "head", "", toolsJSON(2))
	writeTestIter(t, svc, tenantID, turn, 3, "", "", toolsJSON(3))

	// Simulate the v70 state: the columns/index absent (drop + rebuild the
	// columns' data as if they never existed).
	if _, err := conn.Exec(`DROP INDEX IF EXISTS idx_iter_window;`); err != nil {
		t.Fatal(err)
	}
	// Drop the computed columns by recreating them as raw default-0 columns:
	// ALTER ADD is guarded by columnExists, so simulate a "column exists but
	// zeroed" partial migration (the crash-recovery path) instead.
	if _, err := conn.Exec(`UPDATE iteration_history SET tool_only = 0, tool_count = 0`); err != nil {
		t.Fatal(err)
	}
	if err := migrateV70ToV71(db); err != nil {
		t.Fatalf("migrate v70->v71: %v", err)
	}
	// The backfill must have recomputed the columns correctly.
	var toolOnly2, toolCount2, toolOnly3, toolCount3, toolOnly1 int
	if err := conn.QueryRow(`SELECT tool_only, tool_count FROM iteration_history WHERE turn_id = ? AND iteration = 2`, turn).Scan(&toolOnly2, &toolCount2); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT tool_only, tool_count FROM iteration_history WHERE turn_id = ? AND iteration = 3`, turn).Scan(&toolOnly3, &toolCount3); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRow(`SELECT tool_only FROM iteration_history WHERE turn_id = ? AND iteration = 1`, turn).Scan(&toolOnly1); err != nil {
		t.Fatal(err)
	}
	if toolOnly1 != 0 {
		t.Fatalf("text block tool_only = %d, want 0", toolOnly1)
	}
	if toolOnly2 != 0 || toolCount2 != 2 {
		t.Fatalf("run head (mixed) tool_only/tool_count = %d/%d, want 0/2", toolOnly2, toolCount2)
	}
	if toolOnly3 != 1 || toolCount3 != 3 {
		t.Fatalf("run member (tool-only) tool_only/tool_count = %d/%d, want 1/3", toolOnly3, toolCount3)
	}
	var cnt int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = 'idx_iter_window'`).Scan(&cnt); err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Fatalf("want idx_iter_window after migration, got %d", cnt)
	}
	// Idempotent: running again must not fail or duplicate.
	if err := migrateV70ToV71(db); err != nil {
		t.Fatalf("migrate v70->v71 (idempotent re-run): %v", err)
	}
}
