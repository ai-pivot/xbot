package pg

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/event"
	log "xbot/logger"
	"xbot/storage/common"
)

// backend implements Storage for PostgreSQL.
type backend struct {
	pool     *pgxpool.Pool
	session  SessionStore // per-session or shared (tenant_id-filtered)
	tenant   TenantStore
	memory   MemoryStore
	config   ConfigStore
	cron     CronStore
	trigger  event.TriggerStore
	oauth    OAuthTokenStore
	agents   InteractiveAgentStore
	groups   GroupStore
	activity ActivityStore
	runners  RunnerStore
}

// NewStorage creates a Storage backed by PostgreSQL.
// dsn example: "postgres://user:pass@host:5432/xbot?sslmode=disable"
func NewStorage(ctx context.Context, dsn string) (Storage, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("open pg pool: %w", err)
	}

	// Init schema (idempotent — CREATE IF NOT EXISTS)
	if err := initSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("init pg schema: %w", err)
	}

	return NewStorageWithPool(pool)
}

// NewStorageWithPool wires every PG store onto an existing pool. Exported so
// tests (and embedding callers that own pool lifecycle) can construct the
// backend without a second connection. A nil field panics on first use, so this
// is also the single wiring guard.
func NewStorageWithPool(pool *pgxpool.Pool) (Storage, error) {
	pg := &backend{pool: pool}
	pg.tenant = newTenantStore(pool)
	pg.memory = newMemoryStore(pool)
	pg.config = newConfigStore(pool)
	pg.cron = newCronStore(pool)
	pg.trigger = newTriggerStore(pool)
	pg.oauth = newOAuthTokenStore(pool)
	pg.agents = newInteractiveAgentStore(pool)
	pg.groups = newGroupStore(pool)
	pg.activity = newActivityStore(pool)
	pg.runners = newRunnerStore(pool)
	pg.session = newSessionStore(pool) // shared pool, tenant_id-filtered
	return pg, nil
}

func (s *backend) Session(tenantID int64) (SessionStore, error) {
	return s.session, nil // single shared pool; tenant_id filtering is per-method
}
func (s *backend) Tenant() TenantStore                      { return s.tenant }
func (s *backend) Memory() MemoryStore                      { return s.memory }
func (s *backend) Config() ConfigStore                      { return s.config }
func (s *backend) Cron() CronStore                          { return s.cron }
func (s *backend) Trigger() event.TriggerStore              { return s.trigger }
func (s *backend) OAuth() OAuthTokenStore                   { return s.oauth }
func (s *backend) InteractiveAgents() InteractiveAgentStore { return s.agents }
func (s *backend) Groups() GroupStore                       { return s.groups }
func (s *backend) Activity() ActivityStore                  { return s.activity }
func (s *backend) Runners() RunnerStore                     { return s.runners }
func (s *backend) Close() error                             { s.pool.Close(); return nil }

// CopySessionFrom copies a session's conversation into another session.
//
// ⛔ The messages are NOT a raw `INSERT … SELECT`: the conversation is a derived
// view of the append-only log (Replay collapses to the latest compression
// checkpoint — whose summary lives in `record_data` JSON, not in rows — applies
// masks, and drops display-only rows). Copying raw rows therefore duplicated the
// source session's cron notifications into the fork. See copySessionMessages.
//
// The halves are treated differently because they have different natures:
//
//   - messages: materialised from the replayed view in Go, written with one batched
//     insert (the payload has to cross the process; the row count did not).
//   - iteration_history: a pure copy — no derivation — so it stays entirely inside
//     the database as one `INSERT … SELECT` and no rows cross the wire.
//
// Control rows (compression/mask bookkeeping) are already folded into the replayed
// messages, so nothing about them needs copying.
func (s *backend) CopySessionFrom(dstTenantID, srcTenantID int64) (int, error) {
	src, err := s.Session(srcTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session: open source: %w", err)
	}
	dst, err := s.Session(dstTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session: open destination: %w", err)
	}

	msgs, err := common.CopySessionMessages(src, dst, srcTenantID, dstTenantID)
	if err != nil {
		return 0, fmt.Errorf("copy session: %w", err)
	}

	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `INSERT INTO iteration_history
		 (message_id, tenant_id, turn_id, iteration, content, reasoning, tools, tokens,
		  ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens,
		  model, subscription_id)
		SELECT 0, $1, turn_id, iteration, content, reasoning, tools, tokens,
		       ttft_ms, tokens_per_sec, total_ms, tpot_ms, input_tokens, cached_tokens,
		       model, subscription_id
		FROM iteration_history WHERE tenant_id = $2 ORDER BY id`, dstTenantID, srcTenantID); err != nil {
		// Iteration detail is a rendering nicety, not context data.
		log.WithError(err).Warn("copy session: iteration history copy failed (skipped)")
	}

	return len(msgs), nil
}

var _ Storage = (*backend)(nil)

// InitSchema applies the PostgreSQL schema (idempotent). Exported so tooling —
// e.g. the SQLite→PG migration command — creates exactly the schema the runtime
// uses instead of duplicating DDL.
func InitSchema(ctx context.Context, pool *pgxpool.Pool) error {
	return initSchema(ctx, pool)
}
