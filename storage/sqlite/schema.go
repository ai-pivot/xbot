package sqlite

import (
	"fmt"

	log "xbot/logger"
)

// createSchema creates the initial database schema at the current schemaVersion.
// The DDL includes ALL tables/columns/indexes that migrations v1→v53 would add,
// so fresh databases skip the migration chain entirely. This is critical on
// Windows where running 51 migrations per test DB causes CI timeouts (600s+).
//
// When adding a new migration, update this DDL and bump schema_version to match.
func (db *DB) createSchema() error {
	schema := `
CREATE TABLE tenants (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    runner_id TEXT DEFAULT '',
    subscription_id TEXT DEFAULT '',
    model TEXT DEFAULT '',
    model_id TEXT DEFAULT '',
    owner_user_id INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_active_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    cwd TEXT DEFAULT '',
    UNIQUE(channel, chat_id)
);

CREATE TABLE session_messages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    tenant_id INTEGER NOT NULL,
	    role TEXT NOT NULL,
	    content TEXT NOT NULL,
	    tool_call_id TEXT,
	    tool_name TEXT,
	    tool_arguments TEXT,
	    tool_calls TEXT,
	    detail TEXT,
	    reasoning_content TEXT DEFAULT '',
	    display_only INTEGER DEFAULT 0,
	    context_tokens INTEGER DEFAULT 0,
	    turn_id INTEGER DEFAULT 0,
	    record_type TEXT NOT NULL DEFAULT 'message',
	    target_history_id INTEGER,
	    record_data TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_session_messages_tenant_created ON session_messages(tenant_id, created_at);
CREATE INDEX idx_session_messages_tenant_history ON session_messages(tenant_id, id);

CREATE TABLE tenant_state (
    tenant_id INTEGER PRIMARY KEY,
    last_consolidated INTEGER DEFAULT 0,
    last_prompt_tokens INTEGER DEFAULT 0,
    last_completion_tokens INTEGER DEFAULT 0,
	    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE long_term_memory (
    tenant_id INTEGER PRIMARY KEY,
	    content TEXT NOT NULL,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE event_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    tenant_id INTEGER NOT NULL,
    entry TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_event_history_tenant_created ON event_history(tenant_id, created_at);

CREATE TABLE user_profiles (
    sender_id TEXT PRIMARY KEY,
    name TEXT NOT NULL DEFAULT '',
    profile TEXT NOT NULL DEFAULT '',
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE core_memory_blocks (
	    tenant_id INTEGER NOT NULL,
    block_name TEXT NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    content TEXT NOT NULL DEFAULT '',
    char_limit INTEGER NOT NULL DEFAULT 2000,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (tenant_id, block_name, user_id),
	    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);

CREATE TABLE archival_memory (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    tenant_id INTEGER NOT NULL,
	    content TEXT NOT NULL,
    embedding BLOB,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_archival_memory_tenant ON archival_memory(tenant_id);

CREATE VIRTUAL TABLE IF NOT EXISTS event_history_fts USING fts5(
    entry,
    content='event_history',
    content_rowid='id'
);

CREATE TRIGGER event_history_ai AFTER INSERT ON event_history BEGIN
    INSERT INTO event_history_fts(rowid, entry) VALUES (new.id, new.entry);
END;

CREATE TABLE schema_version (
    version INTEGER PRIMARY KEY
);
INSERT INTO schema_version (version) VALUES (54);

-- LLM subscriptions (v22→v23 base, modified by v25-v44 migrations)
CREATE TABLE user_llm_subscriptions (
    id          TEXT PRIMARY KEY,
    sender_id   TEXT NOT NULL,
    name        TEXT NOT NULL DEFAULT '',
    provider    TEXT NOT NULL DEFAULT 'openai',
    base_url    TEXT NOT NULL DEFAULT '',
    api_key     TEXT NOT NULL DEFAULT '',
    model       TEXT NOT NULL DEFAULT '',
    max_context INTEGER DEFAULT 0,
    max_output_tokens INTEGER DEFAULT 0,
    thinking_mode TEXT DEFAULT '',
    cached_models TEXT NOT NULL DEFAULT '',
    api_type    TEXT DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    is_system   INTEGER NOT NULL DEFAULT 0,
    user_id     INTEGER DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_llm_subs_sender ON user_llm_subscriptions(sender_id);

-- Per-model config table (v34→v35 base, modified by v36-v38 migrations)
CREATE TABLE IF NOT EXISTS subscription_models (
    id                TEXT PRIMARY KEY,
    subscription_id   TEXT NOT NULL REFERENCES user_llm_subscriptions(id) ON DELETE CASCADE,
    model             TEXT NOT NULL,
    max_context       INTEGER NOT NULL DEFAULT 0,
    max_output_tokens INTEGER NOT NULL DEFAULT 0,
    thinking_mode     TEXT NOT NULL DEFAULT '',
    api_type          TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 1,
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_sub_models_sub ON subscription_models(subscription_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sub_models_uniq ON subscription_models(subscription_id, model);

-- User default model mapping (v38→v39)
CREATE TABLE IF NOT EXISTS user_default_model (
    sender_id       TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL,
    model           TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL DEFAULT (datetime('now')),
    user_id         INTEGER DEFAULT 0
);

-- Canonical user identity system (v44→v45)
CREATE TABLE IF NOT EXISTS users (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    display_name TEXT NOT NULL DEFAULT '',
    role         TEXT NOT NULL DEFAULT 'user' CHECK(role IN ('admin', 'user')),
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
CREATE TABLE IF NOT EXISTS user_identities (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id         INTEGER NOT NULL,
    channel         TEXT NOT NULL,
    channel_user_id TEXT NOT NULL,
    linked_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(channel, channel_user_id),
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_user_identities_user ON user_identities(user_id);
CREATE TABLE IF NOT EXISTS link_codes (
    code        TEXT PRIMARY KEY,
    user_id     INTEGER NOT NULL,
    expires_at  TIMESTAMP NOT NULL,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE
);

CREATE TABLE runner_tokens (
    user_id     TEXT PRIMARY KEY,
    token       TEXT NOT NULL,
    mode        TEXT NOT NULL DEFAULT 'native',
    docker_image TEXT NOT NULL DEFAULT '',
    workspace   TEXT NOT NULL DEFAULT '/workspace',
    created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE runners (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    token        TEXT    NOT NULL UNIQUE,
    mode         TEXT    NOT NULL DEFAULT 'native',
    docker_image TEXT    NOT NULL DEFAULT 'ubuntu:22.04',
    workspace    TEXT    NOT NULL DEFAULT '',
    llm_provider TEXT    NOT NULL DEFAULT '',
    llm_api_key  TEXT    NOT NULL DEFAULT '',
    llm_model    TEXT    NOT NULL DEFAULT '',
    llm_base_url TEXT    NOT NULL DEFAULT '',
    owner_user_id INTEGER DEFAULT 0,
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, name)
);

CREATE TABLE web_users (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    password   TEXT NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE user_settings (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    channel    TEXT NOT NULL,
    sender_id  TEXT NOT NULL,
    key        TEXT NOT NULL,
    value      TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL,
    user_id    INTEGER DEFAULT 0,
    UNIQUE(channel, sender_id, key)
);
CREATE INDEX idx_user_settings_sender ON user_settings(channel, sender_id);

CREATE TABLE cron_jobs (
    id TEXT PRIMARY KEY,
    message TEXT NOT NULL,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL DEFAULT '',
    cron_expr TEXT,
    every_seconds INTEGER DEFAULT 0,
    delay_seconds INTEGER DEFAULT 0,
    at TEXT,
    created_at DATETIME NOT NULL,
    next_run DATETIME NOT NULL,
    last_trigger DATETIME,
    one_shot INTEGER NOT NULL DEFAULT 0,
    user_id INTEGER DEFAULT 0
);
CREATE INDEX idx_cron_jobs_next_run ON cron_jobs(next_run);
CREATE INDEX idx_cron_jobs_sender ON cron_jobs(sender_id);

CREATE TABLE event_triggers (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    event_type  TEXT NOT NULL DEFAULT 'webhook',
    channel     TEXT NOT NULL,
    chat_id     TEXT NOT NULL,
    sender_id   TEXT NOT NULL,
    message_tpl TEXT NOT NULL,
    secret      TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    one_shot    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    last_fired  TEXT,
    fire_count  INTEGER NOT NULL DEFAULT 0,
    user_id     INTEGER DEFAULT 0
);
CREATE INDEX idx_event_triggers_sender ON event_triggers(sender_id);
CREATE INDEX idx_event_triggers_type ON event_triggers(event_type, enabled);

CREATE TABLE user_chats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    channel TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    label TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    sort_order INTEGER DEFAULT 0,
    user_id INTEGER DEFAULT 0,
    UNIQUE(channel, sender_id, chat_id)
);
CREATE INDEX idx_user_chats_sender ON user_chats(channel, sender_id);

CREATE TABLE IF NOT EXISTS pending_resumes (
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    sender_id TEXT NOT NULL,
	    content TEXT NOT NULL,
    created_at TEXT NOT NULL,
    PRIMARY KEY (channel, chat_id)
);

-- Seed: shared tenant (id=0) for core_memory human blocks (v32→v33)
INSERT OR IGNORE INTO tenants (id, channel, chat_id, created_at, last_active_at)
VALUES (0, '_shared', '_shared', datetime('now'), datetime('now'));

-- Seed: canonical admin user + CLI/system identities (v44→v45)
INSERT OR IGNORE INTO users (id, display_name, role) VALUES (1, 'Admin', 'admin');
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'cli', 'cli_user');
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'cli', 'admin');
INSERT OR IGNORE INTO user_identities (user_id, channel, channel_user_id) VALUES (1, 'system', '__system__');

-- v54: structured iteration history (replaces Detail JSON for iteration data)
CREATE TABLE IF NOT EXISTS iteration_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL,
    tenant_id INTEGER NOT NULL,
    turn_id INTEGER NOT NULL DEFAULT 0,
    iteration INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    reasoning TEXT NOT NULL DEFAULT '',
    tools TEXT NOT NULL DEFAULT '[]',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (message_id) REFERENCES session_messages(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id);
CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id);
`
	if _, err := db.Conn().Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	log.WithField("version", schemaVersion).Info("Database schema initialized")
	return nil
}
