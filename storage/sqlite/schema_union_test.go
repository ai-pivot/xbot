package sqlite

import (
	"context"
	"database/sql"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// ── schema 并集守卫 ───────────────────────────────────────────────────────────
//
// 背景：fresh DB（createSchema）与既有库（迁移 + ensureStartupSchema）是两条路径，
// 任何一条漏表都会在运行时表现为 "no such table"，而且往往只在特定功能被触发时才炸
// （OAuth 登录、xbot 记忆 provider）。这里的测试把「两样东西必须同时存在」钉死：
//   ① 实物：fresh 库与既有库都必须有这些表/索引；
//   ② 源码：schema.go（+ agent_memory.go）里写的每一张表，实物里必须真的有 ——
//      防止「加了 DDL 但表名写错/没被执行」这种静默漂移。

func unionTableExists(t *testing.T, conn *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := conn.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master for table %q: %v", name, err)
	}
	return got == name
}

func unionIndexExists(t *testing.T, conn *sql.DB, name string) bool {
	t.Helper()
	var got string
	err := conn.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name=?", name).Scan(&got)
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("query sqlite_master for index %q: %v", name, err)
	}
	return got == name
}

// TestFreshDBSchemaUnion: 全新建库后，本分支新增的（groups/group_members/
// interactive_agents）与 origin/master 侧搬来的（oauth_tokens、xbot 记忆表）必须同时存在。
func TestFreshDBSchemaUnion(t *testing.T) {
	db, err := Open(t.TempDir() + "/x.db")
	if err != nil {
		t.Fatalf("Open fresh db: %v", err)
	}
	defer db.Close()
	conn := db.Conn()

	wantTables := []string{
		// origin/master 搬来的
		"oauth_tokens",
		"xbot_short_term_memories",
		"xbot_long_term_memories",
		// 本分支的
		"groups",
		"group_members",
		"interactive_agents",
	}
	for _, name := range wantTables {
		if !unionTableExists(t, conn, name) {
			t.Errorf("fresh db is missing table %q", name)
		}
	}
	if !unionIndexExists(t, conn, "oauth_tokens_expires_at") {
		t.Errorf("fresh db is missing index oauth_tokens_expires_at")
	}

	// oauth_tokens 的列必须齐全（列清单来自 origin/master；缺列 = 运行时扫描错位）
	wantOAuthCols := []string{
		"provider", "channel", "chat_id", "access_token", "refresh_token",
		"expires_at", "scopes", "raw", "updated_at",
	}
	for _, col := range wantOAuthCols {
		if !unionHasColumn(t, conn, "oauth_tokens", col) {
			t.Errorf("oauth_tokens is missing column %q", col)
		}
	}
}

func unionHasColumn(t *testing.T, conn *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := conn.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("pragma table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan pragma table_info(%s): %v", table, err)
		}
		if name == column {
			return true
		}
	}
	return false
}

var createTableRe = regexp.MustCompile(`(?i)CREATE\s+(?:VIRTUAL\s+)?TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([A-Za-z_][A-Za-z0-9_]*)`)

// TestSchemaGoDDLMatchesArtifact: 源码里写到的每一张表，实物里必须存在。
// 谁往 schema.go / agent_memory.go 加了表却写错名字（或忘了执行），这条会红。
func TestSchemaGoDDLMatchesArtifact(t *testing.T) {
	db, err := Open(t.TempDir() + "/x.db")
	if err != nil {
		t.Fatalf("Open fresh db: %v", err)
	}
	defer db.Close()

	declared, err := declaredTables("schema.go", "agent_memory.go")
	if err != nil {
		t.Fatalf("parse DDL source: %v", err)
	}
	// 防呆：解析器本身失效时（返回空集）必须红，不能静默通过。
	if len(declared) < 20 {
		t.Fatalf("parsed only %d CREATE TABLE statements from schema.go/agent_memory.go — parser is broken", len(declared))
	}
	for _, name := range declared {
		if !unionTableExists(t, db.Conn(), name) {
			t.Errorf("schema source declares table %q but the database has no such table", name)
		}
	}
	// 反向抽查：两个来源的 DDL 都真的出现在解析结果里（避免解析只覆盖了一个文件）
	for _, must := range []string{"oauth_tokens", "groups", "interactive_agents", "xbot_long_term_memories"} {
		found := false
		for _, name := range declared {
			if name == must {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("DDL parser did not see %q", must)
		}
	}
}

// declaredTables 从给定的包内源文件里解析所有 CREATE TABLE 的表名。
func declaredTables(files ...string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(raw), -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out, nil
}

// TestOAuthDDLMatchesOAuthPackage: storage/sqlite 的 oauth_tokens 列清单必须与
// oauth.SQLiteStorage.initSchema 完全一致 —— 两者写的是**同一个库文件**（shared DB），
// 一边多/少一列就会让另一边的初始化在这些库上静默失效。
func TestOAuthDDLMatchesOAuthPackage(t *testing.T) {
	raw, err := os.ReadFile("../../oauth/storage.go")
	if err != nil {
		t.Fatalf("read oauth/storage.go: %v", err)
	}
	cols := oauthColumnsFromDDL(t, string(raw))
	if len(cols) == 0 {
		t.Fatalf("found no oauth_tokens column list in ../../oauth/storage.go")
	}
	local, err := declaredTables("schema.go")
	if err != nil {
		t.Fatalf("parse schema.go: %v", err)
	}
	_ = local // 表名一致性由 TestSchemaGoDDLMatchesArtifact 覆盖

	db, err := Open(t.TempDir() + "/x.db")
	if err != nil {
		t.Fatalf("Open fresh db: %v", err)
	}
	defer db.Close()
	for _, col := range cols {
		if !unionHasColumn(t, db.Conn(), "oauth_tokens", col) {
			t.Errorf("oauth/storage.go declares oauth_tokens column %q but storage/sqlite's oauth_tokens lacks it", col)
		}
	}
}

// oauthColumnsFromDDL 抽取 oauth/storage.go 里 CREATE TABLE oauth_tokens 的列名
// （跳过 PRIMARY KEY 约束行）。
func oauthColumnsFromDDL(t *testing.T, src string) []string {
	t.Helper()
	idx := strings.Index(src, "CREATE TABLE IF NOT EXISTS oauth_tokens")
	if idx < 0 {
		return nil
	}
	body := src[idx:]
	end := strings.Index(body, ");")
	if end < 0 {
		t.Fatalf("unterminated oauth_tokens DDL in oauth/storage.go")
	}
	body = body[:end]
	var cols []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" || strings.HasPrefix(strings.ToUpper(line), "CREATE") ||
			strings.HasPrefix(strings.ToUpper(line), "PRIMARY KEY") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		cols = append(cols, fields[0])
	}
	return cols
}

// TestEnsureAgentMemorySchemaIdempotent: 幂等 —— 连跑两次不报错；Open→Close→Open 不报错。
func TestEnsureAgentMemorySchemaIdempotent(t *testing.T) {
	path := t.TempDir() + "/x.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	svc := NewAgentMemoryService(db)
	ctx := context.Background()
	if err := svc.EnsureAgentMemorySchema(ctx); err != nil {
		t.Fatalf("EnsureAgentMemorySchema #1: %v", err)
	}
	if err := svc.EnsureAgentMemorySchema(ctx); err != nil {
		t.Fatalf("EnsureAgentMemorySchema #2 (must be idempotent): %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open after Close: %v", err)
	}
	defer db2.Close()
	if err := NewAgentMemoryService(db2).EnsureAgentMemorySchema(ctx); err != nil {
		t.Fatalf("EnsureAgentMemorySchema on reopened db: %v", err)
	}
	for _, name := range []string{"xbot_short_term_memories", "xbot_long_term_memories"} {
		if !unionTableExists(t, db2.Conn(), name) {
			t.Errorf("reopened db lost table %q", name)
		}
	}
}

// TestExistingDBCatchesUpMissingTables: 既有库（schema_version 已是 76）缺表时，
// 再次 Open 必须把表补回来。这条直接对应真实故障模式：
// version == schemaVersion 时 initSchema 直接 return，不补任何表 ⇒ 全库都缺
// oauth_tokens 时只能靠 Open 的兜底 ensure。
func TestExistingDBCatchesUpMissingTables(t *testing.T) {
	path := t.TempDir() + "/x.db"
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("expected schema_version %d, got %d", schemaVersion, version)
	}

	// 模拟「v76 库但整库缺 oauth_tokens / agent 记忆表」
	for _, stmt := range []string{
		"DROP TABLE oauth_tokens",
		"DROP TABLE xbot_short_term_memories",
		"DROP TABLE xbot_long_term_memories",
	} {
		if _, err := db.Conn().Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if unionTableExists(t, db.Conn(), "oauth_tokens") {
		t.Fatalf("oauth_tokens still exists after DROP — test setup is broken")
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open existing v76 db: %v", err)
	}
	defer db2.Close()
	for _, name := range []string{
		"oauth_tokens",
		"xbot_short_term_memories",
		"xbot_long_term_memories",
	} {
		if !unionTableExists(t, db2.Conn(), name) {
			t.Errorf("re-Open of an existing v%d db did not recreate table %q", version, name)
		}
	}
	if !unionIndexExists(t, db2.Conn(), "oauth_tokens_expires_at") {
		t.Errorf("re-Open of an existing db did not recreate index oauth_tokens_expires_at")
	}
}
