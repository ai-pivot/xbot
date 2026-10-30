package serverapp

// user_chats 的 label / sort_order 取值查询（/api/session-tree 主路径）。
//
// 背景：callbacks.go 里原先用 3 条【逐租户行执行】的相关子查询取这两列 ——
// 其中跨 channel 的 label 兜底没有前导列可用的索引，EXPLAIN QUERY PLAN 直接
// 给出 SCAN uc（每条 tenants 行扫一遍 user_chats）。改成一次 GROUP BY 预取后，
// 本文件把两件事钉死：
//
//  1) 语义不变：新查询与旧的 COALESCE 链在同一份数据上逐行等价（含
//     「同 chat_id 不同 channel」「label 为空串」「多行取最新」三类边界）。
//  2) 计划不再逐行全扫：EXPLAIN QUERY PLAN 里不出现 SCAN uc / CORRELATED。
//
// 判别力：把 callbacks.go 的查询换回旧形态，2) 必须 RED（见 G-22 验收 d）。

import (
	"database/sql"
	"strings"
	"testing"

	"xbot/config"
	"xbot/storage/sqlite"
)

// legacyUserChatsLabelSQL 是改造前的逐行相关子查询形态，只作为语义参照物
// （差分测试）与「变异对照」时的替代实现 —— 生产路径已经不再使用它。
const legacyUserChatsLabelSQL = `
  SELECT t.channel, t.id, t.chat_id, t.created_at, t.last_active_at,
          COALESCE((SELECT uc.label FROM user_chats uc
           WHERE uc.channel = t.channel AND uc.chat_id = t.chat_id AND uc.label != ''
           ORDER BY uc.rowid DESC LIMIT 1),
           (SELECT uc.label FROM user_chats uc
           WHERE uc.chat_id = t.chat_id AND uc.label != ''
           ORDER BY uc.rowid DESC LIMIT 1), '') AS label,
         COALESCE((SELECT uc.sort_order FROM user_chats uc
                  WHERE uc.channel = t.channel AND uc.chat_id = t.chat_id
                  ORDER BY uc.rowid DESC LIMIT 1), 0) AS sort_order,
         COALESCE(t.preview, '') AS preview
  FROM tenants t
  WHERE t.channel IN (?,?) AND t.chat_id != '_shared'
  ORDER BY t.last_active_at DESC`

type userChatsRow struct {
	channel, chatID, label string
	sortOrder              int
}

// openUserChatsQueryDB 用【真实 schema】（storage/sqlite 的 createSchema +
// ensureAdditiveTables）开一个临时库 —— 索引是否齐全直接反映仓库现状，
// 而不是测试自己手搓的建表语句。
func openUserChatsQueryDB(t *testing.T) (db *sql.DB, path string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	d, err := sqlite.Open(config.DBFilePath())
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.Conn(), config.DBFilePath()
}

// seedUserChatsFixtures 铺三类边界数据 + 普通数据：
//
//	tenant web/c1  → user_chats 同 chat_id 另有 cli 行            （跨 channel）
//	tenant web/c2  → 该 chat 的 label 全是空串，只有 sort_order   （label 空串）
//	tenant cli/c3  → 同 (channel, chat_id) 多行，最新一行才作数    （多行取最新）
//	tenant feishu/c4 → 完全没有 user_chats 行                     （COALESCE 兜底）
//	tenant cli/c5  → 只有别的 channel 有非空 label                （跨 channel 兜底）
func seedUserChatsFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	stmts := []string{
		// tenants
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('web','c1','2026-10-01T00:00:00Z','2026-10-05T00:00:00Z','p1')`,
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('web','c2','2026-10-01T00:00:00Z','2026-10-04T00:00:00Z','p2')`,
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('cli','c3','2026-10-01T00:00:00Z','2026-10-03T00:00:00Z','p3')`,
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('cli','c1','2026-10-01T00:00:00Z','2026-10-02T12:00:00Z','p1b')`,
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('feishu','c4','2026-10-01T00:00:00Z','2026-10-02T00:00:00Z','p4')`,
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('cli','c5','2026-10-01T00:00:00Z','2026-10-01T00:00:00Z','p5')`,
		`INSERT INTO tenants (channel, chat_id, created_at, last_active_at, preview) VALUES ('web','_shared','2026-10-01T00:00:00Z','2026-10-09T00:00:00Z','shared')`,

		// ① 同 chat_id 不同 channel：web 与 cli 各有自己的 label/sort_order
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('web','w1','c1','web-label',4)`,
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('cli','c-owner','c1','cli-label',9)`,

		// ② label 为空串：只有 sort_order 应被取到，label 退回 ''
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('web','w1','c2','',2)`,

		// ③ 多行取最新：同 (channel, chat_id) 两行，靠后者（id 更大）胜出
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('cli','c1','c3','old-label',1)`,
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('cli','c2','c3','new-label',5)`,
		// 再加一行「更新的空 label」：sort_order 应取它，label 仍取上一条非空
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('cli','c3','c3','',8)`,

		// ⑤ 只有别的 channel 有非空 label → 跨 channel 兜底
		`INSERT INTO user_chats (channel, sender_id, chat_id, label, sort_order) VALUES ('web','w1','c5','from-web',0)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

func scanUserChatsRows(t *testing.T, rows *sql.Rows) []userChatsRow {
	t.Helper()
	defer rows.Close()
	var out []userChatsRow
	for rows.Next() {
		var id int64
		var r userChatsRow
		var createdAt, lastActive, preview string
		if err := rows.Scan(&r.channel, &id, &r.chatID, &createdAt, &lastActive, &r.label, &r.sortOrder, &preview); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	return out
}

func indexUserChatsRows(rows []userChatsRow) map[string]userChatsRow {
	m := make(map[string]userChatsRow, len(rows))
	for _, r := range rows {
		m[r.channel+"/"+r.chatID] = r
	}
	return m
}

// TestUserChatsLabelSemantics 显式断言三类边界数据的期望取值。
func TestUserChatsLabelSemantics(t *testing.T) {
	db, _ := openUserChatsQueryDB(t)
	seedUserChatsFixtures(t, db)

	query, args := listTenantsByChannelsQuery([]string{"web", "cli", "feishu"})
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	got := indexUserChatsRows(scanUserChatsRows(t, rows))

	want := map[string]userChatsRow{
		// ① 同 chat_id 不同 channel：各取自己 channel 的最新非空 label
		"web/c1": {channel: "web", chatID: "c1", label: "web-label", sortOrder: 4},
		"cli/c1": {channel: "cli", chatID: "c1", label: "cli-label", sortOrder: 9},
		// ② label 为空串：label 退 ''，sort_order 仍取最新一行
		"web/c2": {channel: "web", chatID: "c2", label: "", sortOrder: 2},
		// ③ 多行取最新：label 取最新【非空】行，sort_order 取最新行（含空 label 行）
		"cli/c3": {channel: "cli", chatID: "c3", label: "new-label", sortOrder: 8},
		// ④ 无 user_chats 行：COALESCE 兜底
		"feishu/c4": {channel: "feishu", chatID: "c4", label: "", sortOrder: 0},
		// ⑤ 只有别的 channel 有非空 label：跨 channel 兜底
		"cli/c5": {channel: "cli", chatID: "c5", label: "from-web", sortOrder: 0},
	}
	if len(got) != len(want) {
		t.Fatalf("row count = %d, want %d (rows=%#v)", len(got), len(want), got)
	}
	for key, w := range want {
		g, ok := got[key]
		if !ok {
			t.Fatalf("missing row %s (got %#v)", key, got)
		}
		if g.label != w.label || g.sortOrder != w.sortOrder {
			t.Errorf("%s: label/sort_order = %q/%d, want %q/%d",
				key, g.label, g.sortOrder, w.label, w.sortOrder)
		}
	}
	// _shared 必须被过滤掉
	if _, ok := got["web/_shared"]; ok {
		t.Errorf("_shared tenant must be filtered out")
	}
}

// TestUserChatsLabelQueryMatchesLegacyCorrelatedForm 差分测试：同一份数据上，
// 新查询与旧的逐行相关子查询逐行等价 —— 这是「语义完全一致」的直接证据。
func TestUserChatsLabelQueryMatchesLegacyCorrelatedForm(t *testing.T) {
	db, _ := openUserChatsQueryDB(t)
	seedUserChatsFixtures(t, db)

	legacyRows, err := db.Query(legacyUserChatsLabelSQL, "web", "cli")
	if err != nil {
		t.Fatalf("legacy query: %v", err)
	}
	legacy := indexUserChatsRows(scanUserChatsRows(t, legacyRows))

	query, args := listTenantsByChannelsQuery([]string{"web", "cli"})
	newRows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("new query: %v", err)
	}
	current := indexUserChatsRows(scanUserChatsRows(t, newRows))

	if len(legacy) != len(current) {
		t.Fatalf("row count differs: legacy=%d new=%d", len(legacy), len(current))
	}
	for key, l := range legacy {
		c, ok := current[key]
		if !ok {
			t.Fatalf("row %s missing in new query result", key)
		}
		if l != c {
			t.Errorf("%s: legacy=%#v new=%#v", key, l, c)
		}
	}
}

// explainQueryPlan 返回 EXPLAIN QUERY PLAN 的 detail 行（单行拼在一起便于断言，
// 也便于 t.Logf 落证据）。
func explainQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan: %v", err)
		}
		lines = append(lines, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	return lines
}

// TestUserChatsQueryPlanHasNoPerRowScan 钉住查询计划：user_chats 只能被预取一次，
// 不允许出现逐租户行执行的相关子查询。
func TestUserChatsQueryPlanHasNoPerRowScan(t *testing.T) {
	db, _ := openUserChatsQueryDB(t)
	seedUserChatsFixtures(t, db)

	cases := []struct {
		name  string
		query string
		args  []any
	}{
		{"listTenantsByChannel", listTenantsByChannelSQL, []any{"web"}},
		{"listTenantsByChannels", func() string {
			q, _ := listTenantsByChannelsQuery([]string{"cli", "web", "feishu"})
			return q
		}(), []any{"cli", "web", "feishu"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explainQueryPlan(t, db, tc.query, tc.args...)
			joined := strings.Join(plan, "\n")
			for _, line := range plan {
				t.Logf("PLAN| %s", line)
			}

			if strings.Contains(joined, "SCAN uc") {
				t.Errorf("query plan still scans user_chats per row (SCAN uc):\n%s", joined)
			}
			if strings.Contains(joined, "CORRELATED") {
				t.Errorf("query plan still has correlated subquery:\n%s", joined)
			}
			if !strings.Contains(joined, "idx_user_chats_channel_chat") {
				t.Errorf("expected plan to use idx_user_chats_channel_chat:\n%s", joined)
			}
		})
	}
}

// TestUserChatsLookupIndexSurvivesReopen 验证索引补齐是幂等的：
// ① 新建库带索引；② 手动 DROP 后重新 open 自动补回；③ 再 open 一次不报错。
func TestUserChatsLookupIndexSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XBOT_HOME", dir)
	path := config.DBFilePath()

	hasIndex := func(t *testing.T, db *sql.DB) bool {
		t.Helper()
		var n string
		err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='idx_user_chats_channel_chat'`).Scan(&n)
		if err == sql.ErrNoRows {
			return false
		}
		if err != nil {
			t.Fatalf("index lookup: %v", err)
		}
		return true
	}

	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	if !hasIndex(t, db.Conn()) {
		t.Fatalf("fresh database lacks idx_user_chats_channel_chat")
	}
	// 模拟「生产式漂移」：库已存在但索引被删掉
	if _, err := db.Conn().Exec(`DROP INDEX idx_user_chats_channel_chat`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// 重新 open：ensureAdditiveTables 应把它补回来
	db2, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !hasIndex(t, db2.Conn()) {
		t.Fatalf("index was not restored on reopen")
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("close2: %v", err)
	}

	// 第三次 open：索引已存在，重复执行不报错
	db3, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("third open (idempotent ensure): %v", err)
	}
	t.Cleanup(func() { _ = db3.Close() })
	if !hasIndex(t, db3.Conn()) {
		t.Fatalf("index disappeared on third open")
	}
}
