package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgMemoryStore implements MemoryStore for PostgreSQL.
type pgMemoryStore struct {
	pool *pgxpool.Pool
}

func newPGMemoryStore(pool *pgxpool.Pool) MemoryStore {
	return &pgMemoryStore{pool: pool}
}

// ── Core memory blocks ──

func (s *pgMemoryStore) InitBlocks(tenantID int64, userID string) error {
	ctx := context.Background()
	for _, name := range []string{"persona", "human", "working_context"} {
		_, err := s.pool.Exec(ctx,
			`INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
			 VALUES ($1, $2, $3, '', 2000)
			 ON CONFLICT (tenant_id, block_name, user_id) DO NOTHING`,
			tenantID, name, userID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *pgMemoryStore) GetBlock(tenantID int64, blockName, userID string) (string, int, error) {
	ctx := context.Background()
	var content string
	var charLimit int
	err := s.pool.QueryRow(ctx,
		`SELECT content, char_limit FROM core_memory_blocks
		 WHERE tenant_id = $1 AND block_name = $2 AND user_id = $3`,
		tenantID, blockName, userID).Scan(&content, &charLimit)
	if err == pgx.ErrNoRows {
		return "", 2000, nil
	}
	return content, charLimit, err
}

func (s *pgMemoryStore) SetBlock(tenantID int64, blockName, content, userID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`INSERT INTO core_memory_blocks (tenant_id, block_name, user_id, content, char_limit)
		 VALUES ($1, $2, $3, $4, 2000)
		 ON CONFLICT (tenant_id, block_name, user_id) DO UPDATE SET content = $4, updated_at = now()`,
		tenantID, blockName, userID, content)
	return err
}

func (s *pgMemoryStore) GetAllBlocks(tenantID int64, userID string) (map[string]string, error) {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx,
		`SELECT block_name, content FROM core_memory_blocks
		 WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]string)
	for rows.Next() {
		var name, content string
		if err := rows.Scan(&name, &content); err != nil {
			return nil, err
		}
		result[name] = content
	}
	return result, nil
}

func (s *pgMemoryStore) ClearBlock(tenantID int64, blockName, userID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE core_memory_blocks SET content = '', updated_at = now()
		 WHERE tenant_id = $1 AND block_name = $2 AND user_id = $3`,
		tenantID, blockName, userID)
	return err
}

func (s *pgMemoryStore) ClearAllBlocks(tenantID int64, userID string) error {
	ctx := context.Background()
	_, err := s.pool.Exec(ctx,
		`UPDATE core_memory_blocks SET content = '', updated_at = now()
		 WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID)
	return err
}

// ── Long-term memory + state ──

func (s *pgMemoryStore) ReadLongTerm(ctx context.Context, tenantID int64) (string, error) {
	var content string
	err := s.pool.QueryRow(ctx,
		`SELECT content FROM long_term_memory WHERE tenant_id = $1`, tenantID).Scan(&content)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	return content, err
}

func (s *pgMemoryStore) WriteLongTerm(ctx context.Context, tenantID int64, content string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO long_term_memory (tenant_id, content) VALUES ($1, $2)
		 ON CONFLICT (tenant_id) DO UPDATE SET content = $2, updated_at = now()`,
		tenantID, content)
	return err
}

func (s *pgMemoryStore) ClearLongTerm(ctx context.Context, tenantID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM long_term_memory WHERE tenant_id = $1`, tenantID)
	return err
}

func (s *pgMemoryStore) GetState(ctx context.Context, tenantID int64) (int, error) {
	var lastConsolidated int
	err := s.pool.QueryRow(ctx,
		`SELECT last_consolidated FROM tenant_state WHERE tenant_id = $1`, tenantID).Scan(&lastConsolidated)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return lastConsolidated, err
}

func (s *pgMemoryStore) SetState(ctx context.Context, tenantID int64, lastConsolidated int) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_state (tenant_id, last_consolidated) VALUES ($1, $2)
		 ON CONFLICT (tenant_id) DO UPDATE SET last_consolidated = $2`,
		tenantID, lastConsolidated)
	return err
}

// ── Event history ──

func (s *pgMemoryStore) AppendHistory(ctx context.Context, tenantID int64, entry string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO event_history (tenant_id, entry) VALUES ($1, $2)`, tenantID, entry)
	return err
}

func (s *pgMemoryStore) GetHistoryEntries(ctx context.Context, tenantID int64, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT entry FROM event_history WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2`,
		tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var entry string
		if err := rows.Scan(&entry); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, nil
}

// GetHistoryEntriesInRange returns event_history rows within [start, end], newest
// first (limit <= 0 means 20) — the PG peer of the SQLite implementation: same
// defaulting and ordering, so recall behaves identically on both backends.
func (s *pgMemoryStore) GetHistoryEntriesInRange(ctx context.Context, tenantID int64, start, end time.Time, limit int) ([]HistoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.pool.Query(ctx, `
		SELECT entry, created_at FROM event_history
		WHERE tenant_id = $1 AND created_at >= $2 AND created_at <= $3
		ORDER BY created_at DESC LIMIT $4`, tenantID, start, end, limit)
	if err != nil {
		return nil, fmt.Errorf("recall time range: %w", err)
	}
	defer rows.Close()
	var entries []HistoryEntry
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Entry, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan recall entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recall entries: %w", err)
	}
	return entries, nil
}

func (s *pgMemoryStore) ClearHistory(ctx context.Context, tenantID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM event_history WHERE tenant_id = $1`, tenantID)
	return err
}
func (s *pgMemoryStore) GetHistoryCount(ctx context.Context, tenantID int64) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM event_history WHERE tenant_id = $1`, tenantID).Scan(&count)
	return count, err
}
func (s *pgMemoryStore) ClearState(ctx context.Context, tenantID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM tenant_state WHERE tenant_id = $1`, tenantID)
	return err
}
func (s *pgMemoryStore) SetTokenState(ctx context.Context, tenantID int64, promptTokens, completionTokens int64) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_state (tenant_id, last_prompt_tokens, last_completion_tokens)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id) DO UPDATE SET last_prompt_tokens = $2, last_completion_tokens = $3`,
		tenantID, promptTokens, completionTokens)
	return err
}

func (s *pgMemoryStore) GetTokenState(ctx context.Context, tenantID int64) (int64, int64, error) {
	var promptTokens, completionTokens int64
	err := s.pool.QueryRow(ctx,
		`SELECT last_prompt_tokens, last_completion_tokens FROM tenant_state WHERE tenant_id = $1`,
		tenantID).Scan(&promptTokens, &completionTokens)
	if err == pgx.ErrNoRows {
		return 0, 0, nil
	}
	return promptTokens, completionTokens, err
}
func (s *pgMemoryStore) SetPersonaIsolation(enabled bool) {
	// no-op for PG (persona isolation is an SQLite CoreMemoryService concept)
}
