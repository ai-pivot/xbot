package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// pgStorage implements Storage for PostgreSQL.
type pgStorage struct {
	pool    *pgxpool.Pool
	session SessionStore // per-session or shared (tenant_id-filtered)
	tenant  TenantStore
	memory  MemoryStore
	config  ConfigStore
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
	pg.session = newPGSessionStore(pool) // shared pool, tenant_id-filtered
	return pg, nil
}

func (s *pgStorage) Session(tenantID int64) (SessionStore, error) {
	return s.session, nil // single shared pool; tenant_id filtering is per-method
}
func (s *pgStorage) Tenant() TenantStore { return s.tenant }
func (s *pgStorage) Memory() MemoryStore { return s.memory }
func (s *pgStorage) Config() ConfigStore { return s.config }
func (s *pgStorage) Close() error        { s.pool.Close(); return nil }

var _ Storage = (*pgStorage)(nil)
