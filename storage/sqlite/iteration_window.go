package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// iteration_window.go — 渲染镜像窗口化（v71 存储列 + 复合索引的查询面）。
//
// 设计（见 docs/agent/web-message-store.md 与迭代窗口化设计讨论）：
// 数据窗口化镜像渲染窗口化 —— 折叠 run 的默认渲染只需要头部 7 个工具 + 真实总数
// （FoldedToolGroup 的 PILL_INLINE_HEAD=7 + "+N" 徽标），混合迭代块（content/reasoning）
// 只需要视口邻域。因此每个 turn 的窗口化拉取 = 尾部 K 个非 tool-only 行（文本块 +
// run 头部）+ 与窗口相交的 run 摘要（头部 7 工具 + 工具总数 + 范围），run 内部
// （tool-only 成员）不传输 —— 它们本来就不渲染（折叠着），只在 "+N" 菜单按需分页。
//
// 三类迭代（与前端 mergeToolRuns 的 absorbs 判据一致 —— ⚠️ 语义跨 SQL/Go/TS 三处，
// 单一来源是 computeIterationWindowColumns（写入时计算 tool_only/tool_count），
// E2E 截图对比守护端到端一致性）：
//   - 文本块：tools='[]'（无工具）→ 逐块渲染，进窗口（Rows）
//   - run 头部：tools≠'[]' 且（content≠'' 或 reasoning≠''）→ run 块的头部文本，
//     进摘要（Runs 的 HeadContent/HeadReasoning）
//   - run 成员：tool-only（tools≠'[]' 且 content='' 且 reasoning=''）→ 折叠进 run，
//     不传输（只有头部 7 个工具 + 计数进摘要）
//
// 索引：idx_iter_window (tenant_id, turn_id, tool_only, iteration, tool_count) —
// tool-only 成员扫描是 COVERING（不碰 payload 页），混合块窗口定位后只回表 K 行
// payload（本来就是需要的）。EXPLAIN QUERY PLAN 守护测试盯着。

// The run-membership predicate and its negation, as SQL fragments over the
// v71 stored column (computeIterationWindowColumns is the single semantic
// source; a plain column equality keeps the planner robust — no partial-index
// syntactic-matching fragility).
const (
	toolOnlySQL    = "tool_only = 1"
	notToolOnlySQL = "tool_only = 0"
)

// IterationWindowOpts controls GetIterationWindow.
type IterationWindowOpts struct {
	// MixedLimit is the tail K non-tool-only rows (text blocks + run heads)
	// to load — the individually-rendered window.
	MixedLimit int
	// BeforeIter is the scroll-up cursor: load the window strictly BELOW this
	// iteration (0 = initial fetch from the turn's end).
	BeforeIter int
}

// RunSummaryRecord is a folded run's rendering summary: everything the default
// rendering of a run block needs (the head's text + the merged head-7 tools +
// the true tool count), without transferring the run's interior.
type RunSummaryRecord struct {
	// StartIter is the run's first iteration (the head's iteration — the run
	// block renders at this position, matching mergeToolRuns keeping the head).
	StartIter int
	// EndIter is the run's last iteration (the last member, or the head's own
	// iteration for a memberless run).
	EndIter int
	// HeadContent / HeadReasoning: the head iteration's text (the run block
	// renders it above the pills — mergeToolRuns keeps the head's text).
	HeadContent  string
	HeadReasoning string
	// HeadToolsJSON is the merged head-7 tools as a raw JSON array (the head's
	// tools + the first members' tools, first 7 total — PILL_INLINE_HEAD).
	HeadToolsJSON string
	// ToolCount is the run's TOTAL tool count (the head's + all members') —
	// the "+N" badge. Never estimated: the head's array length + the members'
	// json_array_length sum (the expression index).
	ToolCount int
}

// IterationWindowResult is the rendering-mirrored window for one turn.
type IterationWindowResult struct {
	// Rows: the text-block iterations (non-tool-only rows WITHOUT tools) in
	// the window, ASC. Run heads (non-tool-only rows WITH tools) are NOT here —
	// they are carried by the run summaries (the run block renders the head's
	// text + the merged pills).
	Rows []IterationRecord
	// Runs: the folded-run summaries intersecting the window's range (within +
	// crossing), ASC by StartIter.
	Runs []RunSummaryRecord
	// Total is the turn's total iteration count (iter_total — the gap
	// detection + the scroll-up completeness).
	Total int
	// LoadedTop is the top of the loaded set: the minimum of the window's
	// first row and the crossing run's head. 0 = the turn has no iterations.
	// 1 = the window covers the turn from the very start (nothing above).
	LoadedTop int
}

// toolOnlyMemberRow is one covering-scan row from idx_iter_window: a run
// member's iteration number + its tool count (the stored tool_count column —
// no payload page read).
type toolOnlyMemberRow struct {
	iteration int
	toolCount int
}

// GetIterationWindow returns the rendering-mirrored window for one turn:
// the tail K non-tool-only rows + the run summaries intersecting the range +
// the total count + the loaded top. See the file comment for the design.
//
// The caller (the history snapshot assembly) maps Rows → individually-rendered
// iterations and Runs → run blocks; the run interiors are NOT transferred.
func (s *SessionService) GetIterationWindow(tenantID int64, turnID uint64, opts IterationWindowOpts) (*IterationWindowResult, error) {
	if opts.MixedLimit <= 0 {
		opts.MixedLimit = 50
	}
	conn, err := s.conn()
	if err != nil {
		return nil, err
	}

	// Q1: the tail K non-tool-only rows (text blocks + run heads), payloads.
	// Uses idx_iter_mixed (the partial index — the WHERE matches syntactically).
	q1 := `
		SELECT message_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id, COALESCE(created_at, '')
		FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ?` + andBelow(opts.BeforeIter) + ` AND ` + notToolOnlySQL + `
		ORDER BY iteration DESC LIMIT ?`
	q1Args := []any{tenantID, turnID}
	if opts.BeforeIter > 0 {
		q1Args = append(q1Args, opts.BeforeIter)
	}
	q1Args = append(q1Args, opts.MixedLimit)
	rows, err := conn.Query(q1, q1Args...)
	if err != nil {
		return nil, fmt.Errorf("get iteration window q1 (mixed rows): %w", err)
	}
	mixed, err := scanIterationRecords(rows)
	if err != nil {
		return nil, err
	}
	// scanIterationRecords returns DESC (the query order); the window is ASC.
	sort.Slice(mixed, func(i, j int) bool { return mixed[i].Iteration < mixed[j].Iteration })

	res := &IterationWindowResult{Rows: []IterationRecord{}, Runs: []RunSummaryRecord{}}

	// Q5: the turn's total iteration count (the existing index — covering).
	if err := conn.QueryRow(`SELECT COUNT(*) FROM iteration_history WHERE tenant_id = ? AND turn_id = ?`, tenantID, turnID).Scan(&res.Total); err != nil {
		return nil, fmt.Errorf("get iteration window total: %w", err)
	}
	if res.Total == 0 {
		return res, nil
	}

	// The window's top: the smallest mixed iteration in the batch (or the
	// cursor when the batch is empty — a tool-only-only stretch, e.g. a giant
	// run; the crossing-run detection below then covers from the cursor down).
	windowTop := opts.BeforeIter
	if len(mixed) > 0 {
		windowTop = mixed[0].Iteration
	}

	// Q2: the run members (tool-only rows) in the NEWLY-LOADED region
	// [windowTop, BeforeIter) — for the initial fetch (BeforeIter=0) the
	// region is [windowTop, ∞). Bounding by the cursor matters on scroll-up:
	// members at or above the cursor were already loaded by the previous
	// window (their run summary covers them) — re-including them would
	// assemble a duplicate headless run when the run's head sits exactly at
	// the cursor boundary.
	membersAbove, err := scanToolOnlyMembers(conn, tenantID, turnID, windowTop, opts.BeforeIter)
	if err != nil {
		return nil, err
	}

	// Q3: the crossing run — the consecutive tool-only members strictly below
	// the window's top that TOUCH it (the highest member is exactly
	// windowTop-1). A run separated from the window by an older non-tool-only
	// row is entirely below the window and will be fetched by a later
	// scroll-up — not a crossing run. Skipped when windowTop=0 (the turn is
	// all tool-only — Q2's unbounded scan already covers the whole run).
	var crossing []toolOnlyMemberRow
	if windowTop > 0 {
		membersBelow, err := scanToolOnlyMembers(conn, tenantID, turnID, 0, windowTop)
		if err != nil {
			return nil, err
		}
		crossing = consecutivePrefixBelow(membersBelow, windowTop)
	}

	// Assemble the runs within the newly-loaded region: merge the mixed rows
	// (Q1) with the members (Q2) in iteration order; a run = a head (a mixed
	// row with tools, or the first member of a member stretch) + the following
	// members.
	runs, textRows, err := s.assembleRuns(conn, tenantID, turnID, mixed, membersAbove)
	if err != nil {
		return nil, err
	}
	res.Rows = textRows
	res.Runs = runs

	// The crossing run (below the window's top): the head detection + the
	// summary. The loaded set extends below the window through the crossing
	// run's extent.
	if len(crossing) > 0 {
		summary, loadedTop, err := s.crossingRunSummary(conn, tenantID, turnID, crossing)
		if err != nil {
			return nil, err
		}
		if summary != nil {
			res.Runs = append(res.Runs, *summary)
		}
		if loadedTop > 0 && (res.LoadedTop == 0 || loadedTop < res.LoadedTop) {
			res.LoadedTop = loadedTop
		}
	}

	// The loaded top: the minimum of the window's top and the crossing run's
	// head. windowTop=0 (an all-tool-only turn) means the single run summary
	// covers the turn from iteration 1 — complete.
	if res.LoadedTop == 0 {
		res.LoadedTop = windowTop
	}
	if res.LoadedTop <= 0 {
		res.LoadedTop = 1
	}
	return res, nil
}

// andBelow renders the cursor clause for the scroll-up fetch.
func andBelow(before int) string {
	if before > 0 {
		return " AND iteration < ?"
	}
	return ""
}

// scanToolOnlyMembers scans the covering index idx_iter_window for the run
// members (tool-only rows) in the iteration range [from, to) — from=0 means
// unbounded below, to=0 means unbounded above. Returns ASC by iteration.
// Index-only: the iteration + the stored tool_count column — no payload page
// reads (this is what makes a 10k-member run's tool count a millisecond-scale
// covering scan instead of a 5MB payload read).
func scanToolOnlyMembers(conn *sql.DB, tenantID int64, turnID uint64, from, to int) ([]toolOnlyMemberRow, error) {
	q := `SELECT iteration, tool_count FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ? AND ` + toolOnlySQL
	args := []any{tenantID, turnID}
	if from > 0 {
		q += ` AND iteration >= ?`
		args = append(args, from)
	}
	if to > 0 {
		q += ` AND iteration < ?`
		args = append(args, to)
	}
	q += ` ORDER BY iteration ASC`
	rows, err := conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("scan tool-only members: %w", err)
	}
	defer rows.Close()
	out := []toolOnlyMemberRow{}
	for rows.Next() {
		var r toolOnlyMemberRow
		if err := rows.Scan(&r.iteration, &r.toolCount); err != nil {
			return nil, fmt.Errorf("scan tool-only member row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// consecutivePrefixBelow returns the member stretch that forms one consecutive
// run TOUCHING the window's bottom edge: the highest member is exactly top-1,
// walking down while consecutive. A run separated from the window by an older
// non-tool-only row (the highest member < top-1) is entirely below the window
// — not a crossing run — returns nil. members must be ASC and all < top.
func consecutivePrefixBelow(members []toolOnlyMemberRow, top int) []toolOnlyMemberRow {
	if len(members) == 0 || top <= 0 {
		return nil
	}
	end := len(members)
	// The touching check: the run must reach the window's bottom edge.
	if members[end-1].iteration != top-1 {
		return nil
	}
	start := end - 1
	for start > 0 && members[start-1].iteration == members[start].iteration-1 {
		start--
	}
	return members[start:end]
}

// windowEntry is one merged-scan entry: a non-tool-only row (payload in hand)
// or a run member (count only, from the covering scan).
type windowEntry struct {
	iter   int
	mixed  *IterationRecord
	member *toolOnlyMemberRow
}

// assembleRuns merges the window's mixed rows (text blocks + run heads) with
// the run members (tool-only rows) in iteration order and produces:
//   - the run summaries for every run intersecting the window (a run head in
//     the window, or a member stretch within the window);
//   - the text rows (the non-tool-only rows WITHOUT tools — the run heads are
//     carried by the summaries, not the Rows).
//
// The head-7 tools need payload reads for the first members (the covering
// scan only carries per-member counts); those are batched per run.
func (s *SessionService) assembleRuns(conn *sql.DB, tenantID int64, turnID uint64, mixed []IterationRecord, members []toolOnlyMemberRow) ([]RunSummaryRecord, []IterationRecord, error) {
	entries := make([]windowEntry, 0, len(mixed)+len(members))
	mi, ti := 0, 0
	for mi < len(mixed) || ti < len(members) {
		if ti == len(members) || (mi < len(mixed) && mixed[mi].Iteration < members[ti].iteration) {
			entries = append(entries, windowEntry{iter: mixed[mi].Iteration, mixed: &mixed[mi]})
			mi++
		} else {
			entries = append(entries, windowEntry{iter: members[ti].iteration, member: &members[ti]})
			ti++
		}
	}

	textRows := []IterationRecord{}
	runs := []RunSummaryRecord{}
	i := 0
	for i < len(entries) {
		e := entries[i]
		if e.mixed != nil && len(e.mixed.Tools) > 0 && e.mixed.Tools != "[]" {
			// Run head (a mixed row with tools): the run = this head + the
			// following member stretch.
			head := e.mixed
			j := i + 1
			for j < len(entries) && entries[j].member != nil {
				j++
			}
			stretch := memberStretch(entries, i+1, j)
			summary, err := s.buildRunSummary(conn, tenantID, turnID, head, stretch)
			if err != nil {
				return nil, nil, err
			}
			runs = append(runs, *summary)
			i = j
			continue
		}
		if e.mixed != nil {
			// Text block (no tools): individually rendered.
			textRows = append(textRows, *e.mixed)
			i++
			continue
		}
		// Member stretch without a mixed head in the window: the head is the
		// first member itself (mergeToolRuns: a tool-only iteration can be the
		// head — hasTools with no text). This happens when the stretch starts
		// at the window's top boundary (the head is below the window — the
		// crossing-run path handles that) or right after a text block.
		j := i
		for j < len(entries) && entries[j].member != nil {
			j++
		}
		stretch := memberStretch(entries, i, j)
		// The head is the first member — its payload (tools JSON) is needed.
		// ⚠️ The head must NOT also be passed as a member (double-counting the
		// head's tools — the GiantRun guard test catches exactly this).
		firstIter := stretch[0].iteration
		headRec, err := fetchIterationRecord(conn, tenantID, turnID, firstIter)
		if err != nil {
			return nil, nil, err
		}
		summary, err := s.buildRunSummary(conn, tenantID, turnID, headRec, stretch[1:])
		if err != nil {
			return nil, nil, err
		}
		runs = append(runs, *summary)
		i = j
	}
	return runs, textRows, nil
}

// memberStretch extracts the member rows [from, to) of the entries slice.
func memberStretch(entries []windowEntry, from, to int) []toolOnlyMemberRow {
	out := make([]toolOnlyMemberRow, 0, to-from)
	for k := from; k < to; k++ {
		if entries[k].member != nil {
			out = append(out, *entries[k].member)
		}
	}
	return out
}

// buildRunSummary assembles one run summary: the head's text + the merged
// head-7 tools (payload reads for the first members) + the true tool count.
func (s *SessionService) buildRunSummary(conn *sql.DB, tenantID int64, turnID uint64, head *IterationRecord, stretch []toolOnlyMemberRow) (*RunSummaryRecord, error) {
	summary := &RunSummaryRecord{
		StartIter:      head.Iteration,
		EndIter:        head.Iteration,
		HeadContent:     head.Content,
		HeadReasoning:  head.Reasoning,
	}
	headTools, headCount, err := parseToolsArray(head.Tools)
	if err != nil {
		return nil, fmt.Errorf("parse run head tools (iter %d): %w", head.Iteration, err)
	}
	summary.ToolCount = headCount
	if len(stretch) > 0 {
		summary.EndIter = stretch[len(stretch)-1].iteration
		for _, m := range stretch {
			summary.ToolCount += m.toolCount
		}
	}
	// The merged head-7: the head's tools + the first members' tools, first 7
	// total (PILL_INLINE_HEAD). The members' tools need payload reads — only
	// as many members as cover the remaining slots.
	merged := append([]json.RawMessage{}, headTools...)
	if len(merged) < 7 {
		need := 7 - len(merged)
		var firstIters []int
		var acc int
		for _, m := range stretch {
			if acc >= need {
				break
			}
			firstIters = append(firstIters, m.iteration)
			acc += m.toolCount
		}
		if len(firstIters) > 0 {
			memberTools, err := fetchToolsJSON(conn, tenantID, turnID, firstIters)
			if err != nil {
				return nil, err
			}
			for _, tools := range memberTools {
				arr, _, err := parseToolsArray(tools)
				if err != nil {
					return nil, err
				}
				merged = append(merged, arr...)
			}
		}
	}
	if len(merged) > 7 {
		merged = merged[:7]
	}
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, fmt.Errorf("marshal head tools: %w", err)
	}
	summary.HeadToolsJSON = string(raw)
	return summary, nil
}

// crossingRunSummary builds the summary for the run extending below the
// window's top (the crossing run): the head detection (the non-tool-only row
// below the member stretch — a run head if it has tools, else the first
// member is the head) + the head-7 + the count. Returns the summary and the
// loaded top (the head's iteration, or the first member's for a memberless
// head).
func (s *SessionService) crossingRunSummary(conn *sql.DB, tenantID int64, turnID uint64, crossing []toolOnlyMemberRow) (*RunSummaryRecord, int, error) {
	// The stretch is ASC; its lowest iteration is the run's start candidate.
	lowest := crossing[0].iteration
	// The head candidate: the non-tool-only row just below the stretch.
	headBelow, err := fetchNonToolOnlyBelow(conn, tenantID, turnID, lowest)
	if err != nil {
		return nil, 0, err
	}
	if headBelow != nil && len(headBelow.Tools) > 0 && headBelow.Tools != "[]" {
		// A run head (a mixed row with tools) — the run includes it.
		summary, err := s.buildRunSummary(conn, tenantID, turnID, headBelow, crossing)
		if err != nil {
			return nil, 0, err
		}
		return summary, headBelow.Iteration, nil
	}
	// Headless: the first member is the head (a tool-only iteration can be
	// the head — hasTools, no text). ⚠️ crossing[0] IS the head — it must not
	// also be counted as a member (double-counting).
	firstRec, err := fetchIterationRecord(conn, tenantID, turnID, lowest)
	if err != nil {
		return nil, 0, err
	}
	summary, err := s.buildRunSummary(conn, tenantID, turnID, firstRec, crossing[1:])
	if err != nil {
		return nil, 0, err
	}
	return summary, lowest, nil
}

// fetchIterationRecord loads one full iteration record by iteration number.
func fetchIterationRecord(conn *sql.DB, tenantID int64, turnID uint64, iteration int) (*IterationRecord, error) {
	row := conn.QueryRow(`
		SELECT message_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id, COALESCE(created_at, '')
		FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ? AND iteration = ?`, tenantID, turnID, iteration)
	var rec IterationRecord
	var createdAt string
	if err := row.Scan(&rec.MessageID, &rec.TurnID, &rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs, &rec.InputTokens, &rec.CachedTokens, &rec.Model, &rec.SubscriptionID, &createdAt); err != nil {
		return nil, fmt.Errorf("fetch iteration record (iter %d): %w", iteration, err)
	}
	rec.CreatedAt = parseSQLiteTime(createdAt)
	return &rec, nil
}

// fetchNonToolOnlyBelow loads the closest non-tool-only row strictly below the
// given iteration (the crossing run's head candidate). Nil when none exists.
func fetchNonToolOnlyBelow(conn *sql.DB, tenantID int64, turnID uint64, below int) (*IterationRecord, error) {
	row := conn.QueryRow(`
		SELECT message_id, turn_id, iteration, content, reasoning, tools, tokens, ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens, model, subscription_id, COALESCE(created_at, '')
		FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ? AND iteration < ? AND `+notToolOnlySQL+`
		ORDER BY iteration DESC LIMIT 1`, tenantID, turnID, below)
	var rec IterationRecord
	var createdAt string
	err := row.Scan(&rec.MessageID, &rec.TurnID, &rec.Iteration, &rec.Content, &rec.Reasoning, &rec.Tools, &rec.Tokens, &rec.TTFTMs, &rec.TokensPerSec, &rec.TotalMs, &rec.TPOTMs, &rec.InputTokens, &rec.CachedTokens, &rec.Model, &rec.SubscriptionID, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("fetch non-tool-only below (iter %d): %w", below, err)
	}
	rec.CreatedAt = parseSQLiteTime(createdAt)
	return &rec, nil
}

// fetchToolsJSON loads the tools JSON for the given iteration numbers (the
// head-7 member payload reads — a small IN batch).
func fetchToolsJSON(conn *sql.DB, tenantID int64, turnID uint64, iterations []int) ([]string, error) {
	if len(iterations) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(iterations))
	args := make([]any, 0, len(iterations)+2)
	args = append(args, tenantID, turnID)
	for i, it := range iterations {
		placeholders[i] = "?"
		args = append(args, it)
	}
	q := fmt.Sprintf(`SELECT iteration, tools FROM iteration_history
		WHERE tenant_id = ? AND turn_id = ? AND iteration IN (%s)
		ORDER BY iteration ASC`, joinInts(placeholders, ","))
	rows, err := conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("fetch tools json: %w", err)
	}
	defer rows.Close()
	byIter := map[int]string{}
	for rows.Next() {
		var it int
		var tools string
		if err := rows.Scan(&it, &tools); err != nil {
			return nil, err
		}
		byIter[it] = tools
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(iterations))
	for _, it := range iterations {
		if t, ok := byIter[it]; ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// parseToolsArray parses a tools JSON array into raw elements + the count.
func parseToolsArray(tools string) ([]json.RawMessage, int, error) {
	if tools == "" || tools == "[]" {
		return nil, 0, nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal([]byte(tools), &arr); err != nil {
		return nil, 0, err
	}
	return arr, len(arr), nil
}

func joinInts(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// RunTool is one element of the "+N" menu's page: the raw tool-snapshot JSON
// + its SOURCE iteration (the ToolProgress.Iteration must carry the tool's
// real iteration, not the run's start — the menu rows show per-tool badges).
type RunTool struct {
	Raw       json.RawMessage
	Iteration int
}

// GetRunTools returns the run's merged tools (the head's + the members'
// concatenated) as a paginated slice [offset, offset+limit) — the "+N" menu's
// on-demand fetch. The run is identified by its extent [startIter, endIter]
// (from the RunSummary the client holds). Returns the page (each element with
// its source iteration) + the run's total tool count (for the menu's
// pagination).
//
// The members are CLAMPED to the true consecutive run (the tool-only rows from
// startIter+1 until the first gap): a stale client extent must never leak the
// NEXT run's tools into the menu. The head's record must exist (validated).
// The payload reads are bounded by the page: the head (1 row) + only the
// members covering [offset, offset+limit) — never the whole run.
func (s *SessionService) GetRunTools(tenantID int64, turnID uint64, startIter, endIter, offset, limit int) ([]RunTool, int, error) {
	if limit <= 0 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	conn, err := s.conn()
	if err != nil {
		return nil, 0, err
	}
	// The head's record (the payload — its tools are the merged array's prefix).
	head, err := fetchIterationRecord(conn, tenantID, turnID, startIter)
	if err != nil {
		return nil, 0, err
	}
	headTools, headCount, err := parseToolsArray(head.Tools)
	if err != nil {
		return nil, 0, fmt.Errorf("parse run head tools (iter %d): %w", startIter, err)
	}

	// The members: the tool-only rows in (startIter, endIter], clamped to the
	// true consecutive run (the covering scan — the counts only).
	members, err := scanToolOnlyMembers(conn, tenantID, turnID, startIter+1, endIter+1)
	if err != nil {
		return nil, 0, err
	}
	// Clamp to the consecutive prefix (until the first iteration gap).
	clamped := members[:0:0]
	for i, m := range members {
		if i > 0 && m.iteration != members[i-1].iteration+1 {
			break
		}
		clamped = append(clamped, m)
	}
	members = clamped

	total := headCount
	for _, m := range members {
		total += m.toolCount
	}
	if offset >= total {
		return []RunTool{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}

	// Assemble the page [offset, end) over the merged array (the head's tools
	// then the members' tools in order). Only the members overlapping the page
	// get payload reads.
	page := make([]RunTool, 0, end-offset)
	// Head segment [0, headCount) — the source iteration is the head's.
	if offset < headCount {
		headEnd := end
		if headEnd > headCount {
			headEnd = headCount
		}
		for _, raw := range headTools[offset:headEnd] {
			page = append(page, RunTool{Raw: raw, Iteration: startIter})
		}
	}
	// Member segments: cumulative [headCount + sum(c0..ck-1), +ck) — the
	// source iteration is each member's own.
	if end > headCount {
		cum := headCount
		var needIters []int
		for _, m := range members {
			segStart, segEnd := cum, cum+m.toolCount
			cum = segEnd
			if segEnd <= offset {
				continue // entirely before the page
			}
			if segStart >= end {
				break // entirely after the page
			}
			needIters = append(needIters, m.iteration)
			if cum >= end {
				break
			}
		}
		if len(needIters) > 0 {
			toolsByIter, err := fetchToolsJSON(conn, tenantID, turnID, needIters)
			if err != nil {
				return nil, 0, err
			}
			// Walk the members again, slicing each one's tools into the page.
			cum = headCount
			for _, m := range members {
				segStart, segEnd := cum, cum+m.toolCount
				cum = segEnd
				if segEnd <= offset || segStart >= end {
					continue
				}
				toolsJSONStr := ""
				for i, it := range needIters {
					if it == m.iteration {
						toolsJSONStr = toolsByIter[i]
						break
					}
				}
				arr, _, err := parseToolsArray(toolsJSONStr)
				if err != nil {
					return nil, 0, err
				}
				from := offset - segStart
				if from < 0 {
					from = 0
				}
				to := end - segStart
				if to > len(arr) {
					to = len(arr)
				}
				for _, raw := range arr[from:to] {
					page = append(page, RunTool{Raw: raw, Iteration: m.iteration})
				}
			}
		}
	}
	return page, total, nil
}
