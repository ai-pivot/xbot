package xbot

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func TestCJKSpacing(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"pure chinese", "记忆系统", "记 忆 系 统"},
		{"mixed cn+en", "GLM模型部署", "GLM 模 型 部 署"},
		{"english only", "GLM DCP HiCache", "GLM DCP HiCache"},
		{"cn phrase with punct", "跨会话记忆。很强大", "跨 会 话 记 忆。很 强 大"},
		{"empty", "", ""},
		{"single cjk", "记", "记"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cjkSpaceRuns(tt.in)
			if got != tt.want {
				t.Errorf("cjkSpaceRuns(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFts5SafeQueryChinese(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"chinese substring", "记忆", `"记" AND "忆"`},
		{"chinese 3 char", "跨会话", `"跨" AND "会" AND "话"`},
		{"mixed cn+en", "GLM模型", `"GLM" AND "模" AND "型"`},
		{"english", "GLM DCP", `"GLM" AND "DCP"`},
		{"empty", "  ", `""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fts5SafeQuery(tt.in)
			if got != tt.want {
				t.Errorf("fts5SafeQuery(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestBuildSearchText(t *testing.T) {
	got := buildSearchText("记忆系统很强大", "GLM, DCP", "部署")
	want := "记 忆 系 统 很 强 大 GLM, DCP 部 署"
	if got != want {
		t.Errorf("buildSearchText() = %q, want %q", got, want)
	}
}

func TestFts5SafeQueryLongMessage(t *testing.T) {
	// A pasted document / log dump must not blow up into an unbounded MATCH
	// expression or truncate a UTF-8 char mid-sequence.
	long := "这是一个非常长的中文文档，包含大量无意义内容。" + string(make([]rune, 5000))
	q := fts5SafeQuery(long)
	if q == "" {
		t.Fatal("long query produced empty MATCH expression")
	}
	// Token cap: no more than maxQueryTokens AND terms.
	tokens := strings.Count(q, " AND ") + 1
	if tokens > 25 {
		t.Errorf("long query produced %d tokens, want <= 25 (capped)", tokens)
	}
	// Must be valid FTS5: every token is a quoted literal, no raw operators.
	if !strings.HasPrefix(q, `"`) || !strings.HasSuffix(q, `"`) {
		t.Errorf("query not fully quoted: %q", q)
	}
}

// TestFts5SearchMultiTermRecall 复现用户 bug：多词查询中只要有一个词
// 不在记忆里（"frpc" vs 记忆里的 "frps"；"机器/地址/转发" 记忆里没有），
// AND 语义就让整个查询零结果 —— 即使高区分度词 "2008" 精确存在。
// 搜索是 BM25 排序场景，召回必须用 OR（任一词命中即可召回，BM25 负责
// 把多词命中/高 IDF 的排前面）；AND 只能留给 dedup 相似度判定。
func TestFts5SearchMultiTermRecall(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE VIRTUAL TABLE fts USING fts5(search_text)`); err != nil {
		t.Fatal(err)
	}
	// 复刻 buildSearchText 的 CJK 空格化（content + keywords 都进索引）。
	content := cjkSpaceRuns("Server 'myserver' = root@43.154.191.136:2008 (shell alias myserver). It hosts frps (port 7077), a Minecraft Fabric server, and xbot-related services.")
	if _, err := db.Exec(`INSERT INTO fts(search_text) VALUES (?)`, content); err != nil {
		t.Fatal(err)
	}
	// 无关记忆（干扰项，验证排序不崩）。
	if _, err := db.Exec(`INSERT INTO fts(search_text) VALUES (?)`, cjkSpaceRuns("用户偏好深色主题 dark theme")); err != nil {
		t.Fatal(err)
	}

	query := "2008 机器 IP 地址 frpc 转发" // frpc/机器/地址/转发 都不在记忆里
	for _, tc := range []struct {
		name string
		expr string
	}{
		{"AND (旧语义)", fts5SafeQuery(query)},
		{"OR (新语义)", fts5OrQuery(query)},
	} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM fts WHERE fts MATCH ?`, tc.expr).Scan(&n); err != nil {
			t.Fatalf("%s: MATCH 语法错误: %v", tc.name, err)
		}
		t.Logf("%s: %q → 召回 %d 条", tc.name, tc.expr, n)
		if tc.name == "OR (新语义)" && n == 0 {
			t.Errorf("BUG: OR 语义下查询 %q 召回 0 条 —— 含 '2008' 的记忆必须可召回", query)
		}
	}

	// 单词查询（回归保护）："2008" 单独可命中。
	if err := db.QueryRow(`SELECT count(*) FROM fts WHERE fts MATCH ?`, fts5OrQuery("2008")).Scan(new(int)); err != nil {
		t.Errorf("单词 OR 查询语法错误: %v", err)
	}
}

func TestTruncateRunes(t *testing.T) {
	// Chinese 3-byte chars must not be split mid-sequence.
	s := "记忆系统很强大"
	got := truncateRunes(s, 3)
	if got != "记忆系..." {
		t.Errorf("truncateRunes(%q, 3) = %q, want %q", s, got, "记忆系...")
	}
	// Short string unchanged.
	if truncateRunes("ab", 5) != "ab" {
		t.Error("short string should be unchanged")
	}
	// Max <= 0 → empty.
	if truncateRunes("abc", 0) != "" {
		t.Error("max 0 should return empty")
	}
}

// newTestMemory builds an XbotMemory on an in-memory SQLite DB.
func newTestMemory(t *testing.T) (*XbotMemory, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	m := New(42, 7, t.TempDir(), db)
	t.Cleanup(func() { db.Close() })
	return m, db
}

func seedTestMemory(t *testing.T, m *XbotMemory) {
	t.Helper()
	// Long-term memory with an identifiable content string.
	if _, err := m.AddMemory(t.Context(), LongTermMemory{
		Type:     "fact",
		Content:  "用户使用 GLM 模型部署在 B300 集群",
		Keywords: "GLM 模型 部署",
	}); err != nil {
		t.Fatal(err)
	}
	// Short-term (session summary) — must NOT be injected when query is empty.
	if err := m.addShortTermMemory("另一个无关会话的总结：frpc 端口转发配置", "frpc 转发", "sess-other"); err != nil {
		t.Fatal(err)
	}
}

// TestRecallEmptyQuerySkipsShortTerm: query 为空时不得注入其他 session 的
// short-term 摘要（recentShortTerm 是无锚点的"别的会话上下文"）。
func TestRecallEmptyQuerySkipsShortTerm(t *testing.T) {
	m, _ := newTestMemory(t)
	seedTestMemory(t, m)

	out, err := m.Recall(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "## Recent Sessions") {
		t.Errorf("empty query must NOT inject other-session short-term summaries\n%q", out)
	}
	if !strings.Contains(out, "## Long-term Memories") {
		t.Errorf("empty query should still inject relevant long-term memories\n%q", out)
	}
}

// TestRecallWithQuerySkipsShortTerm: short-term（其他会话的摘要）永不自动注入——
// 即便 query 命中（2026-09-02 会话隔离重设计）。跨会话内容进入本会话的唯一
// 路径是 memory_search 按需检索；自动注入曾是"另一个会话的压缩摘要污染无关
// 会话"的根源（query 命中即注入 searchShortTerm 的全局 BM25）。
func TestRecallWithQuerySkipsShortTerm(t *testing.T) {
	m, _ := newTestMemory(t)
	seedTestMemory(t, m)

	out, err := m.Recall(t.Context(), "frpc 转发", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "## Recent Sessions") {
		t.Errorf("BUG REPRODUCED: query-anchored cross-session short-term injection — another session's summary must NEVER auto-inject (session isolation)\n%q", out)
	}
	if strings.Contains(out, "另一个无关会话的总结") {
		t.Errorf("BUG REPRODUCED: other session's short-term summary leaked into Recall\n%q", out)
	}
}

// TestRecallCapsTotalRunes: 总量超 recallMaxRunes 时必须截断（注意力预算）。
func TestRecallCapsTotalRunes(t *testing.T) {
	m, _ := newTestMemory(t)
	// 大量长记忆塞满注入内容（远超 recallMaxRunes=3000）。
	long := strings.Repeat("这是一个非常长的记忆内容用于测试注意力预算截断，反复出现以撑爆注入预算。", 100)
	if _, err := m.AddMemory(t.Context(), LongTermMemory{
		Type:    "fact",
		Content: long,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Recall(t.Context(), "注意力预算", "")
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(out)); n > recallMaxRunes+100 {
		t.Errorf("injected runes = %d, want <= %d (hard cap)", n, recallMaxRunes)
	}
	if !strings.Contains(out, "memory truncated to budget") {
		t.Errorf("over-budget recall should carry the truncation marker\nfirst 200: %q", out[:min(200, len(out))])
	}
}

// TestAddLongTermMemoryBM25DedupDirection: 去重阈值方向必须是"越负越相似"。
// SQLite FTS5 bm25() 返回负值，better matches are assigned numerically LOWER
// scores（官方语义：ORDER BY bm25() ASC 把最佳匹配排最前——searchLongTerm 即此用法）。
// 强 keyword 重叠（bm25 << dedupSimilarityThreshold=-6）判 duplicate skip；
// 未达阈值的弱相关不误去重。实测（zz_probe）：真实语料（9 行）强重叠 bm25≈-9.9，
// 1 行语料 bm25≈0（IDF 无区分度）——测试必须种 filler 行到真实规模。
// 回归：旧代码 `bm25() > ?` + `ORDER BY DESC` 方向反了——强重叠不去重（内存膨胀），
// 弱相似反而被误 skip（丢新记忆）。
// 注意：去重在私有 addLongTermMemory（LLM 记忆管道路径）；公开 AddMemory 是
// 显式添加 API，设计上不去重。
func TestAddLongTermMemoryBM25DedupDirection(t *testing.T) {
	m, db := newTestMemory(t)

	// Seed 8 filler rows so the FTS corpus has realistic IDF (bm25() on a
	// 1-row corpus ≈ 0 — no discrimination; filler keywords are pairwise
	// disjoint so they don't dedup each other).
	for i := 0; i < 8; i++ {
		if err := m.addLongTermMemory(LongTermMemory{
			Type:     "fact",
			Content:  fmt.Sprintf("filler row %d unrelated words", i),
			Keywords: fmt.Sprintf("filler%d unique%d", i, i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// 场景 1：强重叠（同 keywords 全部 token 命中）→ duplicate skip
	kw := "alpha beta gamma delta epsilon zeta"
	if err := m.addLongTermMemory(LongTermMemory{
		Type: "fact", Content: "first memory", Keywords: kw,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.addLongTermMemory(LongTermMemory{
		Type: "fact", Content: "second memory nearly identical", Keywords: kw,
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM xbot_long_term_memories`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 9 { // 8 filler + 1 (duplicate skipped)
		t.Errorf("strong-overlap duplicate was not deduped: rows=%d, want 9 (bm25 lower = more relevant, threshold %v)", count, dedupSimilarityThreshold)
	}

	// 场景 2：弱相关（无共同 token）→ 不误去重
	if err := m.addLongTermMemory(LongTermMemory{
		Type: "fact", Content: "unrelated memory", Keywords: "database backup schedule",
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM xbot_long_term_memories`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 10 { // 8 filler + 1 alpha + 1 unrelated
		t.Errorf("weak-similarity memory was incorrectly deduped: rows=%d, want 10", count)
	}
}
