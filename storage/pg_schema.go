package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// initPGSchema creates all tables in a fresh PG database.
// PG starts at schema version 73 (same as SQLite current) — no
// historical migration chain is ported (v1-v72 are SQLite-specific).
// All DDL is idempotent (CREATE TABLE IF NOT EXISTS).
func initPGSchema(ctx context.Context, pool *pgxpool.Pool) error {
	queries := []string{
		// ── Tenants (registry) ──
		`CREATE TABLE IF NOT EXISTS tenants (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			runner_id TEXT NOT NULL DEFAULT '',
			subscription_id TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			model_id TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_active_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			cwd TEXT NOT NULL DEFAULT '',
			db_path TEXT NOT NULL DEFAULT '',
			migrated BOOLEAN NOT NULL DEFAULT false,
			preview TEXT NOT NULL DEFAULT '',
			UNIQUE(channel, chat_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_tenants_channel_chat ON tenants(channel, chat_id)`,

		// ── Session messages ──
		`CREATE TABLE IF NOT EXISTS session_messages (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			role TEXT NOT NULL,
			content TEXT NOT NULL DEFAULT '',
			tool_call_id TEXT,
			tool_name TEXT,
			tool_arguments TEXT,
			tool_calls TEXT,
			detail TEXT,
			reasoning_content TEXT NOT NULL DEFAULT '',
			reasoning_items TEXT NOT NULL DEFAULT '',
			display_only BOOLEAN NOT NULL DEFAULT false,
			internal_only BOOLEAN NOT NULL DEFAULT false,
			context_tokens INTEGER NOT NULL DEFAULT 0,
			turn_id INTEGER NOT NULL DEFAULT 0,
			iteration INTEGER NOT NULL DEFAULT 0,
			record_type TEXT NOT NULL DEFAULT 'message',
			target_history_id INTEGER,
			record_data TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sm_tenant_created ON session_messages(tenant_id, created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_sm_tenant_id ON session_messages(tenant_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_sm_tenant_record ON session_messages(tenant_id, record_type, target_history_id) WHERE record_type != 'message'`,
		`CREATE INDEX IF NOT EXISTS idx_sm_tenant_role_id ON session_messages(tenant_id, role, id) WHERE role IN ('user','assistant')`,

		// ── Tenant state ──
		`CREATE TABLE IF NOT EXISTS tenant_state (
			tenant_id BIGINT PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
			last_consolidated INTEGER NOT NULL DEFAULT 0,
			last_prompt_tokens BIGINT NOT NULL DEFAULT 0,
			last_completion_tokens BIGINT NOT NULL DEFAULT 0
		)`,

		// ── Long-term memory ──
		`CREATE TABLE IF NOT EXISTS long_term_memory (
			tenant_id BIGINT PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
			content TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,

		// ── Event history ──
		`CREATE TABLE IF NOT EXISTS event_history (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			entry TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_eh_tenant_created ON event_history(tenant_id, created_at)`,

		// ── User profiles ──
		`CREATE TABLE IF NOT EXISTS user_profiles (
			sender_id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			profile TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,

		// ── Core memory blocks ──
		`CREATE TABLE IF NOT EXISTS core_memory_blocks (
			tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			block_name TEXT NOT NULL,
			user_id TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			char_limit INTEGER NOT NULL DEFAULT 2000,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (tenant_id, block_name, user_id)
		)`,
		`CREATE TABLE IF NOT EXISTS core_memory_migrations (
			name TEXT PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,

		// ── Archival memory (kept for compatibility; vectordb uses chromem-go files) ──
		`CREATE TABLE IF NOT EXISTS archival_memory (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			content TEXT NOT NULL DEFAULT '',
			embedding BYTEA,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_archival_tenant ON archival_memory(tenant_id)`,

		// ── Schema version ──
		`CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY
		)`,
		`INSERT INTO schema_version (version) VALUES (73) ON CONFLICT DO NOTHING`,

		// ── Token usage ──
		`CREATE TABLE IF NOT EXISTS user_token_usage (
			sender_id TEXT PRIMARY KEY,
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			total_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			conversation_count INTEGER NOT NULL DEFAULT 0,
			llm_call_count INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS daily_token_usage (
			date TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			input_tokens BIGINT NOT NULL DEFAULT 0,
			output_tokens BIGINT NOT NULL DEFAULT 0,
			cached_tokens BIGINT NOT NULL DEFAULT 0,
			conversation_count INTEGER NOT NULL DEFAULT 0,
			llm_call_count INTEGER NOT NULL DEFAULT 0,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (date, sender_id, model)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_token_sender ON daily_token_usage(sender_id)`,
		`CREATE INDEX IF NOT EXISTS idx_daily_token_date ON daily_token_usage(date)`,

		// ── LLM subscriptions ──
		`CREATE TABLE IF NOT EXISTS user_llm_subscriptions (
			id TEXT PRIMARY KEY,
			sender_id TEXT NOT NULL,
			name TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT 'openai',
			base_url TEXT NOT NULL DEFAULT '',
			api_key TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			max_context INTEGER NOT NULL DEFAULT 0,
			max_output_tokens INTEGER NOT NULL DEFAULT 0,
			thinking_mode TEXT NOT NULL DEFAULT '',
			cached_models TEXT DEFAULT '',
			api_type TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_llm_subs_sender ON user_llm_subscriptions(sender_id)`,

		// ── Subscription models ──
		`CREATE TABLE IF NOT EXISTS subscription_models (
			id TEXT PRIMARY KEY,
			subscription_id TEXT NOT NULL REFERENCES user_llm_subscriptions(id) ON DELETE CASCADE,
			model TEXT NOT NULL,
			max_context INTEGER NOT NULL DEFAULT 0,
			max_output_tokens INTEGER NOT NULL DEFAULT 0,
			thinking_mode TEXT NOT NULL DEFAULT '',
			api_type TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true,
			vision BOOLEAN NOT NULL DEFAULT false,
			vision_detail TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sub_models_sub ON subscription_models(subscription_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_sub_models_uniq ON subscription_models(subscription_id, model)`,

		// ── User default model ──
		`CREATE TABLE IF NOT EXISTS user_default_model (
			sender_id TEXT PRIMARY KEY,
			subscription_id TEXT NOT NULL,
			model TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,

		// ── Runners ──
		`CREATE TABLE IF NOT EXISTS runners (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			token TEXT NOT NULL,
			mode TEXT NOT NULL DEFAULT 'native',
			docker_image TEXT NOT NULL DEFAULT '',
			workspace TEXT NOT NULL DEFAULT '',
			llm_provider TEXT NOT NULL DEFAULT '',
			llm_api_key TEXT NOT NULL DEFAULT '',
			llm_model TEXT NOT NULL DEFAULT '',
			llm_base_url TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_runners_token ON runners(token)`,

		// ── Web users ──
		`CREATE TABLE IF NOT EXISTS web_users (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			username TEXT NOT NULL UNIQUE,
			password TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,

		// ── User settings ──
		`CREATE TABLE IF NOT EXISTS user_settings (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			channel TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			key TEXT NOT NULL,
			value TEXT NOT NULL DEFAULT '',
			updated_at BIGINT NOT NULL DEFAULT 0,
			UNIQUE(channel, sender_id, key)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_settings_sender ON user_settings(channel, sender_id)`,

		// ── Cron jobs ──
		`CREATE TABLE IF NOT EXISTS cron_jobs (
			id TEXT PRIMARY KEY,
			message TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL DEFAULT '',
			cron_expr TEXT,
			every_seconds INTEGER NOT NULL DEFAULT 0,
			delay_seconds INTEGER NOT NULL DEFAULT 0,
			at TEXT,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			next_run TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_trigger TIMESTAMPTZ,
			one_shot BOOLEAN NOT NULL DEFAULT false
		)`,
		`CREATE INDEX IF NOT EXISTS idx_cron_next_run ON cron_jobs(next_run)`,
		`CREATE INDEX IF NOT EXISTS idx_cron_sender ON cron_jobs(sender_id)`,

		// ── Event triggers ──
		`CREATE TABLE IF NOT EXISTS event_triggers (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL DEFAULT '',
			event_type TEXT NOT NULL DEFAULT 'webhook',
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			message_tpl TEXT NOT NULL,
			secret TEXT NOT NULL DEFAULT '',
			enabled BOOLEAN NOT NULL DEFAULT true,
			one_shot BOOLEAN NOT NULL DEFAULT false,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_fired TIMESTAMPTZ,
			fire_count INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_triggers_sender ON event_triggers(sender_id)`,
		`CREATE INDEX IF NOT EXISTS idx_triggers_type ON event_triggers(event_type, enabled)`,

		// ── User chats ──
		`CREATE TABLE IF NOT EXISTS user_chats (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			channel TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			label TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			sort_order INTEGER NOT NULL DEFAULT 0,
			UNIQUE(channel, sender_id, chat_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_user_chats_sender ON user_chats(channel, sender_id)`,

		// ── Pending resumes ──
		`CREATE TABLE IF NOT EXISTS pending_resumes (
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			sender_id TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (channel, chat_id)
		)`,

		// ── Iteration history ──
		`CREATE TABLE IF NOT EXISTS iteration_history (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			message_id BIGINT NOT NULL DEFAULT 0,
			tenant_id BIGINT NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
			turn_id BIGINT NOT NULL DEFAULT 0,
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
			created_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_iter_history_msg ON iteration_history(message_id)`,
		`CREATE INDEX IF NOT EXISTS idx_iter_history_turn ON iteration_history(tenant_id, turn_id)`,
		`CREATE INDEX IF NOT EXISTS idx_iter_history_turn_iter ON iteration_history(tenant_id, turn_id, iteration)`,

		// ── Shared artifacts ──
		`CREATE TABLE IF NOT EXISTS shared_artifacts (
			token TEXT PRIMARY KEY,
			plugin_id TEXT NOT NULL DEFAULT '',
			content_type TEXT NOT NULL,
			payload TEXT NOT NULL,
			title TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			created_by BIGINT NOT NULL DEFAULT 0,
			expires_at TIMESTAMPTZ,
			revoked_at TIMESTAMPTZ
		)`,

		// ── OAuth tokens ──
		`CREATE TABLE IF NOT EXISTS oauth_tokens (
			provider TEXT NOT NULL,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			access_token TEXT NOT NULL DEFAULT '',
			refresh_token TEXT NOT NULL DEFAULT '',
			expires_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (provider, channel, chat_id)
		)`,

		// ── Agent memory (official "xbot" provider) ──
		// Native PG full-text search: a generated tsvector column + GIN index.
		// No extension is required (verified on PG 16: to_tsvector('simple', ...)
		// treats a CJK run as one token, exactly like SQLite's unicode61, and the
		// provider hands over CJK-space-separated search_text, so the token streams
		// line up on both backends).
		`CREATE TABLE IF NOT EXISTS xbot_short_term_memories (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			user_id BIGINT NOT NULL DEFAULT 0,
			tenant_id BIGINT NOT NULL DEFAULT 0,
			session_id TEXT NOT NULL DEFAULT '',
			summary TEXT NOT NULL DEFAULT '',
			key_topics TEXT NOT NULL DEFAULT '',
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			access_count INTEGER NOT NULL DEFAULT 0,
			heat_score DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			-- Mirrors the SQLite short-term FTS columns (summary, key_topics).
			search_tsv tsvector GENERATED ALWAYS AS (to_tsvector('simple', summary || ' ' || key_topics)) STORED
		)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_stm_user ON xbot_short_term_memories(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_stm_session ON xbot_short_term_memories(session_id)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_stm_tsv ON xbot_short_term_memories USING GIN (search_tsv)`,

		`CREATE TABLE IF NOT EXISTS xbot_long_term_memories (
			id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			user_id BIGINT NOT NULL DEFAULT 0,
			tenant_id BIGINT NOT NULL DEFAULT 0,
			type TEXT NOT NULL DEFAULT '',
			content TEXT NOT NULL DEFAULT '',
			keywords TEXT NOT NULL DEFAULT '',
			tags TEXT NOT NULL DEFAULT '',
			source_session TEXT NOT NULL DEFAULT '',
			importance DOUBLE PRECISION NOT NULL DEFAULT 0.5,
			heat_score DOUBLE PRECISION NOT NULL DEFAULT 1.0,
			access_count INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			last_accessed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			file_path TEXT NOT NULL DEFAULT '',
			search_text TEXT NOT NULL DEFAULT '',
			scope TEXT NOT NULL DEFAULT 'global',
			superseded_by BIGINT,
			-- Same field set as the SQLite FTS5 index (content, keywords, tags,
			-- search_text): indexing search_text alone would make a query on a
			-- keyword miss on PG while hitting on SQLite.
			search_tsv tsvector GENERATED ALWAYS AS (
				to_tsvector('simple', content || ' ' || keywords || ' ' || tags || ' ' || search_text)
			) STORED
		)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_ltm_user ON xbot_long_term_memories(user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_ltm_type ON xbot_long_term_memories(type)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_ltm_heat ON xbot_long_term_memories(heat_score)`,
		`CREATE INDEX IF NOT EXISTS idx_xbot_ltm_tsv ON xbot_long_term_memories USING GIN (search_tsv)`,

		// ── Seed: shared tenant ──
		`INSERT INTO tenants (id, channel, chat_id) OVERRIDING SYSTEM VALUE
		 VALUES (0, '_shared', '_shared')
		 ON CONFLICT (channel, chat_id) DO NOTHING`,
	}

	for _, q := range queries {
		if _, err := pool.Exec(ctx, q); err != nil {
			return fmt.Errorf("exec DDL: %w\nquery: %s", err, q)
		}
	}
	return nil
}
