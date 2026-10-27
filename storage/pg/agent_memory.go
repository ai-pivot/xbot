package pg

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"xbot/storage/agentmem"
)

// agentMemoryStore implements AgentMemoryStore on PostgreSQL.
//
// Full-text search uses a generated `search_tsv` column + GIN index with
// to_tsvector('simple', ...). No extension is needed: PG's 'simple' config treats
// a CJK run as ONE token, exactly like SQLite's unicode61, and the provider hands
// over CJK-space-separated text/terms, so both backends produce the same tokens.
//
// Ranking direction is adapted HERE: SQLite bm25() is negative (lower = better)
// while PG ts_rank is positive (higher = better). The provider only ever sees
// best-first ordered results.
type agentMemoryStore struct {
	pool *pgxpool.Pool
}

func newAgentMemoryStore(pool *pgxpool.Pool) *agentMemoryStore {
	return &agentMemoryStore{pool: pool}
}

// dedupRankThreshold mirrors the SQLite bm25 threshold (-6.0) on the ts_rank
// scale (0..1). A match this strong means the facts overlap.
const dedupRankThreshold = 0.05

// EnsureAgentMemorySchema is a no-op on PG: the tables are part of
// initSchema, which runs once when the storage is created (idempotent DDL).
func (s *agentMemoryStore) EnsureAgentMemorySchema(ctx context.Context) error { return nil }

// ── Short-term memories ──

func (s *agentMemoryStore) AddAgentShortTermMemory(ctx context.Context, mem agentmem.ShortTermMemory) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO xbot_short_term_memories (user_id, tenant_id, session_id, summary, key_topics)
		VALUES ($1, $2, $3, $4, $5) RETURNING id
	`, mem.UserID, mem.TenantID, mem.SessionID, mem.Summary, mem.KeyTopics).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add agent short-term memory: %w", err)
	}
	return id, nil
}

func (s *agentMemoryStore) SearchAgentShortTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.ShortTermMemory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	var rows pgx.Rows
	var err error
	if len(q.Terms) == 0 {
		rows, err = s.pool.Query(ctx, `
			SELECT id, user_id, tenant_id, session_id, summary, key_topics,
			       created_at, last_accessed_at, access_count, heat_score
			FROM xbot_short_term_memories WHERE user_id = $1
			ORDER BY created_at DESC LIMIT $2`, q.UserID, limit)
	} else {
		rows, err = s.pool.Query(ctx, `
			SELECT id, user_id, tenant_id, session_id, summary, key_topics,
			       created_at, last_accessed_at, access_count, heat_score
			FROM xbot_short_term_memories
			WHERE user_id = $1 AND search_tsv @@ $2::tsquery
			ORDER BY ts_rank(search_tsv, $2::tsquery) DESC LIMIT $3`,
			q.UserID, tsQuery(q.Terms, q.Join), limit)
	}
	if err != nil {
		return nil, fmt.Errorf("search agent short-term memories: %w", err)
	}
	defer rows.Close()
	var out []agentmem.ShortTermMemory
	for rows.Next() {
		var m agentmem.ShortTermMemory
		if err := rows.Scan(&m.ID, &m.UserID, &m.TenantID, &m.SessionID, &m.Summary, &m.KeyTopics,
			&m.CreatedAt, &m.LastAccessedAt, &m.AccessCount, &m.HeatScore); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *agentMemoryStore) EvictAgentShortTermMemories(ctx context.Context, userID, tenantID int64, keep int) error {
	if keep <= 0 {
		keep = 5
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM xbot_short_term_memories
		WHERE user_id = $1 AND id NOT IN (
			SELECT id FROM xbot_short_term_memories WHERE user_id = $1
			ORDER BY created_at DESC LIMIT $2
		)`, userID, keep)
	if err != nil {
		return fmt.Errorf("evict agent short-term memories: %w", err)
	}
	return nil
}

// ── Long-term memories ──

const agentLongTermCols = `id, user_id, tenant_id, type, content, keywords, tags, source_session,
	importance, heat_score, access_count, created_at, last_accessed_at, file_path,
	search_text, scope, COALESCE(superseded_by, 0)`

func (s *agentMemoryStore) AddAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) (int64, error) {
	scope := mem.Scope
	if scope == "" {
		scope = "global"
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO xbot_long_term_memories
			(user_id, tenant_id, type, content, keywords, tags, source_session, importance, search_text, scope)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id
	`, mem.UserID, mem.TenantID, mem.Type, mem.Content, mem.Keywords, mem.Tags,
		mem.SourceSession, mem.Importance, mem.SearchText, scope).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add agent long-term memory: %w", err)
	}
	return id, nil
}

// agentOrderSQL maps the neutral ordering onto PG. Relevance flips to DESC
// because ts_rank is higher-is-better (bm25 is the opposite on SQLite).
func agentOrderSQL(order agentmem.Order, hasQuery bool) string {
	switch order {
	case agentmem.OrderRelevance:
		if hasQuery {
			return "ts_rank(search_tsv, $2::tsquery) DESC"
		}
		return "importance DESC, created_at DESC"
	case agentmem.OrderRecency:
		return "created_at DESC"
	default:
		return "importance DESC, heat_score DESC, created_at DESC"
	}
}

func (s *agentMemoryStore) SearchAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	where := []string{"user_id = $1"}
	args := []any{q.UserID}
	argn := 1
	add := func(clause string, v any) {
		argn++
		where = append(where, fmt.Sprintf(clause, argn))
		args = append(args, v)
	}
	if !q.IncludeSuperseded {
		where = append(where, "superseded_by IS NULL")
	}
	if q.Type != "" {
		add("type = $%d", q.Type)
	}
	if q.Scope != "" {
		add("scope = $%d", q.Scope)
	}
	if q.SourceSession != "" {
		add("source_session = $%d", q.SourceSession)
	}
	hasQuery := len(q.Terms) > 0
	rankExpr := "0::float8 AS relevance"
	if hasQuery {
		argn++
		where = append(where, fmt.Sprintf("search_tsv @@ $%d::tsquery", argn))
		args = append(args, tsQuery(q.Terms, q.Join))
		rankExpr = fmt.Sprintf("ts_rank(search_tsv, $%d::tsquery) AS relevance", argn)
	}
	query := `SELECT ` + agentLongTermCols + `, ` + rankExpr + `
	          FROM xbot_long_term_memories WHERE ` + strings.Join(where, " AND ") + `
	          ORDER BY ` + agentOrderSQL(q.Order, hasQuery) + fmt.Sprintf(` LIMIT $%d`, argn+1)
	args = append(args, limit)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search agent long-term memories: %w", err)
	}
	defer rows.Close()
	return scanAgentLongTerm(rows)
}

func scanAgentLongTerm(rows pgx.Rows) ([]agentmem.LongTermMemory, error) {
	var out []agentmem.LongTermMemory
	for rows.Next() {
		var m agentmem.LongTermMemory
		if err := rows.Scan(&m.ID, &m.UserID, &m.TenantID, &m.Type, &m.Content, &m.Keywords, &m.Tags,
			&m.SourceSession, &m.Importance, &m.HeatScore, &m.AccessCount, &m.CreatedAt, &m.LastAccessedAt,
			&m.FilePath, &m.SearchText, &m.Scope, &m.SupersededBy, &m.Relevance); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *agentMemoryStore) FindAgentDuplicateLongTerm(ctx context.Context, q agentmem.Query) (int64, error) {
	if len(q.Terms) == 0 {
		return 0, nil
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		SELECT id FROM xbot_long_term_memories
		WHERE user_id = $1 AND superseded_by IS NULL
		  AND (scope = 'global' OR (scope = 'session' AND source_session = $2))
		  AND search_tsv @@ $3::tsquery
		  AND ts_rank(search_tsv, $3::tsquery) >= $4
		ORDER BY ts_rank(search_tsv, $3::tsquery) DESC LIMIT 1
	`, q.UserID, q.SourceSession, tsQuery(q.Terms, agentmem.JoinAll), dedupRankThreshold).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find agent duplicate long-term memory: %w", err)
	}
	return id, nil
}

func (s *agentMemoryStore) SupersedeAgentLongTerm(ctx context.Context, q agentmem.Query, newID int64) ([]int64, error) {
	if len(q.Terms) == 0 {
		return nil, nil
	}
	tsq := tsQuery(q.Terms, agentmem.JoinAll)
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM xbot_long_term_memories
		WHERE user_id = $1 AND scope = 'global' AND superseded_by IS NULL
		  AND id < $2 AND search_tsv @@ $3::tsquery
		  AND ts_rank(search_tsv, $3::tsquery) >= $4
	`, q.UserID, newID, tsq, dedupRankThreshold)
	if err != nil {
		return nil, fmt.Errorf("find stale agent long-term memories: %w", err)
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			stale = append(stale, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var superseded []int64
	for _, id := range stale {
		tag, err := s.pool.Exec(ctx, `
			UPDATE xbot_long_term_memories SET superseded_by = $1, updated_at = now()
			WHERE id = $2 AND superseded_by IS NULL`, newID, id)
		if err != nil {
			return superseded, fmt.Errorf("supersede agent long-term memory %d: %w", id, err)
		}
		if tag.RowsAffected() > 0 {
			superseded = append(superseded, id)
		}
	}
	return superseded, nil
}

func (s *agentMemoryStore) ListAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	where := []string{"user_id = $1"}
	args := []any{q.UserID}
	if !q.IncludeSuperseded {
		where = append(where, "superseded_by IS NULL")
	}
	if q.Type != "" {
		where = append(where, fmt.Sprintf("type = $%d", len(args)+1))
		args = append(args, q.Type)
	}
	query := `SELECT ` + agentLongTermCols + `, 0::float8 AS relevance FROM xbot_long_term_memories
	          WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ` +
		agentOrderSQL(q.Order, false) + fmt.Sprintf(` LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent long-term memories: %w", err)
	}
	defer rows.Close()
	return scanAgentLongTerm(rows)
}

func (s *agentMemoryStore) GetAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) (agentmem.LongTermMemory, error) {
	var m agentmem.LongTermMemory
	err := s.pool.QueryRow(ctx, `SELECT `+agentLongTermCols+`, 0::float8 AS relevance
		FROM xbot_long_term_memories WHERE id = $1 AND user_id = $2`, id, userID).
		Scan(&m.ID, &m.UserID, &m.TenantID, &m.Type, &m.Content, &m.Keywords, &m.Tags,
			&m.SourceSession, &m.Importance, &m.HeatScore, &m.AccessCount, &m.CreatedAt, &m.LastAccessedAt,
			&m.FilePath, &m.SearchText, &m.Scope, &m.SupersededBy, &m.Relevance)
	if err != nil {
		return agentmem.LongTermMemory{}, fmt.Errorf("get agent long-term memory %d: %w", id, err)
	}
	return m, nil
}

func (s *agentMemoryStore) UpdateAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE xbot_long_term_memories
		SET content = $1, keywords = $2, tags = $3, importance = $4, search_text = $5, updated_at = now()
		WHERE id = $6 AND user_id = $7
	`, mem.Content, mem.Keywords, mem.Tags, mem.Importance, mem.SearchText, mem.ID, mem.UserID)
	if err != nil {
		return fmt.Errorf("update agent long-term memory %d: %w", mem.ID, err)
	}
	return nil
}

func (s *agentMemoryStore) DeleteAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM xbot_long_term_memories WHERE id = $1 AND user_id = $2`, id, userID)
	if err != nil {
		return fmt.Errorf("delete agent long-term memory %d: %w", id, err)
	}
	return nil
}

func (s *agentMemoryStore) TouchAgentLongTermMemory(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE xbot_long_term_memories
		SET access_count = access_count + 1, last_accessed_at = now() WHERE id = $1`, id)
	return err
}

func (s *agentMemoryStore) DecayAgentLongTerm(ctx context.Context, userID, tenantID int64, halfLifeDays, forgetThreshold float64) error {
	// Same formula as SQLite: heat = importance * exp(-days/halfLife) * log(1+access)
	if _, err := s.pool.Exec(ctx, `
		UPDATE xbot_long_term_memories
		SET heat_score = importance *
		    exp(-(EXTRACT(EPOCH FROM (now() - last_accessed_at)) / 86400.0) / $1) *
		    ln(1 + access_count)
		WHERE user_id = $2`, halfLifeDays, userID); err != nil {
		return fmt.Errorf("decay agent long-term heat: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		UPDATE xbot_long_term_memories SET importance = importance * 0.95
		WHERE heat_score < $1 AND user_id = $2`, forgetThreshold, userID); err != nil {
		return fmt.Errorf("decay agent long-term importance: %w", err)
	}
	return nil
}

func (s *agentMemoryStore) PruneAgentLongTerm(ctx context.Context, userID, tenantID int64, maxEntries int) error {
	if maxEntries <= 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		DELETE FROM xbot_long_term_memories
		WHERE user_id = $1 AND id NOT IN (
			SELECT id FROM xbot_long_term_memories WHERE user_id = $1
			ORDER BY heat_score DESC, importance DESC LIMIT $2
		)`, userID, maxEntries)
	if err != nil {
		return fmt.Errorf("prune agent long-term memories: %w", err)
	}
	return nil
}

func (s *agentMemoryStore) ListAgentLongTermMissingSearchText(ctx context.Context, userID, tenantID int64, limit int) ([]agentmem.LongTermMemory, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `SELECT `+agentLongTermCols+`, 0::float8 AS relevance
		FROM xbot_long_term_memories WHERE user_id = $1 AND search_text = '' LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list agent memories missing search_text: %w", err)
	}
	defer rows.Close()
	return scanAgentLongTerm(rows)
}

func (s *agentMemoryStore) SetAgentLongTermSearchText(ctx context.Context, id int64, searchText string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE xbot_long_term_memories SET search_text = $1 WHERE id = $2`, searchText, id)
	return err
}

// tsQuery renders pre-tokenized terms as a tsquery. Each term is quoted as a
// lexeme (":" escapes are stripped by quoting) and joined with | (any) or &
// (all) — the PG counterpart of the FTS5 OR/AND expression.
func tsQuery(terms []string, join agentmem.TermJoin) string {
	if len(terms) == 0 {
		return ""
	}
	sep := " | "
	if join == agentmem.JoinAll {
		sep = " & "
	}
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		// Strip tsquery operator characters; the provider already sanitized CJK
		// splitting and stopwords, this only guards the lexeme syntax.
		clean := strings.NewReplacer("'", "", ":", "", "&", "", "|", "", "!", "", "(", "", ")", "", "*", "").Replace(t)
		if clean == "" {
			continue
		}
		quoted = append(quoted, "'"+clean+"'")
	}
	if len(quoted) == 0 {
		return ""
	}
	return strings.Join(quoted, sep)
}

// keep time imported for the row structs' time fields.
var _ = time.Time{}

// AddAgentLongTermMemoryWithSupersede inserts the memory and supersedes older
// strongly-matching GLOBAL entries in ONE transaction (same crash-consistency
// contract as SQLite).
func (s *agentMemoryStore) AddAgentLongTermMemoryWithSupersede(ctx context.Context, mem agentmem.LongTermMemory, terms []string) (int64, []int64, error) {
	scope := mem.Scope
	if scope == "" {
		scope = "global"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, nil, fmt.Errorf("begin agent memory tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO xbot_long_term_memories
			(user_id, tenant_id, type, content, keywords, tags, source_session, importance, search_text, scope)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id
	`, mem.UserID, mem.TenantID, mem.Type, mem.Content, mem.Keywords, mem.Tags,
		mem.SourceSession, mem.Importance, mem.SearchText, scope).Scan(&id); err != nil {
		return 0, nil, fmt.Errorf("insert agent long-term memory: %w", err)
	}

	var superseded []int64
	if scope == "global" && len(terms) > 0 {
		tsq := tsQuery(terms, agentmem.JoinAll)
		rows, qerr := tx.Query(ctx, `
			SELECT id FROM xbot_long_term_memories
			WHERE user_id = $1 AND scope = 'global' AND superseded_by IS NULL
			  AND id < $2 AND search_tsv @@ $3::tsquery
			  AND ts_rank(search_tsv, $3::tsquery) >= $4
		`, mem.UserID, id, tsq, dedupRankThreshold)
		if qerr == nil {
			var stale []int64
			for rows.Next() {
				var staleID int64
				if err := rows.Scan(&staleID); err == nil {
					stale = append(stale, staleID)
				}
			}
			rows.Close()
			for _, staleID := range stale {
				tag, uerr := tx.Exec(ctx, `
					UPDATE xbot_long_term_memories SET superseded_by = $1, updated_at = now()
					WHERE id = $2 AND superseded_by IS NULL`, id, staleID)
				if uerr != nil {
					continue
				}
				if tag.RowsAffected() > 0 {
					superseded = append(superseded, staleID)
				}
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, nil, err
	}
	return id, superseded, nil
}

// SetAgentLongTermFilePath records the Markdown mirror path.
func (s *agentMemoryStore) SetAgentLongTermFilePath(ctx context.Context, id int64, filePath string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE xbot_long_term_memories SET file_path = $1 WHERE id = $2`, filePath, id)
	return err
}
