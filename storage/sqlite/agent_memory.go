package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"xbot/storage/agentmem"
)

// AgentMemoryService implements AgentMemoryStore: the official xbot memory
// provider's storage seam. It owns the tables, the FTS5 index and every SQL
// statement; the provider owns memory policy and the shared tokenizer (it hands
// over pre-tokenized query terms).
//
// The types below live here (not in package storage) because storage imports
// storage/sqlite — the same convention as IterationRecord/HistoryRecordType.
//
// Isolation: rows are always scoped by user_id = the canonical owner (0 matches
// only ownerless rows). tenant_id is informational and never used for filtering —
// falling back to it scattered one user's memories across sessions.
type AgentMemoryService struct {
	conn *sql.DB
}

// NewAgentMemoryService creates the agent-memory service from the shared DB.
func NewAgentMemoryService(db *DB) *AgentMemoryService {
	return &AgentMemoryService{conn: db.Conn()}
}

// NewAgentMemoryServiceFromConn creates the agent-memory service from a raw
// connection (tests, and callers that only hold a *sql.DB).
func NewAgentMemoryServiceFromConn(conn *sql.DB) *AgentMemoryService {
	return &AgentMemoryService{conn: conn}
}

// agentMemorySchema creates the tables, the FTS5 index and its sync triggers.
// Idempotent (IF NOT EXISTS), so it doubles as the migration entry point.
const agentMemorySchema = `
CREATE TABLE IF NOT EXISTS xbot_short_term_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 0,
    tenant_id INTEGER NOT NULL DEFAULT 0,
    session_id TEXT NOT NULL,
    summary TEXT NOT NULL,
    key_topics TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    access_count INTEGER DEFAULT 0,
    heat_score REAL DEFAULT 1.0
);
CREATE INDEX IF NOT EXISTS idx_xbot_stm_user ON xbot_short_term_memories(user_id);
CREATE INDEX IF NOT EXISTS idx_xbot_stm_session ON xbot_short_term_memories(session_id);

CREATE VIRTUAL TABLE IF NOT EXISTS xbot_short_term_memories_fts USING fts5(
    summary, key_topics,
    content='xbot_short_term_memories', content_rowid='id',
    tokenize='unicode61'
);
CREATE TRIGGER IF NOT EXISTS xbot_stm_ai AFTER INSERT ON xbot_short_term_memories BEGIN
    INSERT INTO xbot_short_term_memories_fts(rowid, summary, key_topics)
    VALUES (new.id, new.summary, new.key_topics);
END;
CREATE TRIGGER IF NOT EXISTS xbot_stm_ad AFTER DELETE ON xbot_short_term_memories BEGIN
    INSERT INTO xbot_short_term_memories_fts(xbot_short_term_memories_fts, rowid, summary, key_topics)
    VALUES('delete', old.id, old.summary, old.key_topics);
END;
CREATE TRIGGER IF NOT EXISTS xbot_stm_au AFTER UPDATE ON xbot_short_term_memories BEGIN
    INSERT INTO xbot_short_term_memories_fts(xbot_short_term_memories_fts, rowid, summary, key_topics)
    VALUES('delete', old.id, old.summary, old.key_topics);
    INSERT INTO xbot_short_term_memories_fts(rowid, summary, key_topics)
    VALUES (new.id, new.summary, new.key_topics);
END;

CREATE TABLE IF NOT EXISTS xbot_long_term_memories (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL DEFAULT 0,
    tenant_id INTEGER NOT NULL DEFAULT 0,
    type TEXT NOT NULL,
    content TEXT NOT NULL,
    keywords TEXT,
    tags TEXT,
    source_session TEXT,
    importance REAL DEFAULT 0.5,
    heat_score REAL DEFAULT 1.0,
    access_count INTEGER DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    last_accessed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    file_path TEXT,
    search_text TEXT NOT NULL DEFAULT '',
    scope TEXT NOT NULL DEFAULT 'global',
    superseded_by INTEGER
);
CREATE INDEX IF NOT EXISTS idx_xbot_ltm_user ON xbot_long_term_memories(user_id);
CREATE INDEX IF NOT EXISTS idx_xbot_ltm_type ON xbot_long_term_memories(type);
CREATE INDEX IF NOT EXISTS idx_xbot_ltm_heat ON xbot_long_term_memories(heat_score);

CREATE VIRTUAL TABLE IF NOT EXISTS xbot_long_term_memories_fts USING fts5(
    content, keywords, tags, search_text,
    content='xbot_long_term_memories', content_rowid='id',
    tokenize='unicode61'
);
CREATE TRIGGER IF NOT EXISTS xbot_ltm_ai AFTER INSERT ON xbot_long_term_memories BEGIN
    INSERT INTO xbot_long_term_memories_fts(rowid, content, keywords, tags, search_text)
    VALUES (new.id, new.content, new.keywords, new.tags, new.search_text);
END;
CREATE TRIGGER IF NOT EXISTS xbot_ltm_ad AFTER DELETE ON xbot_long_term_memories BEGIN
    INSERT INTO xbot_long_term_memories_fts(xbot_long_term_memories_fts, rowid, content, keywords, tags, search_text)
    VALUES('delete', old.id, old.content, old.keywords, old.tags, old.search_text);
END;
CREATE TRIGGER IF NOT EXISTS xbot_ltm_au AFTER UPDATE ON xbot_long_term_memories BEGIN
    INSERT INTO xbot_long_term_memories_fts(xbot_long_term_memories_fts, rowid, content, keywords, tags, search_text)
    VALUES('delete', old.id, old.content, old.keywords, old.tags, old.search_text);
    INSERT INTO xbot_long_term_memories_fts(rowid, content, keywords, tags, search_text)
    VALUES (new.id, new.content, new.keywords, new.tags, new.search_text);
END;
`

// dedupBM25Threshold: FTS5 bm25() is negative and LOWER = more relevant, so a
// score below this means strong keyword overlap → treat as duplicate.
const dedupBM25Threshold = -6.0

// EnsureAgentMemorySchema creates the tables/indexes and adds columns that
// older databases lack. Idempotent; safe on every start.
func (s *AgentMemoryService) EnsureAgentMemorySchema(ctx context.Context) error {
	conn := s.conn
	if _, err := conn.ExecContext(ctx, agentMemorySchema); err != nil {
		return fmt.Errorf("create agent memory schema: %w", err)
	}
	// Columns added after the tables shipped (idempotent guards, since SQLite has
	// no ADD COLUMN IF NOT EXISTS).
	for _, table := range []string{"xbot_long_term_memories", "xbot_short_term_memories"} {
		ok, err := s.columnExists(ctx, conn, table, "user_id")
		if err != nil {
			return err
		}
		if !ok {
			if _, err := conn.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0"); err != nil {
				return fmt.Errorf("add user_id to %s: %w", table, err)
			}
		}
	}
	for _, col := range []struct{ name, ddl string }{
		{"scope", `ALTER TABLE xbot_long_term_memories ADD COLUMN scope TEXT NOT NULL DEFAULT 'global'`},
		{"superseded_by", "ALTER TABLE xbot_long_term_memories ADD COLUMN superseded_by INTEGER"},
		{"search_text", "ALTER TABLE xbot_long_term_memories ADD COLUMN search_text TEXT NOT NULL DEFAULT ''"},
	} {
		ok, err := s.columnExists(ctx, conn, "xbot_long_term_memories", col.name)
		if err != nil {
			return err
		}
		if !ok {
			if _, err := conn.ExecContext(ctx, col.ddl); err != nil {
				return fmt.Errorf("add %s: %w", col.name, err)
			}
		}
	}
	return nil
}

func (s *AgentMemoryService) columnExists(ctx context.Context, conn *sql.DB, table, column string) (bool, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return false, fmt.Errorf("pragma table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ── Short-term memories ──

func (s *AgentMemoryService) AddAgentShortTermMemory(ctx context.Context, mem agentmem.ShortTermMemory) (int64, error) {
	res, err := s.conn.ExecContext(ctx, `
		INSERT INTO xbot_short_term_memories (user_id, tenant_id, session_id, summary, key_topics)
		VALUES (?, ?, ?, ?, ?)
	`, mem.UserID, mem.TenantID, mem.SessionID, mem.Summary, mem.KeyTopics)
	if err != nil {
		return 0, fmt.Errorf("add agent short-term memory: %w", err)
	}
	return res.LastInsertId()
}

func (s *AgentMemoryService) SearchAgentShortTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.ShortTermMemory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	var query string
	var args []any
	if len(q.Terms) == 0 {
		query = `SELECT id, user_id, tenant_id, session_id, summary, key_topics,
		                created_at, heat_score, access_count
		         FROM xbot_short_term_memories WHERE user_id = ?
		         ORDER BY created_at DESC LIMIT ?`
		args = []any{q.UserID, limit}
	} else {
		query = `SELECT stm.id, stm.user_id, stm.tenant_id, stm.session_id, stm.summary, stm.key_topics,
		                stm.created_at, stm.heat_score, stm.access_count
		         FROM xbot_short_term_memories_fts fts
		         JOIN xbot_short_term_memories stm ON stm.id = fts.rowid
		         WHERE stm.user_id = ? AND xbot_short_term_memories_fts MATCH ?
		         ORDER BY bm25(xbot_short_term_memories_fts) ASC LIMIT ?`
		args = []any{q.UserID, ftsQuery(q.Terms, q.Join), limit}
	}
	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search agent short-term memories: %w", err)
	}
	defer rows.Close()
	var out []agentmem.ShortTermMemory
	for rows.Next() {
		var m agentmem.ShortTermMemory
		if err := rows.Scan(&m.ID, &m.UserID, &m.TenantID, &m.SessionID, &m.Summary, &m.KeyTopics,
			&m.CreatedAt, &m.HeatScore, &m.AccessCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *AgentMemoryService) EvictAgentShortTermMemories(ctx context.Context, userID, tenantID int64, keep int) error {
	if keep <= 0 {
		keep = 5
	}
	_, err := s.conn.ExecContext(ctx, `
		DELETE FROM xbot_short_term_memories
		WHERE id NOT IN (
			SELECT id FROM xbot_short_term_memories WHERE user_id = ?
			ORDER BY created_at DESC LIMIT ?
		) AND user_id = ?
	`, userID, keep, userID)
	if err != nil {
		return fmt.Errorf("evict agent short-term memories: %w", err)
	}
	return nil
}

// ── Long-term memories ──

// agentLongTermColumns is the projection for both read paths. Nullable columns are
// COALESCEd: rows written before file_path/search_text existed hold NULL, and
// scanning NULL into a plain string fails.
const agentLongTermColumns = `id, user_id, tenant_id, type, content, keywords, tags, source_session,
	importance, heat_score, access_count, created_at, last_accessed_at,
	COALESCE(file_path, ''), COALESCE(search_text, ''), COALESCE(scope, 'global'),
	COALESCE(superseded_by, 0)`

func (s *AgentMemoryService) AddAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) (int64, error) {
	scope := mem.Scope
	if scope == "" {
		scope = "global"
	}
	res, err := s.conn.ExecContext(ctx, `
		INSERT INTO xbot_long_term_memories
			(user_id, tenant_id, type, content, keywords, tags, source_session, importance, search_text, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, mem.UserID, mem.TenantID, mem.Type, mem.Content, mem.Keywords, mem.Tags,
		mem.SourceSession, mem.Importance, mem.SearchText, scope)
	if err != nil {
		return 0, fmt.Errorf("add agent long-term memory: %w", err)
	}
	return res.LastInsertId()
}

// agentOrderSQL maps the neutral ordering onto SQLite. Relevance is bm25 ASC
// (lower = better) — the direction stays inside the backend.
func agentOrderSQL(order string, fts bool) string {
	switch order {
	case "relevance":
		if fts {
			return "bm25(xbot_long_term_memories_fts) ASC"
		}
		return "importance DESC, created_at DESC"
	case "recency":
		return "created_at DESC"
	default: // importance
		return "importance DESC, heat_score DESC, created_at DESC"
	}
}

func (s *AgentMemoryService) SearchAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 5
	}
	where := []string{"ltm.user_id = ?"}
	args := []any{q.UserID}
	if !q.IncludeSuperseded {
		where = append(where, "ltm.superseded_by IS NULL")
	}
	if q.Type != "" {
		where = append(where, "ltm.type = ?")
		args = append(args, q.Type)
	}
	if q.Scope != "" {
		where = append(where, "ltm.scope = ?")
		args = append(args, q.Scope)
	}
	if q.SourceSession != "" {
		where = append(where, "ltm.source_session = ?")
		args = append(args, q.SourceSession)
	}

	var query string
	fts := len(q.Terms) > 0
	if fts {
		where = append(where, "xbot_long_term_memories_fts MATCH ?")
		args = append(args, ftsQuery(q.Terms, q.Join))
		query = `SELECT ` + prefixedColumns("ltm") + `, bm25(xbot_long_term_memories_fts) AS relevance
		         FROM xbot_long_term_memories_fts fts
		         JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
		         WHERE ` + strings.Join(where, " AND ") + `
		         ORDER BY ` + agentOrderSQL(string(q.Order), true) + ` LIMIT ?`
	} else {
		query = `SELECT ` + prefixedColumns("ltm") + `, 0 AS relevance
		         FROM xbot_long_term_memories ltm
		         WHERE ` + strings.Join(where, " AND ") + `
		         ORDER BY ` + agentOrderSQL(string(q.Order), false) + ` LIMIT ?`
	}
	args = append(args, limit)

	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search agent long-term memories: %w", err)
	}
	defer rows.Close()
	return scanAgentLongTerm(rows)
}

func prefixedColumns(alias string) string {
	cols := []string{"id", "user_id", "tenant_id", "type", "content", "keywords", "tags", "source_session",
		"importance", "heat_score", "access_count", "created_at", "last_accessed_at"}
	for i, c := range cols {
		cols[i] = alias + "." + c
	}
	// Expressions must NOT be alias-prefixed (ltm.COALESCE(...) is invalid SQL),
	// and nullable columns are COALESCEd so they scan into plain strings.
	cols = append(cols,
		"COALESCE("+alias+".file_path, '')",
		"COALESCE("+alias+".search_text, '')",
		"COALESCE("+alias+".scope, 'global')",
		"COALESCE("+alias+".superseded_by, 0)",
	)
	return strings.Join(cols, ", ")
}

func scanAgentLongTerm(rows *sql.Rows) ([]agentmem.LongTermMemory, error) {
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

func (s *AgentMemoryService) FindAgentDuplicateLongTerm(ctx context.Context, q agentmem.Query) (int64, error) {
	if len(q.Terms) == 0 {
		return 0, nil
	}
	// Dedup scope: a session-scoped extraction only competes with global rows and
	// its OWN session's rows (otherwise session A's fact suppresses session B's).
	where := []string{"ltm.user_id = ?", "ltm.superseded_by IS NULL",
		"(ltm.scope = 'global' OR (ltm.scope = 'session' AND ltm.source_session = ?))",
		"xbot_long_term_memories_fts MATCH ?", "bm25(xbot_long_term_memories_fts) < ?"}
	var id int64
	err := s.conn.QueryRowContext(ctx, `
		SELECT ltm.id FROM xbot_long_term_memories_fts fts
		JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY bm25(xbot_long_term_memories_fts) ASC LIMIT 1
	`, q.UserID, q.SourceSession, ftsQuery(q.Terms, agentmem.JoinAll), dedupBM25Threshold).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("find agent duplicate long-term memory: %w", err)
	}
	return id, nil
}

func (s *AgentMemoryService) SupersedeAgentLongTerm(ctx context.Context, q agentmem.Query, newID int64) ([]int64, error) {
	if len(q.Terms) == 0 {
		return nil, nil
	}
	conn := s.conn
	rows, err := conn.QueryContext(ctx, `
		SELECT ltm.id FROM xbot_long_term_memories_fts fts
		JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
		WHERE ltm.user_id = ? AND ltm.scope = 'global' AND ltm.superseded_by IS NULL
		  AND ltm.id < ?
		  AND xbot_long_term_memories_fts MATCH ?
		  AND bm25(xbot_long_term_memories_fts) < ?
	`, q.UserID, newID, ftsQuery(q.Terms, agentmem.JoinAll), dedupBM25Threshold)
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
		res, err := conn.ExecContext(ctx, `
			UPDATE xbot_long_term_memories SET superseded_by = ?, updated_at = CURRENT_TIMESTAMP
			WHERE id = ? AND superseded_by IS NULL
		`, newID, id)
		if err != nil {
			return superseded, fmt.Errorf("supersede agent long-term memory %d: %w", id, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			superseded = append(superseded, id)
		}
	}
	return superseded, nil
}

func (s *AgentMemoryService) ListAgentLongTermMemories(ctx context.Context, q agentmem.Query) ([]agentmem.LongTermMemory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	where := []string{"user_id = ?"}
	args := []any{q.UserID}
	if !q.IncludeSuperseded {
		where = append(where, "superseded_by IS NULL")
	}
	if q.Type != "" {
		where = append(where, "type = ?")
		args = append(args, q.Type)
	}
	query := `SELECT ` + agentLongTermColumns + `, 0 AS relevance FROM xbot_long_term_memories
	          WHERE ` + strings.Join(where, " AND ") + `
	          ORDER BY ` + agentOrderSQL(string(q.Order), false) + ` LIMIT ?`
	args = append(args, limit)
	rows, err := s.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list agent long-term memories: %w", err)
	}
	defer rows.Close()
	return scanAgentLongTerm(rows)
}

func (s *AgentMemoryService) GetAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) (agentmem.LongTermMemory, error) {
	var m agentmem.LongTermMemory
	err := s.conn.QueryRowContext(ctx, `
		SELECT `+agentLongTermColumns+`, 0 AS relevance FROM xbot_long_term_memories
		WHERE id = ? AND user_id = ?
	`, id, userID).Scan(&m.ID, &m.UserID, &m.TenantID, &m.Type, &m.Content, &m.Keywords, &m.Tags,
		&m.SourceSession, &m.Importance, &m.HeatScore, &m.AccessCount, &m.CreatedAt, &m.LastAccessedAt,
		&m.FilePath, &m.SearchText, &m.Scope, &m.SupersededBy, &m.Relevance)
	if err != nil {
		return agentmem.LongTermMemory{}, fmt.Errorf("get agent long-term memory %d: %w", id, err)
	}
	return m, nil
}

func (s *AgentMemoryService) UpdateAgentLongTermMemory(ctx context.Context, mem agentmem.LongTermMemory) error {
	_, err := s.conn.ExecContext(ctx, `
		UPDATE xbot_long_term_memories
		SET content = ?, keywords = ?, tags = ?, importance = ?, search_text = ?,
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND user_id = ?
	`, mem.Content, mem.Keywords, mem.Tags, mem.Importance, mem.SearchText, mem.ID, mem.UserID)
	if err != nil {
		return fmt.Errorf("update agent long-term memory %d: %w", mem.ID, err)
	}
	return nil
}

func (s *AgentMemoryService) DeleteAgentLongTermMemory(ctx context.Context, userID, tenantID, id int64) error {
	_, err := s.conn.ExecContext(ctx,
		`DELETE FROM xbot_long_term_memories WHERE id = ? AND user_id = ?`, id, userID)
	if err != nil {
		return fmt.Errorf("delete agent long-term memory %d: %w", id, err)
	}
	return nil
}

func (s *AgentMemoryService) TouchAgentLongTermMemory(ctx context.Context, id int64) error {
	_, err := s.conn.ExecContext(ctx, `
		UPDATE xbot_long_term_memories
		SET access_count = access_count + 1, last_accessed_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)
	return err
}

func (s *AgentMemoryService) DecayAgentLongTerm(ctx context.Context, userID, tenantID int64, halfLifeDays, forgetThreshold float64) error {
	conn := s.conn
	if _, err := conn.ExecContext(ctx, `
		UPDATE xbot_long_term_memories
		SET heat_score = importance *
		    exp(-(julianday('now') - julianday(last_accessed_at)) / ?) *
		    log(1 + access_count)
		WHERE user_id = ?
	`, halfLifeDays, userID); err != nil {
		return fmt.Errorf("decay agent long-term heat: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `
		UPDATE xbot_long_term_memories SET importance = importance * 0.95
		WHERE heat_score < ? AND user_id = ?
	`, forgetThreshold, userID); err != nil {
		return fmt.Errorf("decay agent long-term importance: %w", err)
	}
	return nil
}

func (s *AgentMemoryService) PruneAgentLongTerm(ctx context.Context, userID, tenantID int64, maxEntries int) error {
	if maxEntries <= 0 {
		return nil
	}
	_, err := s.conn.ExecContext(ctx, `
		DELETE FROM xbot_long_term_memories
		WHERE user_id = ? AND id NOT IN (
			SELECT id FROM xbot_long_term_memories WHERE user_id = ?
			ORDER BY heat_score DESC, importance DESC LIMIT ?
		)
	`, userID, userID, maxEntries)
	if err != nil {
		return fmt.Errorf("prune agent long-term memories: %w", err)
	}
	return nil
}

func (s *AgentMemoryService) ListAgentLongTermMissingSearchText(ctx context.Context, userID, tenantID int64, limit int) ([]agentmem.LongTermMemory, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.conn.QueryContext(ctx, `
		SELECT `+agentLongTermColumns+`, 0 AS relevance FROM xbot_long_term_memories
		WHERE user_id = ? AND (search_text = '' OR search_text IS NULL) LIMIT ?
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list agent memories missing search_text: %w", err)
	}
	defer rows.Close()
	return scanAgentLongTerm(rows)
}

func (s *AgentMemoryService) SetAgentLongTermSearchText(ctx context.Context, id int64, searchText string) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE xbot_long_term_memories SET search_text = ? WHERE id = ?`, searchText, id)
	return err
}

// ftsQuery renders pre-tokenized terms as an FTS5 MATCH expression. Terms are
// already CJK-split and stopword-filtered by the provider; quoting keeps every
// token literal (no operator interpretation).
func ftsQuery(terms []string, join agentmem.TermJoin) string {
	if len(terms) == 0 {
		return `""`
	}
	quoted := make([]string, 0, len(terms))
	for _, t := range terms {
		quoted = append(quoted, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	if join == agentmem.JoinAll {
		return strings.Join(quoted, " AND ")
	}
	return strings.Join(quoted, " OR ")
}

// AddAgentLongTermMemoryWithSupersede inserts the memory and supersedes older
// strongly-matching GLOBAL entries in ONE transaction (crash consistency: a
// partial write would leave contradictory facts both active and both injected).
func (s *AgentMemoryService) AddAgentLongTermMemoryWithSupersede(ctx context.Context, mem agentmem.LongTermMemory, terms []string) (int64, []int64, error) {
	scope := mem.Scope
	if scope == "" {
		scope = "global"
	}
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("begin agent memory tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `
		INSERT INTO xbot_long_term_memories
			(user_id, tenant_id, type, content, keywords, tags, source_session, importance, search_text, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, mem.UserID, mem.TenantID, mem.Type, mem.Content, mem.Keywords, mem.Tags,
		mem.SourceSession, mem.Importance, mem.SearchText, scope)
	if err != nil {
		return 0, nil, fmt.Errorf("insert agent long-term memory: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, nil, err
	}

	var superseded []int64
	if scope == "global" && len(terms) > 0 {
		rows, qerr := tx.QueryContext(ctx, `
			SELECT ltm.id FROM xbot_long_term_memories_fts fts
			JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
			WHERE ltm.user_id = ? AND ltm.scope = 'global' AND ltm.superseded_by IS NULL
			  AND ltm.id < ?
			  AND xbot_long_term_memories_fts MATCH ?
			  AND bm25(xbot_long_term_memories_fts) < ?
		`, mem.UserID, id, ftsQuery(terms, agentmem.JoinAll), dedupBM25Threshold)
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
				// superseded_by IS NULL guard: two concurrent writers must not
				// overwrite an already-superseded row.
				r, uerr := tx.ExecContext(ctx, `
					UPDATE xbot_long_term_memories
					SET superseded_by = ?, updated_at = CURRENT_TIMESTAMP
					WHERE id = ? AND superseded_by IS NULL`, id, staleID)
				if uerr != nil {
					continue
				}
				if n, _ := r.RowsAffected(); n > 0 {
					superseded = append(superseded, staleID)
				}
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return id, superseded, nil
}

// SetAgentLongTermFilePath records the Markdown mirror path.
func (s *AgentMemoryService) SetAgentLongTermFilePath(ctx context.Context, id int64, filePath string) error {
	_, err := s.conn.ExecContext(ctx,
		`UPDATE xbot_long_term_memories SET file_path = ? WHERE id = ?`, filePath, id)
	return err
}
