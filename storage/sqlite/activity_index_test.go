package sqlite

// activity_index_test.go — 跨会话活动流索引的守护测试（通信视图性能重构）。
//
// 覆盖四件事：
//  1. 迁移 v76→v77 真的建出表/索引，且**幂等**（重跑/老库升级都安全）；
//  2. 写入幂等（同一 (tenant, chat, message) 只一行）与「只索引结构化 kind」；
//  3. 规模测试：2000 行活动 + 200 行子代理下的过滤与排序断言 + 延迟数字；
//  4. 索引体积（2000 行占多少 KB）—— 数字进报告，不做硬断言（与驱动/页大小相关）。

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ── 迁移 ────────────────────────────────────────────────────────────────────

// TestMigrationV77CreatesActivityIndex：v76 的老库升级后必须同时拿到
// activity_index（表 + 3 个索引）和 interactive_agents.last_reply_at。
func TestMigrationV77CreatesActivityIndex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v76.db")

	// 造一个「v76 形态」的库：有 interactive_agents 但**没有** last_reply_at，
	// 也没有 activity_index；版本号停在 76（升级前的真实形态）。
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	// 注意：initSchema 用「tenants 表是否存在」判定全新库，所以 fixture 必须带上它，
	// 否则会被当成 fresh DB 走 createSchema（撞已存在的 schema_version）。
	if _, err := raw.Exec(`
		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			UNIQUE(channel, chat_id)
		);
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version(version) VALUES (76);
		CREATE TABLE interactive_agents (
			agent_key       TEXT PRIMARY KEY,
			parent_channel  TEXT NOT NULL,
			parent_chat_id  TEXT NOT NULL,
			role_name       TEXT NOT NULL,
			instance        TEXT NOT NULL DEFAULT '',
			group_id        TEXT NOT NULL DEFAULT '',
			task            TEXT NOT NULL DEFAULT '',
			background      INTEGER NOT NULL DEFAULT 0,
			status          TEXT NOT NULL DEFAULT 'idle',
			turn_id         INTEGER NOT NULL DEFAULT 0,
			model_name      TEXT NOT NULL DEFAULT '',
			sub_id          TEXT NOT NULL DEFAULT '',
			origin_user_id  TEXT NOT NULL DEFAULT '',
			workspace_root  TEXT NOT NULL DEFAULT '',
			parent_agent_id TEXT NOT NULL DEFAULT '',
			last_error      TEXT NOT NULL DEFAULT '',
			last_reply      TEXT NOT NULL DEFAULT '',
			created_at      TEXT NOT NULL,
			updated_at      TEXT NOT NULL
		);
		INSERT INTO interactive_agents(agent_key, parent_channel, parent_chat_id, role_name, created_at, updated_at)
			VALUES ('agent:old/i1', 'web', 'web-1', 'old', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z');
	`); err != nil {
		_ = raw.Close()
		t.Fatalf("create v76 fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate v76→v77): %v", err)
	}
	defer db.Close()

	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 77 {
		t.Fatalf("schema version = %d, want 77", version)
	}
	if ok, err := tableExists(db.Conn(), "activity_index"); err != nil || !ok {
		t.Fatalf("activity_index missing after migration (err=%v)", err)
	}
	for _, ix := range []string{"idx_activity_created", "idx_activity_chat", "idx_activity_kind"} {
		var name string
		err := db.Conn().QueryRow(
			"SELECT name FROM sqlite_master WHERE type='index' AND name=?", ix).Scan(&name)
		if err != nil {
			t.Fatalf("index %s missing after migration: %v", ix, err)
		}
	}
	if ok, err := columnExists(db.Conn(), "interactive_agents", "last_reply_at"); err != nil || !ok {
		t.Fatalf("interactive_agents.last_reply_at missing after migration (err=%v)", err)
	}
	// 存量数据必须保留（迁移只加列/加表，不动行）。
	var n int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM interactive_agents").Scan(&n); err != nil {
		t.Fatalf("count interactive_agents: %v", err)
	}
	if n != 1 {
		t.Fatalf("interactive_agents rows = %d, want 1 (migration must not drop rows)", n)
	}
}

// TestMigrationV76ToV77Idempotent：同一迁移重跑两次不报错、不重复、不丢数据。
func TestMigrationV76ToV77Idempotent(t *testing.T) {
	db := openTestDB(t)
	// 先灌一行，验证重跑不会清表。
	if err := NewActivityIndex(db.Conn()).Insert(ActivityItem{
		TenantID: 1, ChatID: "c1", Channel: "web", MessageID: 7,
		Kind: StructuredKindReport, Summary: "先写一行",
	}); err != nil {
		t.Fatalf("insert before rerun: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := migrateV76ToV77(db); err != nil {
			t.Fatalf("rerun #%d: %v", i+1, err)
		}
	}
	var n int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM activity_index").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("activity_index rows after 2 reruns = %d, want 1", n)
	}
	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("version: %v", err)
	}
	if version != 77 {
		t.Fatalf("version = %d, want 77", version)
	}
}

// TestActivityIndexInsertIsIdempotentAndSkipsText：UNIQUE 去重 + text 不入索引。
func TestActivityIndexInsertIsIdempotentAndSkipsText(t *testing.T) {
	db := openTestDB(t)
	idx := NewActivityIndex(db.Conn())
	rec := ActivityItem{TenantID: 1, ChatID: "c1", Channel: "web", MessageID: 9,
		Kind: StructuredKindDecision, SenderName: "main", Summary: "准。"}
	for i := 0; i < 3; i++ {
		if err := idx.Insert(rec); err != nil {
			t.Fatalf("insert #%d: %v", i+1, err)
		}
	}
	if err := idx.Insert(ActivityItem{TenantID: 1, ChatID: "c1", Channel: "web",
		MessageID: 10, Kind: StructuredKindText, Summary: "普通聊天不该进索引"}); err != nil {
		t.Fatalf("insert text: %v", err)
	}
	n, err := idx.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1 (3 dup inserts + 1 text insert must collapse)", n)
	}
}

// ── 规模测试 ────────────────────────────────────────────────────────────────

// seedActivityScale 造 2000 行活动（1900 条落在 72h 内 + 100 条落在 30 天前）
// 和 200 行子代理（20 行 unloaded、30 行 90 天未活跃、40 行 5 天前活跃）。
func seedActivityScale(t *testing.T, idx *ActivityIndex, svc *InteractiveAgentService) (now time.Time) {
	t.Helper()
	now = time.Now().UTC()
	conn := idx.db
	tx, err := conn.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT INTO activity_index
		(tenant_id, chat_id, channel, message_id, kind, sender_id, sender_name, summary, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer func() { _ = stmt.Close() }()

	kinds := []string{StructuredKindReport, StructuredKindDecision, StructuredKindAskUser,
		StructuredKindAnswer, StructuredKindCommand, StructuredKindEvent}
	// 1900 行：最近 1900 分钟，全部落在 72h 窗口内。
	for i := 0; i < 1900; i++ {
		ts := now.Add(-time.Duration(i) * time.Minute)
		if _, err := stmt.Exec(int64(1+i%40), fmt.Sprintf("chat-%02d", i%40), "web", i+1,
			kinds[i%len(kinds)], "sender", "部门", fmt.Sprintf("摘要 %d", i), FormatActivityTime(ts)); err != nil {
			t.Fatalf("seed activity row %d: %v", i, err)
		}
	}
	// 100 行：30 天前（默认 72h 窗口必须把它们排除）。
	for i := 0; i < 100; i++ {
		ts := now.Add(-30 * 24 * time.Hour).Add(time.Duration(i) * time.Minute)
		if _, err := stmt.Exec(int64(1), "chat-old", "web", 100000+i,
			StructuredKindReport, "sender", "旧部门", "30 天前的奏折", FormatActivityTime(ts)); err != nil {
			t.Fatalf("seed old activity row %d: %v", i, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit activity seed: %v", err)
	}

	// 200 行子代理。
	tx2, err := conn.Begin()
	if err != nil {
		t.Fatalf("begin agents: %v", err)
	}
	defer func() { _ = tx2.Rollback() }()
	astmt, err := tx2.Prepare(`INSERT INTO interactive_agents
		(agent_key, parent_channel, parent_chat_id, role_name, instance, status,
		 last_reply, last_reply_at, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		t.Fatalf("prepare agents: %v", err)
	}
	defer func() { _ = astmt.Close() }()
	// updated_at/last_reply_at 用 UTC RFC3339 —— 与 Upsert 写入的形态完全一致
	// （ListActive 的截止时间也按同一 layout 格式化后做字符串比较，两处必须是
	// 同一种宽度，否则比较口径就错了）。
	rf := func(ts time.Time) string { return ts.UTC().Format(time.RFC3339) }
	add := func(key string, status string, updated time.Time) {
		if _, err := astmt.Exec(key, "web", "web-1", "court.gongbu", key,
			status, "回执", rf(updated), rf(now.Add(-200*24*time.Hour)), rf(updated)); err != nil {
			t.Fatalf("seed agent %s: %v", key, err)
		}
	}
	for i := 0; i < 110; i++ { // 最近活跃
		add(fmt.Sprintf("agent:hot/%02d", i), InteractiveAgentIdle, now.Add(-time.Duration(i)*time.Minute))
	}
	for i := 0; i < 40; i++ { // 5 天前：超过 72h 窗口，但不是 90 天
		add(fmt.Sprintf("agent:stale/%02d", i), InteractiveAgentIdle, now.Add(-5*24*time.Hour))
	}
	for i := 0; i < 30; i++ { // 90 天前
		add(fmt.Sprintf("agent:ancient/%02d", i), InteractiveAgentIdle, now.Add(-90*24*time.Hour))
	}
	for i := 0; i < 20; i++ { // 已经 unload 的（都在最近）
		add(fmt.Sprintf("agent:gone/%02d", i), InteractiveAgentUnloaded, now.Add(-time.Duration(i)*time.Minute))
	}
	if err := tx2.Commit(); err != nil {
		t.Fatalf("commit agents seed: %v", err)
	}
	return now
}

// percentile 返回升序排好的样本数组的 p 分位（线性插值，够用即可）。
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	pos := p * float64(len(sorted)-1)
	lo := int(pos)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := pos - float64(lo)
	return time.Duration(float64(sorted[lo]) + frac*float64(sorted[hi]-sorted[lo]))
}

// TestActivityIndexScaleQuery：2000 行规模下 /api/activity 的查询语义与延迟。
//
// 断言：
//   - limit=200 返回恰好 200 条，按 created_at 严格倒序；
//   - 默认 72h 窗口把 30 天前的行排除；
//   - limit 上限被夹到 500（挡 ?limit=999999）；
//   - kind 过滤只返回指定 kind；
//   - 延迟 P99 < 50ms（这是本次重构的验收数字）。
func TestActivityIndexScaleQuery(t *testing.T) {
	db := openTestDB(t)
	idx := NewActivityIndex(db.Conn())
	svc := NewInteractiveAgentService(db.Conn())
	now := seedActivityScale(t, idx, svc)

	total, err := idx.Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2000 {
		t.Fatalf("seeded rows = %d, want 2000", total)
	}

	// ① 条数与排序。
	items, hasMore, err := idx.List(200, 72*time.Hour, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 200 {
		t.Fatalf("limit=200 returned %d items, want 200", len(items))
	}
	if !hasMore {
		t.Fatal("has_more = false, want true (2000 rows available)")
	}
	cutoff := now.Add(-72 * time.Hour)
	prev := time.Time{}
	for i, it := range items {
		ts, err := ParseActivityTime(it.CreatedAt)
		if err != nil {
			t.Fatalf("item %d: parse created_at: %v", i, err)
		}
		if ts.Before(cutoff) {
			t.Fatalf("item %d created_at %s is older than the 72h window (cutoff %s)",
				i, it.CreatedAt, cutoff.Format(activityTimeLayout))
		}
		if i > 0 && ts.After(prev) {
			t.Fatalf("items not DESC by created_at: item %d (%s) is newer than item %d (%s)",
				i, it.CreatedAt, i-1, prev.Format(activityTimeLayout))
		}
		prev = ts
	}

	// ② 默认 limit / 上限夹取。
	if items, _, err := idx.List(0, 72*time.Hour, nil); err != nil || len(items) != ActivityLimitDefault {
		t.Fatalf("limit=0 → %d items (err=%v), want default %d", len(items), err, ActivityLimitDefault)
	}
	if items, _, err := idx.List(999999, 72*time.Hour, nil); err != nil || len(items) != ActivityLimitMax {
		t.Fatalf("limit=999999 → %d items (err=%v), want clamp to %d", len(items), err, ActivityLimitMax)
	}

	// ③ 窗口：30 天前的行只在更大窗口里出现。
	wide, _, err := idx.List(500, 60*24*time.Hour, []string{StructuredKindReport})
	if err != nil {
		t.Fatalf("wide List: %v", err)
	}
	foundOld := false
	for _, it := range wide {
		if it.ChatID == "chat-old" {
			foundOld = true
		}
		if it.Kind != StructuredKindReport {
			t.Fatalf("kind filter leaked %q", it.Kind)
		}
	}
	if !foundOld {
		t.Fatal("60d window did not surface 30-day-old rows")
	}

	// ④ 延迟（P50 / P99，200 次采样）。
	durations := make([]time.Duration, 0, 200)
	for i := 0; i < 200; i++ {
		start := time.Now()
		if _, _, err := idx.List(200, 72*time.Hour, nil); err != nil {
			t.Fatalf("timed List: %v", err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	p50 := percentile(durations, 0.50)
	p99 := percentile(durations, 0.99)
	t.Logf("activity List(limit=200) over 2000 rows: P50=%v P99=%v max=%v",
		p50, p99, durations[len(durations)-1])
	if p99 > 50*time.Millisecond {
		t.Fatalf("P99 latency %v exceeds the 50ms budget", p99)
	}
}

// TestInteractiveAgentsListActive：过滤断言（unloaded 与过老的行必须消失）。
func TestInteractiveAgentsListActive(t *testing.T) {
	db := openTestDB(t)
	idx := NewActivityIndex(db.Conn())
	svc := NewInteractiveAgentService(db.Conn())
	now := seedActivityScale(t, idx, svc)

	assertNoViolations := func(t *testing.T, rows []AgentActivity, includeUnloaded bool) {
		t.Helper()
		prev := ""
		for i, r := range rows {
			if !includeUnloaded && r.Status == InteractiveAgentUnloaded {
				t.Fatalf("row %d (%s) is unloaded but include_unloaded=false", i, r.AgentKey)
			}
			ts, err := time.Parse(time.RFC3339, r.UpdatedAt)
			if err != nil {
				t.Fatalf("row %d updated_at %q: %v", i, r.UpdatedAt, err)
			}
			if ts.Before(now.Add(-72 * time.Hour)) {
				t.Fatalf("row %d (%s) updated_at %s is older than the 72h window",
					i, r.AgentKey, r.UpdatedAt)
			}
			if i > 0 && r.UpdatedAt > prev {
				t.Fatalf("rows not DESC by updated_at at %d: %s > %s", i, r.UpdatedAt, prev)
			}
			prev = r.UpdatedAt
		}
	}

	cutoff := now.Add(-72 * time.Hour)
	rows, err := svc.ListActive(cutoff, false)
	if err != nil {
		t.Fatalf("ListActive: %v", err)
	}
	// 110 个最近活跃 + 20 unloaded（排除）+ 40 个 5 天前（窗口外）+ 30 个 90 天前（窗口外）
	if len(rows) != 110 {
		t.Fatalf("active agents = %d, want 110 (unloaded + out-of-window must be filtered)", len(rows))
	}
	assertNoViolations(t, rows, false)
	for _, r := range rows {
		if r.Status == InteractiveAgentUnloaded {
			t.Fatalf("unloaded agent %s leaked into the default view", r.AgentKey)
		}
		if len(r.AgentKey) > 11 && r.AgentKey[:11] == "agent:gone/" {
			t.Fatalf("unloaded agent %s leaked into the default view", r.AgentKey)
		}
		if len(r.AgentKey) > 14 && r.AgentKey[:14] == "agent:ancient/" {
			t.Fatalf("90-day-old agent %s leaked into the 72h view", r.AgentKey)
		}
	}

	withUnloaded, err := svc.ListActive(cutoff, true)
	if err != nil {
		t.Fatalf("ListActive(include_unloaded): %v", err)
	}
	if len(withUnloaded) != 130 {
		t.Fatalf("include_unloaded=true → %d rows, want 130 (110 + 20 unloaded)", len(withUnloaded))
	}
	assertNoViolations(t, withUnloaded, true)

	// 更宽的窗口（120d）必须把 90 天前的行带回来，但仍然是倒序。
	wide, err := svc.ListActive(now.Add(-120*24*time.Hour), false)
	if err != nil {
		t.Fatalf("ListActive(wide): %v", err)
	}
	if len(wide) != 180 {
		t.Fatalf("120d window → %d rows, want 180 (110 + 40 + 30)", len(wide))
	}
	ancient := 0
	for _, r := range wide {
		if len(r.AgentKey) > 14 && r.AgentKey[:14] == "agent:ancient/" {
			ancient++
		}
	}
	if ancient != 30 {
		t.Fatalf("90-day-old agents in 120d window = %d, want 30", ancient)
	}
}

// TestActivityIndexFootprint：2000 行索引占多少空间（数字进报告，不做硬断言）。
//
// 两种摘要在**各自独立的库**里测：同一库里 DELETE 再 INSERT 会复用 freelist 页，
// 量出来的增量是 0（假数字）。所以每次都开一个全新库。
func TestActivityIndexFootprint(t *testing.T) {
	measure := func(t *testing.T, label string, summaryOf func(i int) string) int64 {
		t.Helper()
		dir := t.TempDir()
		db, err := Open(filepath.Join(dir, "xbot.db"))
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		defer db.Close()
		dirSize := func() int64 {
			var total int64
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatalf("read dir: %v", err)
			}
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				if info, err := e.Info(); err == nil {
					total += info.Size()
				}
			}
			return total
		}
		checkpoint := func() {
			if _, err := db.Conn().Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
				t.Fatalf("checkpoint: %v", err)
			}
		}
		checkpoint()
		empty := dirSize()

		now := time.Now().UTC()
		tx, err := db.Conn().Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		stmt, err := tx.Prepare(`INSERT INTO activity_index
			(tenant_id, chat_id, channel, message_id, kind, sender_id, sender_name, summary, created_at)
			VALUES (?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		for i := 0; i < 2000; i++ {
			if _, err := stmt.Exec(int64(1+i%40), fmt.Sprintf("chat-%02d", i%40), "web", i+1,
				StructuredKindReport, "sender", "工部", TruncateActivitySummary(summaryOf(i)),
				FormatActivityTime(now.Add(-time.Duration(i)*time.Minute))); err != nil {
				t.Fatalf("seed %d: %v", i, err)
			}
		}
		_ = stmt.Close()
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		checkpoint()
		delta := dirSize() - empty
		if n, err := NewActivityIndex(db.Conn()).Count(); err != nil || n != 2000 {
			t.Fatalf("rows = %d (err=%v), want 2000", n, err)
		}
		t.Logf("activity_index 2000 rows (%s): +%d bytes (%.1f KB), db total %.1f KB",
			label, delta, float64(delta)/1024, float64(dirSize())/1024)
		return delta
	}

	// 最坏情况：200 个中文字（约 600 字节摘要）。
	worst := measure(t, "200 个中文字摘要（最坏情况）", func(i int) string {
		return fmt.Sprintf("第 %d 条部门奏折摘要 %s", i, strings.Repeat("内容", 80))
	})
	// 典型情况：一句话结论（约 30 字）。
	typical := measure(t, "一句话结论摘要（典型情况）", func(i int) string {
		return fmt.Sprintf("第 %d 条：六部巡检完成，机器台账无异常。", i)
	})
	if worst <= 0 || typical <= 0 {
		t.Fatalf("footprint measurement failed (worst=%d, typical=%d)", worst, typical)
	}
	if typical > worst {
		t.Fatalf("typical summaries (%d B) larger than worst-case (%d B) — measurement is inverted", typical, worst)
	}
}
