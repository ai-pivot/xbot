package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// createV62Fixture builds a hand-crafted v62 database (pre-multi-user-removal
// schema) with MULTI-USER data spread across two canonical users, so the v63
// migration's data-collapse behavior is observable. Returns the DB path.
func createV62Fixture(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "v62.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	defer raw.Close()
	if _, err := raw.Exec(`
		-- Canonical identity tables (v44→v62 era).
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			display_name TEXT NOT NULL DEFAULT '',
			role TEXT NOT NULL DEFAULT 'user',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE user_identities (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL,
			channel TEXT NOT NULL,
			channel_user_id TEXT NOT NULL,
			linked_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(channel, channel_user_id)
		);
		CREATE TABLE link_codes (
			code TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			expires_at TIMESTAMP NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO users (id, display_name, role) VALUES (1, 'Admin', 'admin'), (2, 'Other', 'user');
		INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'cli', 'cli_user'), (1, 'web', 'web-1'), (2, 'web', 'web-7');

		CREATE TABLE user_settings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL, sender_id TEXT NOT NULL,
			key TEXT NOT NULL, value TEXT NOT NULL DEFAULT '',
			updated_at INTEGER NOT NULL,
			user_id INTEGER DEFAULT 0,
			UNIQUE(channel, sender_id, key)
		);
		-- cli_user wins the thinking_mode conflict; web-7 has a unique key.
		INSERT INTO user_settings (channel, sender_id, key, value, updated_at, user_id) VALUES
			('cli', 'cli_user', 'thinking_mode', 'on',  100, 1),
			('cli', 'web-7',   'thinking_mode', 'off', 200, 2),
			('web', 'web-7',   'ui:layout', 'dark',  300, 2);

		CREATE TABLE user_llm_subscriptions (
			id TEXT PRIMARY KEY, sender_id TEXT NOT NULL DEFAULT '', name TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '', base_url TEXT NOT NULL DEFAULT '', api_key TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
			user_id INTEGER DEFAULT 0,
			created_at TEXT NOT NULL DEFAULT (datetime('now')), updated_at TEXT NOT NULL DEFAULT (datetime('now'))
		);
		INSERT INTO user_llm_subscriptions (id, sender_id, name, user_id) VALUES
			('sub-1', 'cli_user', 'main-sub', 1),
			('sub-2', 'web-7',    'other-sub', 2);

		CREATE TABLE user_default_model (
			sender_id TEXT PRIMARY KEY,
			subscription_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT (datetime('now')),
			user_id INTEGER DEFAULT 0
		);
		-- web-7's row is newer (updated_at DESC) and must win.
		INSERT INTO user_default_model (sender_id, subscription_id, model, updated_at, user_id) VALUES
			('cli_user', 'sub-1', 'glm-5.2', '2026-01-01 00:00:00', 1),
			('web-7',    'sub-2', 'glm-5.2-air', '2026-06-01 00:00:00', 2);

		CREATE TABLE user_chats (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL, sender_id TEXT NOT NULL, chat_id TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '', created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			sort_order INTEGER DEFAULT 0, user_id INTEGER DEFAULT 0,
			UNIQUE(channel, sender_id, chat_id)
		);
		INSERT INTO user_chats (channel, sender_id, chat_id, label, user_id) VALUES
			('web', 'cli_user', 'chat-a', 'A', 1),
			('web', 'web-7',    'chat-b', 'B', 2),
			('web', 'cli_user', 'chat-dup', 'dup-op', 1),
			('web', 'web-7',    'chat-dup', 'dup-other', 2);

		CREATE TABLE user_token_usage (
			sender_id TEXT PRIMARY KEY,
			input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0, cached_tokens INTEGER NOT NULL DEFAULT 0,
			conversation_count INTEGER NOT NULL DEFAULT 0, llm_call_count INTEGER NOT NULL DEFAULT 0,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO user_token_usage (sender_id, input_tokens, output_tokens, total_tokens, cached_tokens, conversation_count, llm_call_count) VALUES
			('cli_user', 100, 50, 150, 20, 3, 5),
			('web-7',    30, 10,  40,  5, 2, 2);

		CREATE TABLE daily_token_usage (
			date TEXT NOT NULL, sender_id TEXT NOT NULL, model TEXT NOT NULL DEFAULT '',
			input_tokens INTEGER NOT NULL DEFAULT 0, output_tokens INTEGER NOT NULL DEFAULT 0,
			cached_tokens INTEGER NOT NULL DEFAULT 0, conversation_count INTEGER NOT NULL DEFAULT 0,
			llm_call_count INTEGER NOT NULL DEFAULT 0, updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (date, sender_id, model)
		);
		INSERT INTO daily_token_usage (date, sender_id, model, input_tokens, output_tokens, cached_tokens) VALUES
			('2026-08-01', 'cli_user', 'glm-5.2', 10, 5, 2),
			('2026-08-01', 'web-7',    'glm-5.2', 4,  2, 1);

		CREATE TABLE runners (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL, name TEXT NOT NULL, token TEXT NOT NULL UNIQUE,
			mode TEXT NOT NULL DEFAULT 'native', docker_image TEXT NOT NULL DEFAULT 'ubuntu:22.04',
			workspace TEXT NOT NULL DEFAULT '', llm_provider TEXT NOT NULL DEFAULT '',
			llm_api_key TEXT NOT NULL DEFAULT '', llm_model TEXT NOT NULL DEFAULT '', llm_base_url TEXT NOT NULL DEFAULT '',
			owner_user_id INTEGER DEFAULT 0, created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(user_id, name)
		);
		-- Same-name runner conflicts: the operator's row wins, web-7's is renamed.
		INSERT INTO runners (user_id, name, token, owner_user_id) VALUES
			('cli_user', 'gpu', 'tok-gpu-1', 1),
			('web-7',    'gpu', 'tok-gpu-2', 2),
			('web-7',    'cpu', 'tok-cpu', 2);

		CREATE TABLE runner_tokens (
			user_id TEXT PRIMARY KEY, token TEXT NOT NULL, mode TEXT NOT NULL DEFAULT 'native',
			docker_image TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '/workspace',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO runner_tokens (user_id, token) VALUES ('web-7', 'tok-legacy');

		CREATE TABLE user_profiles (
			sender_id TEXT PRIMARY KEY, display_name TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO user_profiles (sender_id) VALUES ('cli_user'), ('web-7');

		CREATE TABLE xbot_short_term_memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO xbot_short_term_memories (user_id) VALUES (1), (2), (2);

		CREATE TABLE xbot_long_term_memories (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		INSERT INTO xbot_long_term_memories (user_id) VALUES (1), (2);

		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL, chat_id TEXT NOT NULL,
			runner_id TEXT DEFAULT '', subscription_id TEXT DEFAULT '', model TEXT DEFAULT '', model_id TEXT DEFAULT '',
			owner_user_id INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP, last_active_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			cwd TEXT DEFAULT '',
			UNIQUE(channel, chat_id)
		);
		INSERT INTO tenants (channel, chat_id, owner_user_id) VALUES ('web', 'chat-a', 1), ('web', 'chat-b', 2);

		CREATE TABLE cron_jobs (
			id TEXT PRIMARY KEY, message TEXT NOT NULL, channel TEXT NOT NULL, chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL DEFAULT '', cron_expr TEXT, every_seconds INTEGER DEFAULT 0,
			delay_seconds INTEGER DEFAULT 0, at TEXT, created_at DATETIME NOT NULL, next_run DATETIME NOT NULL,
			last_trigger DATETIME, one_shot INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER DEFAULT 0
		);
		CREATE INDEX idx_cron_jobs_user ON cron_jobs(user_id);
		INSERT INTO cron_jobs (id, message, channel, chat_id, sender_id, created_at, next_run, user_id)
			VALUES ('job-1', 'm', 'web', 'chat-a', 'cli_user', '2026-01-01', '2026-01-02', 2);

		CREATE TABLE event_triggers (
			id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '', event_type TEXT NOT NULL DEFAULT 'webhook',
			channel TEXT NOT NULL, chat_id TEXT NOT NULL, sender_id TEXT NOT NULL,
			message_tpl TEXT NOT NULL, secret TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1,
			one_shot INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL, last_fired TEXT,
			fire_count INTEGER NOT NULL DEFAULT 0,
			user_id INTEGER DEFAULT 0
		);
		INSERT INTO event_triggers (id, channel, chat_id, sender_id, message_tpl, created_at, user_id)
			VALUES ('trg-1', 'web', 'chat-a', 'cli_user', 'tpl', '2026-01-01', 2);

		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version (version) VALUES (62);
	`); err != nil {
		t.Fatalf("create v62 fixture: %v", err)
	}
	return dbPath
}

// TestMigrateV62ToV63_CollapsesMultiUserToSingleOperator verifies the v63
// migration data collapse end-to-end on a multi-user v62 database:
//   - identity tables (users/user_identities/link_codes) dropped
//   - sender-scoped rows collapsed to 'cli_user' with conflict resolution
//   - token usage SUM-merged; xbot memories UNION-kept at user_id=1
//   - canonical INTEGER user_id columns dropped
func TestMigrateV62ToV63_CollapsesMultiUserToSingleOperator(t *testing.T) {
	db, err := Open(createV62Fixture(t))
	if err != nil {
		t.Fatalf("open (triggers migration): %v", err)
	}
	defer db.Close()
	conn := db.Conn()

	// Identity tables are gone.
	for _, tbl := range []string{"users", "user_identities", "link_codes"} {
		if ok, err := tableExists(conn, tbl); err != nil {
			t.Fatalf("check %s: %v", tbl, err)
		} else if ok {
			t.Errorf("table %s still exists after v63", tbl)
		}
	}

	// schema_version = 63.
	var version int
	if err := conn.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 63 {
		t.Fatalf("schema version = %d, want 63", version)
	}

	// user_settings: every row is cli_user; the operator wins conflicts;
	// unique keys from other senders survive the collapse.
	type settingRow struct{ sender, key, value string }
	rows, err := conn.Query(`SELECT sender_id, key, value FROM user_settings ORDER BY key`)
	if err != nil {
		t.Fatalf("query user_settings: %v", err)
	}
	var got []settingRow
	for rows.Next() {
		var r settingRow
		if err := rows.Scan(&r.sender, &r.key, &r.value); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, r)
	}
	rows.Close()
	if len(got) != 2 {
		t.Fatalf("user_settings rows = %d (%+v), want 2", len(got), got)
	}
	for _, r := range got {
		if r.sender != "cli_user" {
			t.Errorf("user_settings row (%s, %s) sender = %s, want cli_user", r.key, r.value, r.sender)
		}
		if r.key == "thinking_mode" && r.value != "on" {
			t.Errorf("thinking_mode conflict resolved to %q, want the operator's %q", r.value, "on")
		}
	}

	// Subscriptions: UNION-kept, sender collapsed.
	var subCount, nonOpSubs int
	if err := conn.QueryRow("SELECT COUNT(*), SUM(CASE WHEN sender_id != 'cli_user' THEN 1 ELSE 0 END) FROM user_llm_subscriptions").Scan(&subCount, &nonOpSubs); err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	if subCount != 2 {
		t.Errorf("subscriptions = %d, want 2 (UNION-kept)", subCount)
	}
	if nonOpSubs != 0 {
		t.Errorf("%d subscriptions not collapsed to cli_user", nonOpSubs)
	}

	// user_default_model: single row (the freshest), sender collapsed.
	var dmSender, dmSub string
	if err := conn.QueryRow("SELECT sender_id, subscription_id FROM user_default_model").Scan(&dmSender, &dmSub); err != nil {
		t.Fatalf("read user_default_model: %v", err)
	}
	if dmSender != "cli_user" || dmSub != "sub-2" {
		t.Errorf("user_default_model = (%s, %s), want (cli_user, sub-2) — freshest row wins", dmSender, dmSub)
	}

	// user_chats: all cli_user, (channel, chat_id) dedup keeps the operator's row.
	var chatRows int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM user_chats WHERE sender_id != 'cli_user'`).Scan(&chatRows); err != nil {
		t.Fatalf("count non-operator chats: %v", err)
	}
	if chatRows != 0 {
		t.Errorf("%d user_chats rows not collapsed", chatRows)
	}
	var dupLabel string
	if err := conn.QueryRow(`SELECT label FROM user_chats WHERE channel = 'web' AND chat_id = 'chat-dup'`).Scan(&dupLabel); err != nil {
		t.Fatalf("read dedup chat: %v", err)
	}
	if dupLabel != "dup-op" {
		t.Errorf("chat-dup label = %q, want operator's %q (conflict keeps the operator row)", dupLabel, "dup-op")
	}

	// user_token_usage: SUM-merged into the operator row.
	var inTok, outTok, convCnt int
	if err := conn.QueryRow(`SELECT input_tokens, output_tokens, conversation_count FROM user_token_usage WHERE sender_id = 'cli_user'`).Scan(&inTok, &outTok, &convCnt); err != nil {
		t.Fatalf("read merged token usage: %v", err)
	}
	if inTok != 130 || outTok != 60 || convCnt != 5 {
		t.Errorf("token usage = (%d, %d, conv %d), want SUM (130, 60, 5)", inTok, outTok, convCnt)
	}
	var tokRows int
	if err := conn.QueryRow("SELECT COUNT(*) FROM user_token_usage").Scan(&tokRows); err != nil {
		t.Fatalf("count token usage rows: %v", err)
	}
	if tokRows != 1 {
		t.Errorf("token usage rows = %d, want 1 (single operator)", tokRows)
	}

	// daily_token_usage: SUM-merged per (date, model).
	var dInTok int
	if err := conn.QueryRow(`SELECT input_tokens FROM daily_token_usage WHERE date = '2026-08-01' AND model = 'glm-5.2' AND sender_id = 'cli_user'`).Scan(&dInTok); err != nil {
		t.Fatalf("read merged daily usage: %v", err)
	}
	if dInTok != 14 {
		t.Errorf("daily usage input_tokens = %d, want 14 (10+4 SUM)", dInTok)
	}

	// runners: same-name conflict renamed, all owned by cli_user.
	var runnerCount, gpuOwner int
	if err := conn.QueryRow(`SELECT COUNT(*), SUM(CASE WHEN user_id = 'cli_user' THEN 1 ELSE 0 END) FROM runners`).Scan(&runnerCount, &gpuOwner); err != nil {
		t.Fatalf("count runners: %v", err)
	}
	if runnerCount != 3 || gpuOwner != 3 {
		t.Errorf("runners = (%d total, %d operator), want (3, 3)", runnerCount, gpuOwner)
	}
	var renamed string
	if err := conn.QueryRow(`SELECT token FROM runners WHERE name = 'gpu_web-7'`).Scan(&renamed); err != nil {
		t.Fatalf("renamed conflicting runner missing: %v", err)
	}

	// runner_tokens / user_profiles: single collapsed row each.
	var rtCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM runner_tokens WHERE user_id = 'cli_user'`).Scan(&rtCount); err != nil {
		t.Fatalf("count runner_tokens: %v", err)
	}
	if rtCount != 1 {
		t.Errorf("runner_tokens collapsed rows = %d, want 1", rtCount)
	}
	var upCount int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM user_profiles WHERE sender_id = 'cli_user'`).Scan(&upCount); err != nil {
		t.Fatalf("count user_profiles: %v", err)
	}
	if upCount != 1 {
		t.Errorf("user_profiles collapsed rows = %d, want 1", upCount)
	}

	// xbot memories: UNION-kept, user_id collapsed to 1.
	var stmUser2, ltmUser2 int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM xbot_short_term_memories WHERE user_id != 1`).Scan(&stmUser2); err != nil {
		t.Fatalf("count short memories: %v", err)
	}
	if err := conn.QueryRow(`SELECT COUNT(*) FROM xbot_long_term_memories WHERE user_id != 1`).Scan(&ltmUser2); err != nil {
		t.Fatalf("count long memories: %v", err)
	}
	if stmUser2 != 0 || ltmUser2 != 0 {
		t.Errorf("xbot memories not collapsed: %d short + %d long rows with user_id != 1", stmUser2, ltmUser2)
	}
	var stmTotal int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM xbot_short_term_memories`).Scan(&stmTotal); err != nil {
		t.Fatalf("count total short memories: %v", err)
	}
	if stmTotal != 3 {
		t.Errorf("xbot_short_term_memories = %d, want 3 (UNION-kept)", stmTotal)
	}

	// Canonical INTEGER columns dropped.
	for _, tc := range [][2]string{
		{"tenants", "owner_user_id"},
		{"runners", "owner_user_id"},
		{"user_llm_subscriptions", "user_id"},
		{"user_settings", "user_id"},
		{"user_default_model", "user_id"},
		{"user_chats", "user_id"},
		{"cron_jobs", "user_id"},
		{"event_triggers", "user_id"},
	} {
		if ok, err := columnExists(conn, tc[0], tc[1]); err != nil {
			t.Fatalf("check %s.%s: %v", tc[0], tc[1], err)
		} else if ok {
			t.Errorf("column %s.%s still exists after v63", tc[0], tc[1])
		}
	}
}

// TestMigrateV62ToV63_Idempotent re-running the migration body on an
// already-migrated database must not error (all steps are guarded).
func TestMigrateV62ToV63_Idempotent(t *testing.T) {
	db, err := Open(createV62Fixture(t))
	if err != nil {
		t.Fatalf("open (first run): %v", err)
	}
	defer db.Close()
	// Second run must be safe: every step is guarded (tableExists /
	// columnExists / conflict resolution on an already-collapsed DB).
	if err := migrateV62ToV63(db); err != nil {
		t.Fatalf("re-run migrateV62ToV63: %v", err)
	}
	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 63 {
		t.Errorf("version after re-run = %d, want 63", version)
	}
}
