package sqlite

import (
	"context"
	"fmt"

	log "xbot/logger"
)

// createSchema creates the initial database schema at the current schemaVersion.
// The DDL includes ALL tables/columns/indexes that migrations v1→v77 would add,
// so fresh databases skip the migration chain entirely. This is critical on
// Windows where running every migration per test DB causes CI timeouts (600s+).
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
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_active_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    cwd TEXT DEFAULT '',
    -- v71: 每会话一个 DB（one session, one DB）—— 会话库注册表列。
    -- db_path: 会话库相对路径（sessions/<channel>/<bucket>/<name>.db，相对主库目录）；
    -- migrated: 会话数据是否已惰性迁移到会话库（0=主库 session_messages 仍是权威，
    --   首次打开会话库时按 tenant_id 拷贝并置 1；拷贝前先 DELETE，幂等）；
    -- preview: 会话最新一条 user/assistant 消息的截断预览（跨会话列表读主库这一列，
    --   不再 JOIN session_messages —— 拆库后主库没有消息数据，预览由写入路径维护）。
    db_path TEXT NOT NULL DEFAULT '',
    migrated INTEGER NOT NULL DEFAULT 0,
    preview TEXT NOT NULL DEFAULT '',
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
	    reasoning_items TEXT DEFAULT '',
	    display_only INTEGER DEFAULT 0,
	    internal_only INTEGER DEFAULT 0,
	    context_tokens INTEGER DEFAULT 0,
    turn_id INTEGER DEFAULT 0,
    iteration INTEGER NOT NULL DEFAULT 0,
    record_type TEXT NOT NULL DEFAULT 'message',
    target_history_id INTEGER,
    record_data TEXT,
    -- v74: 群组消息信封（Agent 通信指挥室）。群发 = 遍历成员各发一份，
    -- 每个成员会话库里的副本都带同一 group_id 与发送者身份。
    group_id TEXT NOT NULL DEFAULT '',
    sender_type TEXT NOT NULL DEFAULT '',
    sender_id TEXT NOT NULL DEFAULT '',
    sender_name TEXT NOT NULL DEFAULT '',
    -- v75: 结构化消息列（与会话库 v5 同一列清单；主库仅遗留迁移期使用）。
    chat_id TEXT NOT NULL DEFAULT '',
    kind TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL DEFAULT '',
    reply_to TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL DEFAULT '',
    structured_json TEXT NOT NULL DEFAULT '',
    direction TEXT NOT NULL DEFAULT '',
    answered INTEGER NOT NULL DEFAULT 0,
    cancelled INTEGER NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
     FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE
);
CREATE INDEX idx_session_messages_tenant_created ON session_messages(tenant_id, created_at);
CREATE INDEX idx_session_messages_tenant_history ON session_messages(tenant_id, id);
-- v60: partial index for control records (ask_question/ask_answer/mask/context_edit...).
-- WHERE record_type != 'message' keeps the hot-path INSERT (plain messages) zero-cost
-- (message rows never enter the index), while control-record lookups (ask_answer
-- anti-join by tenant_id+record_type+target_history_id) hit the index instead of a
-- full tenant scan.
CREATE INDEX IF NOT EXISTS idx_sm_tenant_record ON session_messages(tenant_id, record_type, target_history_id) WHERE record_type != 'message';
-- v61: partial index for the ListUserChats preview subquery (latest user/assistant
-- message per tenant). WHERE role IN ('user','assistant') keeps tool/assistant-tool rows
-- out of the index.
CREATE INDEX IF NOT EXISTS idx_sm_tenant_role_id ON session_messages(tenant_id, role, id) WHERE role IN ('user','assistant');
-- v74: 群组展示的游标分页索引（按群取一页，id DESC 直接走索引）。
CREATE INDEX IF NOT EXISTS ix_sm_group_cursor ON session_messages(group_id, id DESC);

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
INSERT INTO schema_version (version) VALUES (79);

-- Token usage statistics (v19 cumulative + v25 daily). Fresh databases skip
-- historical migrations, so both tables must be part of this schema snapshot.
CREATE TABLE IF NOT EXISTS user_token_usage (
    sender_id TEXT PRIMARY KEY,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    conversation_count INTEGER NOT NULL DEFAULT 0,
    llm_call_count INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS daily_token_usage (
    date TEXT NOT NULL,
    sender_id TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    conversation_count INTEGER NOT NULL DEFAULT 0,
    llm_call_count INTEGER NOT NULL DEFAULT 0,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (date, sender_id, model)
);
CREATE INDEX IF NOT EXISTS idx_daily_token_usage_sender ON daily_token_usage(sender_id);
CREATE INDEX IF NOT EXISTS idx_daily_token_usage_date ON daily_token_usage(date);

-- LLM subscriptions (v22→v23 base, modified by v25-v44 migrations; is_system
-- dropped in v62 — the system subscription was removed, the global fallback
-- LLM is the in-memory defaultLLM built from cfg.LLM)
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
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_llm_subs_sender ON user_llm_subscriptions(sender_id);

-- Per-model config table (v34→v35 base, modified by v36-v38 migrations; v64 adds vision columns)
CREATE TABLE IF NOT EXISTS subscription_models (
    id                TEXT PRIMARY KEY,
    subscription_id   TEXT NOT NULL REFERENCES user_llm_subscriptions(id) ON DELETE CASCADE,
    model             TEXT NOT NULL,
    max_context       INTEGER NOT NULL DEFAULT 0,
    max_output_tokens INTEGER NOT NULL DEFAULT 0,
    thinking_mode     TEXT NOT NULL DEFAULT '',
    api_type          TEXT NOT NULL DEFAULT '',
    enabled           INTEGER NOT NULL DEFAULT 1,
    vision            INTEGER NOT NULL DEFAULT 0,
    vision_detail     TEXT NOT NULL DEFAULT '',
    created_at        TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at        TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_sub_models_sub ON subscription_models(subscription_id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_sub_models_uniq ON subscription_models(subscription_id, model);

-- User default model mapping (v38→v39; user_id dropped in v63 multi-user removal)
CREATE TABLE IF NOT EXISTS user_default_model (
    sender_id       TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL,
    model           TEXT NOT NULL DEFAULT '',
    updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE runners (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL UNIQUE,
    token        TEXT    NOT NULL,
    mode         TEXT    NOT NULL DEFAULT 'native',
    docker_image TEXT    NOT NULL DEFAULT '',
    workspace    TEXT    NOT NULL DEFAULT '',
    llm_provider TEXT    NOT NULL DEFAULT '',
    llm_api_key  TEXT    NOT NULL DEFAULT '',
    llm_model    TEXT    NOT NULL DEFAULT '',
    llm_base_url TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_runners_token ON runners(token);

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
    one_shot INTEGER NOT NULL DEFAULT 0
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
    fire_count  INTEGER NOT NULL DEFAULT 0
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

-- v54: structured iteration history (replaces Detail JSON for iteration data)
CREATE TABLE IF NOT EXISTS iteration_history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_id INTEGER NOT NULL DEFAULT 0,
    tenant_id INTEGER NOT NULL,
    turn_id INTEGER NOT NULL DEFAULT 0,
    iteration INTEGER NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    reasoning TEXT NOT NULL DEFAULT '',
    tools TEXT NOT NULL DEFAULT '[]',
    tokens INTEGER NOT NULL DEFAULT 0,
    ttft_ms INTEGER NOT NULL DEFAULT 0,
    tokens_per_sec INTEGER NOT NULL DEFAULT 0,
    total_ms INTEGER NOT NULL DEFAULT 0,
    tpot_ms INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    model TEXT NOT NULL DEFAULT '',
    subscription_id TEXT NOT NULL DEFAULT '',
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id);
CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id);

-- v65: shared artifacts — generic storage behind plugin-provided shareable
-- content. The host knows nothing about what is shared: content_type is named
-- by the producing plugin and payload is opaque to the host; only that
-- plugin's own share renderer can interpret it. token IS the credential — a
-- high-entropy random string granting read access to one immutable artifact
-- without any session.
CREATE TABLE IF NOT EXISTS shared_artifacts (
    token TEXT PRIMARY KEY,
    plugin_id TEXT NOT NULL DEFAULT '',
    content_type TEXT NOT NULL,
    payload TEXT NOT NULL,
    title TEXT NOT NULL DEFAULT '',
    created_at TEXT NOT NULL,
    created_by INTEGER NOT NULL DEFAULT 0,
    expires_at TEXT NOT NULL DEFAULT '',
    revoked_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_shared_artifacts_creator ON shared_artifacts(created_by);

-- 注意：oauth_tokens 不在上面/下面这段 DDL 里 —— 它的建表语句单点定义在文件尾部的
-- oauthTokensSchema 常量（createSchema 与 ensureStartupSchema 共用同一份，避免两份
-- DDL 漂移）；列清单与 oauth/storage.go 的 initSchema 逐字一致（同一个库文件）。
-- v74: 群组元数据（Agent 通信指挥室）。只有两张小表，没有群级消息表：
-- 群发 = 遍历 alive 成员各发一份，消息落在各自会话库。
CREATE TABLE IF NOT EXISTS groups (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    owner TEXT NOT NULL,
    created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS group_members (
    group_id TEXT NOT NULL,
    member_addr TEXT NOT NULL,
    status TEXT NOT NULL DEFAULT 'alive',
    joined_at TEXT NOT NULL,
    PRIMARY KEY (group_id, member_addr)
);
CREATE INDEX IF NOT EXISTS ix_group_members_addr ON group_members(member_addr, status);

-- v76: interactive SubAgent 注册表（重启后递归重建子代理树）。
-- 只存重建所需的最小事实；messages 在各子代理自己的 TenantSession 里。
CREATE TABLE IF NOT EXISTS interactive_agents (
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
    -- v77: 最近一次「回复文本发生变化」的时刻（空 = 从未回复）。活动视图按它显示
    -- 「这个子代理最后一次真正说话是什么时候」。
    last_reply_at   TEXT NOT NULL DEFAULT '',
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_ia_parent ON interactive_agents(parent_channel, parent_chat_id);
CREATE INDEX IF NOT EXISTS ix_ia_status ON interactive_agents(status);
CREATE INDEX IF NOT EXISTS ix_ia_updated ON interactive_agents(updated_at DESC);
-- v77: 跨会话「活动流」索引（通信视图性能重构）。
--
-- 铁律：结构化消息本体只落会话分库（one session one DB）。跨会话看消息若逐库
-- 打开，代价随会话数线性恶化（40 会话 ≈ 40 次 open + 8000 行）。这张表在主库
-- 冗余一份「可展示的最小摘要」，视图只打主库一张表，与会话数无关。
--
-- 只收结构化 kind（report/ask_user/answer/decision/command/event），普通 text
-- 不入索引 —— 否则索引会被聊天文本撑成第二个消息库。
-- created_at 固定宽度 UTC（2006-01-02T15:04:05.000000000Z）：字符串序 == 时间序，
-- 因此 ORDER BY created_at DESC 可以直走索引（不用函数索引/解析）。
CREATE TABLE IF NOT EXISTS activity_index (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    tenant_id   INTEGER NOT NULL,
    chat_id     TEXT    NOT NULL,
    channel     TEXT    NOT NULL,
    message_id  INTEGER NOT NULL,
    kind        TEXT    NOT NULL,
    sender_id   TEXT    NOT NULL DEFAULT '',
    sender_name TEXT    NOT NULL DEFAULT '',
    summary     TEXT    NOT NULL DEFAULT '',
    created_at  TEXT    NOT NULL,
    UNIQUE(tenant_id, chat_id, message_id)
);
CREATE INDEX IF NOT EXISTS idx_activity_created ON activity_index(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_chat   ON activity_index(chat_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_activity_kind   ON activity_index(kind, created_at DESC);

`
	if _, err := db.Conn().Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	// oauth_tokens 的 DDL 只在这里定义一次（oauthTokensSchema），fresh DB 必须建出来，
	// 否则 OAuth 登录路径会撞 no such table。既有库缺表由 ensureStartupSchema 补。
	if _, err := db.Conn().Exec(oauthTokensSchema); err != nil {
		return fmt.Errorf("create oauth schema: %w", err)
	}
	log.WithField("version", schemaVersion).Info("Database schema initialized")
	return nil
}

// oauthTokensSchema is the OAuth token table + expiry index.
//
// Single source of truth for this DDL inside storage/sqlite: it is executed both by
// createSchema (fresh databases) and by ensureStartupSchema (databases created
// before the table existed). The column list MUST stay identical to
// oauth.SQLiteStorage.initSchema — that path runs against this very database
// (shared DB, not a separate file), and a mismatch would make one initializer
// silently fail against the other's table.
const oauthTokensSchema = `
CREATE TABLE IF NOT EXISTS oauth_tokens (
    provider TEXT NOT NULL,
    channel TEXT NOT NULL,
    chat_id TEXT NOT NULL,
    access_token TEXT NOT NULL,
    refresh_token TEXT,
    expires_at INTEGER NOT NULL,
    scopes TEXT,
    raw TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (provider, channel, chat_id)
);
CREATE INDEX IF NOT EXISTS oauth_tokens_expires_at ON oauth_tokens(expires_at);

-- 统一推送层的投递目标。provider 决定 address 的含义：
-- fcm=FCM token / apns=device token / webpush=PushSubscription endpoint / feishu=chat_id。
-- metadata 存 provider 专属字段（WebPush 的 p256dh/auth、设备信息等），JSON。
CREATE TABLE IF NOT EXISTS push_targets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL,
    sender_id    TEXT    NOT NULL,
    provider     TEXT    NOT NULL,
    platform     TEXT    NOT NULL,
    address      TEXT    NOT NULL,
    metadata     TEXT    NOT NULL DEFAULT '{}',
    device_name  TEXT    NOT NULL DEFAULT '',
    app_version  TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL DEFAULT 0,
    last_seen_at INTEGER NOT NULL DEFAULT 0,
    UNIQUE(provider, address)
);
CREATE INDEX IF NOT EXISTS idx_push_targets_sender ON push_targets(sender_id);

-- 推送层的运行配置（单个 JSON blob，crypto.Encrypt 加密后落库）。
-- config.json 的 push 段只作为首次启动的种子，之后以这里为准。
CREATE TABLE IF NOT EXISTS push_settings (
    id         INTEGER PRIMARY KEY CHECK (id = 1),
    config     TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT 0
);
`

// ensureStartupSchema creates the tables that a database may legitimately lack
// even though its schema_version is already current — either because the table
// shipped after that version was introduced, or because it was created outside
// the migration chain entirely:
//
//   - oauth_tokens        (only ever created by the oauth storage/service path)
//   - xbot_*_memories     (agent memory: created by the memory provider on first use)
//
// Every statement is IF NOT EXISTS, so this is idempotent and cheap on each start.
//
// 启动路径铁律：xbot-server 由 supervisor 托管，Open() 返回错误就会「启动失败 → 自动
// 重启 → 再失败」崩溃循环。所以这里**只告警、不返回错误**，绝不阻断启动。
func ensureStartupSchema(ctx context.Context, db *DB) {
	if _, err := db.Conn().ExecContext(ctx, oauthTokensSchema); err != nil {
		log.WithError(err).Warn("Ensure oauth_tokens schema failed; continuing without it")
	}
	if err := NewAgentMemoryService(db).EnsureAgentMemorySchema(ctx); err != nil {
		log.WithError(err).Warn("Ensure agent memory schema failed; continuing without it")
	}
}
