package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/event"
)

// pgStorage implements Storage for PostgreSQL.
type pgStorage struct {
	pool    *pgxpool.Pool
	session SessionStore // per-session or shared (tenant_id-filtered)
	tenant  TenantStore
	memory  MemoryStore
	config  ConfigStore
	cron    CronStore
	trigger event.TriggerStore
	oauth   OAuthTokenStore
}

// NewPGStorage creates a Storage backed by PostgreSQL.
// dsn example: "postgres://user:pass@host:5432/xbot?sslmode=disable"
func NewPGStorage(ctx context.Context, dsn string) (Storage, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pg pool: %w", err)
	}

	// Init schema (idempotent — CREATE IF NOT EXISTS)
	if err := initPGSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("init pg schema: %w", err)
	}

	pg := &pgStorage{pool: pool}
	pg.tenant = newPGTenantStore(pool)
	pg.memory = newPGMemoryStore(pool)
	pg.config = newPGConfigStore(pool)
	pg.cron = newPGCronStore(pool)
	pg.trigger = newPGTriggerStore(pool)
	pg.oauth = newPGOAuthTokenStore(pool)
	pg.session = newPGSessionStore(pool) // shared pool, tenant_id-filtered
	return pg, nil
}

func (s *pgStorage) Session(tenantID int64) (SessionStore, error) {
	return s.session, nil // single shared pool; tenant_id filtering is per-method
}
func (s *pgStorage) Tenant() TenantStore         { return s.tenant }
func (s *pgStorage) Memory() MemoryStore         { return s.memory }
func (s *pgStorage) Config() ConfigStore         { return s.config }
func (s *pgStorage) Cron() CronStore             { return s.cron }
func (s *pgStorage) Trigger() event.TriggerStore { return s.trigger }
func (s *pgStorage) OAuth() OAuthTokenStore      { return s.oauth }
func (s *pgStorage) Close() error                { s.pool.Close(); return nil }

// CopySessionFrom copies a session's history with two `INSERT … SELECT`
// statements, in one transaction.
//
// Fork used to read every row into the process and write it back — 2235 messages
// plus 811 iteration records meant ~2.6MB across the wire twice, and the rows are
// already in the database we are writing to. `INSERT … SELECT` does the whole
// copy server-side: no payload transfer, no per-row round trip.
//
// Two deliberate equivalences with the old read-then-write path:
//
//   - Control rows (compression/mask/context_edit bookkeeping) are flattened to
//     plain `message` rows, exactly as `AppendMessages` did (it always wrote
//     record_type='message', target_history_id=0, record_data=”).
//   - The one sanitize rule that actually fires in production is applied in the
//     SELECT: empty assistant shells (no content, no tool_calls) are skipped.
//     Over 7 days of logs that was 209 of 229 sanitize events; the tool-pair
//     repair (Pass 3) never fired, and it is a read-time concern anyway — every
//     turn re-sanitizes before building the LLM context (agent/engine_run.go).
func (s *pgStorage) CopySessionFrom(dstTenantID, srcTenantID int64) (int, error) {
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("copy session: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `INSERT INTO session_messages
		 (tenant_id, role, content, tool_call_id, tool_name, tool_arguments, tool_calls,
		  detail, reasoning_content, reasoning_items, display_only, internal_only,
		  context_tokens, turn_id, iteration, record_type, target_history_id, record_data)
		SELECT $1, role, content, tool_call_id, tool_name, tool_arguments, tool_calls,
		       detail, reasoning_content, reasoning_items, display_only, internal_only,
		       0, turn_id, iteration, 'message', 0, ''
		FROM session_messages
		WHERE tenant_id = $2
		  AND NOT (role = 'assistant' AND content = ''
		           AND (tool_calls IS NULL OR tool_calls IN ('', 'null', '[]')))
		ORDER BY id`, dstTenantID, srcTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session messages: %w", err)
	}
	n := int(tag.RowsAffected())

	if _, err := tx.Exec(ctx, `INSERT INTO iteration_history
		 (message_id, tenant_id, turn_id, iteration, content, reasoning, tools, tokens,
		  ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens,
		  model, subscription_id)
		SELECT 0, $1, turn_id, iteration, content, reasoning, tools, tokens,
		       ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens,
		       model, subscription_id
		FROM iteration_history WHERE tenant_id = $2 ORDER BY id`, dstTenantID, srcTenantID); err != nil {
		return 0, fmt.Errorf("copy session iteration history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("copy session: commit: %w", err)
	}
	return n, nil
}

var _ Storage = (*pgStorage)(nil)

// InitPGSchema applies the PostgreSQL schema (idempotent). Exported so tooling —
// e.g. the SQLite→PG migration command — creates exactly the schema the runtime
// uses instead of duplicating DDL.
func InitPGSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return initPGSchema(ctx, pool)
}
