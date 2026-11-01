package xbot

// redact_write_test.go — 写入点判据（W1 镜像 / W2 长时+短时记忆表 / W3 core summary）。
//
// 全部使用**假 key**（sk-BINGBUFAKE…，与兵部探针同款），不含任何真实凭据。
// 与兵部探针的区别：探针只验「不落盘 + 仍可检索」，这里额外验 SearchText（索引副本）、
// UpdateMemory 的搜索文本、以及非凭据文本逐字节不变。

import (
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const w4FakeKey = "sk-BINGBUFAKE0123456789abcdefghijklmnopqrstuvwxyz0123"

const w4Marker = "PROBEMARKER-W1W2W3"

// itoa 把记忆 id 变文件名后缀（notes/<type>_<id>.md）。
func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// scanString 取单列值（测试专用）。
func scanString(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	var s string
	if err := db.QueryRow(query, args...).Scan(&s); err != nil {
		t.Fatalf("query %q: %v", query, err)
	}
	return s
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// assertNoLeak 断言 s 不含假 key，且保留标记词（脱敏不得把内容吞掉）。
func assertNoLeak(t *testing.T, label, s string) {
	t.Helper()
	if strings.Contains(s, w4FakeKey) {
		t.Errorf("%s 仍含假 key: %q", label, s)
	}
	if !strings.Contains(s, w4Marker) {
		t.Errorf("%s 丢了标记词（过度脱敏）: %q", label, s)
	}
}

func TestAddMemoryRedactsAllColumnsAndSearchText(t *testing.T) {
	m, db := newTestMemory(t)

	content := w4Marker + " 假凭据：Bearer " + w4FakeKey + " 以及 api_key=" + w4FakeKey
	id, err := m.AddMemory(t.Context(), LongTermMemory{
		Type:     "fact",
		Content:  content,
		Keywords: w4Marker + " token=" + w4FakeKey,
		Tags:     w4Marker + " secret:" + w4FakeKey,
	})
	if err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	// W1 镜像文件
	mirror := mustRead(t, filepath.Join(m.baseDir, "notes", "fact_"+itoa(id)+".md"))
	assertNoLeak(t, "mirror note", mirror)

	// W2 列 + SearchText（索引副本）
	var gotContent, gotKeywords, gotTags, gotSearch string
	if err := db.QueryRow(
		`SELECT content, keywords, tags, search_text FROM xbot_long_term_memories WHERE id = ?`, id,
	).Scan(&gotContent, &gotKeywords, &gotTags, &gotSearch); err != nil {
		t.Fatalf("select memory row: %v", err)
	}
	assertNoLeak(t, "content 列", gotContent)
	assertNoLeak(t, "keywords 列", gotKeywords)
	assertNoLeak(t, "tags 列", gotTags)
	assertNoLeak(t, "search_text 列", gotSearch)

	// 不脱敏过度：按标记词仍能检索到
	hits, err := m.searchLongTerm(w4Marker, 5)
	if err != nil {
		t.Fatalf("searchLongTerm: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("标记词检索不到（过度脱敏）")
	}
}

func TestUpdateMemoryRedactsColumnsAndSearchText(t *testing.T) {
	m, db := newTestMemory(t)
	id, err := m.AddMemory(t.Context(), LongTermMemory{Type: "fact", Content: w4Marker + " 初始内容"})
	if err != nil {
		t.Fatalf("AddMemory: %v", err)
	}

	if err := m.UpdateMemory(t.Context(), id, w4Marker+" 更新后 Bearer "+w4FakeKey,
		w4Marker+" api_key="+w4FakeKey, w4Marker+" cookie="+w4FakeKey, 0.9); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}

	var gotContent, gotKeywords, gotTags, gotSearch string
	if err := db.QueryRow(
		`SELECT content, keywords, tags, search_text FROM xbot_long_term_memories WHERE id = ?`, id,
	).Scan(&gotContent, &gotKeywords, &gotTags, &gotSearch); err != nil {
		t.Fatalf("select memory row: %v", err)
	}
	assertNoLeak(t, "UpdateMemory content 列", gotContent)
	assertNoLeak(t, "UpdateMemory keywords 列", gotKeywords)
	assertNoLeak(t, "UpdateMemory tags 列", gotTags)
	assertNoLeak(t, "UpdateMemory search_text 列", gotSearch)
}

func TestAddLongTermMemoryRedactsMirrorAndRow(t *testing.T) {
	m, db := newTestMemory(t)

	// 自动抽取路径（PreCompress / Memorize 都走它）
	if err := m.addLongTermMemory(LongTermMemory{
		Type:     "fact",
		Content:  w4Marker + " 自动抽取 content Bearer " + w4FakeKey,
		Keywords: "probe " + w4Marker,
		Tags:     w4Marker + " password=" + w4FakeKey,
	}); err != nil {
		t.Fatalf("addLongTermMemory: %v", err)
	}

	var id int64
	if err := db.QueryRow(`SELECT id FROM xbot_long_term_memories ORDER BY id DESC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("select id: %v", err)
	}
	assertNoLeak(t, "addLongTermMemory content 列",
		scanString(t, db, `SELECT content FROM xbot_long_term_memories WHERE id = ?`, id))
	mirror := mustRead(t, filepath.Join(m.baseDir, "notes", "fact_"+itoa(id)+".md"))
	assertNoLeak(t, "addLongTermMemory mirror", mirror)
}

func TestAddShortTermMemoryRedacts(t *testing.T) {
	m, db := newTestMemory(t)

	if err := m.addShortTermMemory(
		w4Marker+" 摘要 Bearer "+w4FakeKey, w4Marker+" api_key="+w4FakeKey, "sess-redact"); err != nil {
		t.Fatalf("addShortTermMemory: %v", err)
	}

	summary := scanString(t, db, `SELECT summary FROM xbot_short_term_memories ORDER BY id DESC LIMIT 1`)
	topics := scanString(t, db, `SELECT key_topics FROM xbot_short_term_memories ORDER BY id DESC LIMIT 1`)
	assertNoLeak(t, "short-term summary", summary)
	if strings.Contains(topics, w4FakeKey) {
		t.Errorf("short-term key_topics 仍含假 key: %q", topics)
	}
}

func TestWriteCoreSummaryRedacts(t *testing.T) {
	m, _ := newTestMemory(t)

	m.writeCoreSummary(w4Marker + " 摘要里有 Bearer " + w4FakeKey + " 和 api_key=" + w4FakeKey)

	got := mustRead(t, filepath.Join(m.baseDir, coreSummaryFileName))
	assertNoLeak(t, "MEMORY.md", got)
}

// TestWritePathKeepsBenignTextByteIdentical 非凭据文本必须逐字节不变（防误伤）。
func TestWritePathKeepsBenignTextByteIdentical(t *testing.T) {
	m, _ := newTestMemory(t)
	benign := "中文原样；sk-learn；total_tokens=5；task-frontend-cancel-reason-consume；" +
		"路径 /home/smith/.xbot/memory/1/notes/fact_1.md"
	id, err := m.AddMemory(t.Context(), LongTermMemory{Type: "fact", Content: benign})
	if err != nil {
		t.Fatalf("AddMemory: %v", err)
	}
	mirror := mustRead(t, filepath.Join(m.baseDir, "notes", "fact_"+itoa(id)+".md"))
	if !strings.Contains(mirror, benign) {
		t.Errorf("非凭据文本被改写:\n want 含 %q\n got %q", benign, mirror)
	}
}
