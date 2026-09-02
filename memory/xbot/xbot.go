// Package xbot implements a SQLite FTS5-based memory provider with three-tier
// memory (working/short-term/long-term), BM25 retrieval, heat decay, and
// compression awareness. No external embedding dependency required.
//
// Design inspired by:
//   - Mem0 (ECAI 2025): LLM-driven write path, lightweight read path
//   - A-MEM (NeurIPS 2025): Atomic memory notes with keywords/tags
//   - MemoryOS (EMNLP 2025): Three-tier memory with heat decay
//   - MemoryBank (AAAI 2024): Ebbinghaus forgetting curve
//   - Memweave (2025): Markdown files as source of truth, SQLite as derived index
package xbot

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/memory"
	"xbot/prompt"
)

const (
	// coreSummaryFileName is the core summary file injected into system prompt.
	coreSummaryFileName = "MEMORY.md"
	// coreSummaryMaxChars limits the core summary size.
	coreSummaryMaxChars = 2000
	// recallMaxRunes is the hard cap on TOTAL injected memory content per
	// Recall call (core summary + short-term + long-term). Without it, a
	// growing MEMORY.md + many memories can inject several thousand runes of
	// mostly-unrelated context every turn, diluting attention.
	recallMaxRunes = 3000
	// defaultRecallTopK is the default number of memories to retrieve.
	defaultRecallTopK = 5
	// defaultShortTermCapacity is the max number of short-term memories kept.
	defaultShortTermCapacity = 5
	// heatHalfLifeDays controls memory heat decay (Ebbinghaus-inspired).
	heatHalfLifeDays = 30.0
	// forgetThreshold is the heat score below which memory importance decays.
	forgetThreshold = 0.1
	// consolidateInterval is the minimum time between two LLM consolidations
	// per provider instance (10 min per session). The old message-count
	// throttle ran 3 LLM calls per turn — this caps extraction rate.
	consolidateInterval = 10 * time.Minute
	// longTermMaxEntries is the per-user cap on long-term memories. Beyond it,
	// the lowest-heat entries are pruned (memory bloat control).
	longTermMaxEntries = 300
	// dedupSimilarityThreshold: BM25 similarity below which a candidate is
	// considered a duplicate. SQLite bm25() returns negative values (LOWER =
	// more relevant — same ordering as ORDER BY score ASC in searchLongTerm).
	// A score below (more negative than) -6 ≈ strong keyword overlap.
	dedupSimilarityThreshold = -6.0
)

// schemaSQL creates all xbot memory tables. Uses IF NOT EXISTS for idempotency.
// All table names use xbot_ prefix to avoid conflicts with letta tables.
//
// Memory isolation is by user_id (canonical owner), NOT tenant_id:
// the same user must see the same memories across ALL their sessions/tenants.
// tenant_id is kept as an informational column (source tenant) but all
// queries filter on user_id.
const schemaSQL = `
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
    search_text TEXT NOT NULL DEFAULT '',  -- CJK 逐字空格切分后的检索文本
    scope TEXT NOT NULL DEFAULT 'global',  -- 'global' = 注入所有会话；'session' = 自动提取的会话内记忆（永不跨会话注入）
    superseded_by INTEGER                  -- 被替代条目的 ID（新事实取代旧事实，如集群搬迁；NULL = 活跃）
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

// XbotMemory is the xbot memory provider with three-tier memory and BM25 retrieval.
// It implements memory.MemoryProvider and memory.CompressionAware.
//
// Isolation model: memories are scoped by userID (canonical owner), NOT tenantID.
// The same user sees the same memories across ALL sessions/tenants — this is
// the definition of "cross-session memory". tenantID is retained as an
// informational column (source tenant) but never used for filtering.
//
// LLM CLIENT OWNERSHIP: this provider holds NO llmClient/model fields. The
// single-operator deployment shares ONE XbotMemory instance across ALL sessions,
// and a mutable client field was a cross-session race (2026-09-02 incident:
// chat_BD94FA4BB469's PostCompress core-summary update landed on a different
// session's model/endpoint after concurrent ConsolidateTurns overwrote the
// shared field — F64D's extraction ran with feishu's deepseek config and vice
// versa, a perfect swap). Every LLM-using operation (Memorize/ConsolidateTurn/
// PreCompress/PostCompress) receives its client+model EXPLICITLY in its input
// and threads them down to generateLLM — no shared mutable state.
type XbotMemory struct {
	userID   int64  // canonical owner user_id (isolation scope)
	tenantID int64  // source tenant (informational, not used for filtering)
	baseDir  string // ~/.xbot/memory/{tenantID}/
	db       *sql.DB
	mu       sync.RWMutex

	// Time-based consolidation throttle (guarded by mu).
	// At most ONE LLM extraction per consolidateInterval per provider instance.
	// The watermark (LastConsolidated) advances every turn regardless, so no
	// messages are lost on restart.
	lastConsolidateAt time.Time
}

var _ memory.MemoryProvider = (*XbotMemory)(nil)
var _ memory.CompressionAware = (*XbotMemory)(nil)
var _ memory.TurnConsolidator = (*XbotMemory)(nil)

// Name returns the provider's unique identifier.
func (m *XbotMemory) Name() string { return "xbot" }

func init() {
	memory.RegisterProviderFactory("xbot", func(deps memory.ProviderDeps) memory.MemoryProvider {
		return New(deps.UserID, deps.TenantID, deps.BaseDir, deps.DB)
	})
	memory.RegisterPromptParts("xbot", memory.PromptParts{
		ToolsPrompt:  prompt.ToolsXbot,
		MemoryPrompt: prompt.MemoryXbot,
		UserGuide:    prompt.UserMessageGuideXbot,
	})
}

// New creates a XbotMemory instance.
// userID is the canonical owner — memories are scoped and shared by this ID
// across all sessions.
//
// IMPORTANT: if userID is 0 (the caller used GetOrCreateSession without an
// owner — e.g. buildToolContextExtras, async ConsolidateTurn, feishu channels),
// we LOOK UP the real owner from the tenants table. Falling back to tenant_id
// would write user_id=tenant_id and re-split memories per session — the exact
// bug that made different sessions see different memories.
// db is the shared SQLite connection (reuses xbot's existing DB).
// baseDir is the per-tenant memory directory for Markdown files.
func New(userID, tenantID int64, baseDir string, db *sql.DB) *XbotMemory {
	os.MkdirAll(baseDir, 0o755)
	os.MkdirAll(filepath.Join(baseDir, "notes"), 0o755)

	// Resolve the canonical owner from the tenants table when not provided.
	// This is authoritative: tenants.owner_user_id is set by GetOrCreateTenantIDWithOwner
	// and links every session of the same user to the same owner id.
	if userID <= 0 && tenantID > 0 && db != nil {
		var owner int64
		err := db.QueryRow(
			`SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE id = ?`,
			tenantID,
		).Scan(&owner)
		if err == nil && owner > 0 {
			userID = owner
		}
	}

	m := &XbotMemory{
		userID:   userID,
		tenantID: tenantID,
		baseDir:  baseDir,
		db:       db,
	}
	m.initSchema()
	m.migrateLegacyTenantData()
	return m
}

// scopeWhere returns the WHERE clause fragment for memory isolation.
// user_id > 0 → user-scoped (cross-session shared).
// user_id == 0 → no owner resolved; matches ONLY user_id=0 rows (isolated).
// NEVER falls back to tenant_id — writing user_id=tenant_id was the root cause
// of "different sessions see different memories" (each session had its own
// tenant_id → memories re-split per session).
func (m *XbotMemory) scopeWhere(alias string) string {
	if alias != "" {
		return alias + ".user_id = ?"
	}
	return "user_id = ?"
}

// scopeArg returns the argument for the scope WHERE clause.
// If userID is 0, returns 0 — matching only ownerless (user_id=0) rows.
// Never returns tenantID: that would scatter memories across tenants.
// Must hold RLock: SetOwnerUserID writes m.userID under Lock from a different
// goroutine (auto-memorize ConsolidateTurn runs concurrently with processMessage).
func (m *XbotMemory) scopeArg() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.userID
}

// generateLLM issues a memory-pipeline LLM call (session summaries, atomic
// memory extraction, core-summary updates) through the STREAMING path whenever
// the client supports it, falling back to non-stream Generate otherwise.
//
// The client+model are EXPLICIT parameters (threaded from the operation's
// input — Memorize/ConsolidateTurn/PreCompress/PostCompress), NEVER shared
// struct state: one XbotMemory instance serves all sessions (single operator),
// and a shared mutable field was raced by concurrent memory operations
// (2026-09-02 incident — see the struct comment).
//
// Root cause this guards (web:chat_BD94FA4BB469 turn 367, 2026-08-30): the
// memory LLM calls used llmClient.Generate — the non-stream retry path whose
// perAttemptCtx carries a HARD 120s deadline (llm/retry.go). On a busy cluster
// the PostCompress updateCoreSummary call was killed at exactly 120s, then
// retried 5×2 minutes, burning ~10 minutes AFTER a successful compression
// while the turn sat "stuck". The streaming path has NO total deadline — only
// the 120s IDLE timeout between SSE chunks, reset on EVERY chunk
// (CollectStreamWithCallback) — an actively-streaming response never times
// out. Same semantics the compression LLM call itself now uses
// (agent/compress.go Stream:true).
//
// RetryLLM gets the full-cycle GenerateStreamAndCollect (connection + event
// collection retried as a whole); plain StreamingLLM gets GenerateStream +
// CollectStream; clients that implement neither (test mocks) fall back to
// Generate.
func (m *XbotMemory) generateLLM(ctx context.Context, client llm.LLM, model string, messages []llm.ChatMessage, tools []llm.ToolDefinition) (*llm.LLMResponse, error) {
	if rl, ok := client.(*llm.RetryLLM); ok {
		return rl.GenerateStreamAndCollect(ctx, model, messages, tools, "", nil, nil, nil, nil)
	}
	if sc, ok := client.(llm.StreamingLLM); ok {
		eventCh, err := sc.GenerateStream(ctx, model, messages, tools, "")
		if err != nil {
			return nil, err
		}
		return llm.CollectStream(ctx, eventCh)
	}
	return client.Generate(ctx, model, messages, tools, "")
}

// SetOwnerUserID sets the canonical owner user_id at runtime.
//
// The XbotMemory is created per-tenant by both the agent loop (which knows the
// owner via ResolveUserContext) and the tool-extras path (which does NOT —
// it calls GetOrCreateSession without an owner). Both share the same cached
// TenantSession, so the provider may be constructed with userID=0 first.
// processMessage calls this AFTER resolving the owner so every subsequent
// query is correctly scoped cross-session.
func (m *XbotMemory) SetOwnerUserID(userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if userID > 0 {
		m.userID = userID
	}
}

func (m *XbotMemory) initSchema() {
	_, err := m.db.Exec(schemaSQL)
	if err != nil {
		log.WithError(err).Error("xbot-memory: failed to initialize schema")
	}
}

// migrateLegacyTenantData ensures the user_id column exists and backfills it
// from tenants.owner_user_id for rows created before the user-scoped isolation
// model. This makes existing tenant-scoped memories visible to their owner
// across all sessions.
//
// CRITICAL: the ALTER TABLE part MUST run regardless of m.userID — the column
// is required by all queries (scopeWhere emits user_id when userID>0). The old
// code gated the whole function on userID>0, so when the provider was created
// with userID=0 (multitenant pre-owner path), the column was never added and
// every later query crashed with "no column named user_id".
//
// SQLite does not support ALTER TABLE ADD COLUMN IF NOT EXISTS, so we check
// pragma table_info first and only ALTER when the column is missing.
func (m *XbotMemory) migrateLegacyTenantData() {
	// Step 1: ensure user_id column exists on both tables — ALWAYS runs.
	for _, table := range []string{"xbot_long_term_memories", "xbot_short_term_memories"} {
		var hasUserID bool
		rows, err := m.db.Query("PRAGMA table_info(" + table + ")")
		if err == nil {
			for rows.Next() {
				var cid int
				var name, typ string
				var notNull, pk int
				var dflt any
				if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err == nil && name == "user_id" {
					hasUserID = true
				}
			}
			rows.Close()
		}
		if !hasUserID {
			if _, err := m.db.Exec("ALTER TABLE " + table + " ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0"); err != nil {
				log.WithError(err).WithField("table", table).Warn("xbot-memory: failed to add user_id column")
			} else {
				log.WithField("table", table).Info("xbot-memory: added user_id column")
			}
		}
	}

	// Step 1b: ensure scope + superseded_by columns exist (session isolation,
	// 2026-09-02 redesign). scope DEFAULT 'global' keeps existing rows'
	// behavior unchanged — they were user-scoped global memories. superseded_by
	// is the supersede-chain link (new facts replace stale ones instead of
	// double-storing both). Same idempotent columnExists pattern as Step 1.
	for _, col := range []struct{ name, ddl string }{
		{"scope", `ALTER TABLE xbot_long_term_memories ADD COLUMN scope TEXT NOT NULL DEFAULT 'global'`},
		{"superseded_by", "ALTER TABLE xbot_long_term_memories ADD COLUMN superseded_by INTEGER"},
	} {
		var hasCol bool
		rows, err := m.db.Query("PRAGMA table_info(xbot_long_term_memories)")
		if err == nil {
			for rows.Next() {
				var cid int
				var name, typ string
				var notNull, pk int
				var dflt any
				if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err == nil && name == col.name {
					hasCol = true
				}
			}
			rows.Close()
		}
		if !hasCol {
			if _, err := m.db.Exec(col.ddl); err != nil {
				log.WithError(err).WithField("column", col.name).Warn("xbot-memory: failed to add column")
			} else {
				log.WithField("column", col.name).Info("xbot-memory: added column")
			}
		}
	}

	// Step 2: backfill user_id from tenants.owner_user_id (runs once per owner).
	// The UPDATE is idempotent: user_id=0 rows get filled; already-filled rows
	// are untouched. This is safe to run every startup.
	m.db.Exec(`
		UPDATE xbot_long_term_memories
		SET user_id = (SELECT owner_user_id FROM tenants WHERE tenants.id = xbot_long_term_memories.tenant_id)
		WHERE user_id = 0 AND tenant_id IN (SELECT id FROM tenants WHERE owner_user_id IS NOT NULL)
	`)
	m.db.Exec(`
		UPDATE xbot_short_term_memories
		SET user_id = (SELECT owner_user_id FROM tenants WHERE tenants.id = xbot_short_term_memories.tenant_id)
		WHERE user_id = 0 AND tenant_id IN (SELECT id FROM tenants WHERE owner_user_id IS NOT NULL)
	`)

	// Step 3: ensure search_text column exists (CJK-aware FTS indexing).
	// Added for Chinese retrieval support. Existing rows are backfilled with
	// buildSearchText(content, keywords, tags) — the same transform applied on
	// new writes, so the FTS index and query transform stay symmetric.
	var hasSearchText bool
	rows, err := m.db.Query("PRAGMA table_info(xbot_long_term_memories)")
	if err == nil {
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull, pk int
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pk); err == nil && name == "search_text" {
				hasSearchText = true
			}
		}
		rows.Close()
	}
	if !hasSearchText {
		if _, err := m.db.Exec("ALTER TABLE xbot_long_term_memories ADD COLUMN search_text TEXT NOT NULL DEFAULT ''"); err != nil {
			log.WithError(err).Warn("xbot-memory: failed to add search_text column")
		} else {
			log.Info("xbot-memory: added search_text column")
		}
	}

	// Backfill search_text for rows where it's empty. Idempotent: only fills
	// empty values, safe every startup. Uses a Go-side backfill because the
	// CJK spacing transform is a Go function (buildSearchText), not expressible
	// in pure SQL.
	m.backfillSearchText()
}

// backfillSearchText fills search_text for rows that have an empty value.
// Only the current provider's scope (user_id or tenant_id) is touched.
func (m *XbotMemory) backfillSearchText() {
	rows, err := m.db.Query(`
		SELECT id, content, keywords, tags
		FROM xbot_long_term_memories
		WHERE `+m.scopeWhere("")+` AND (search_text = '' OR search_text IS NULL)
		LIMIT 500
	`, m.scopeArg())
	if err != nil {
		log.WithError(err).Debug("xbot-memory: search_text backfill query failed")
		return
	}
	defer rows.Close()

	type row struct {
		id       int64
		content  string
		keywords string
		tags     string
	}
	var pending []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.content, &r.keywords, &r.tags); err == nil {
			pending = append(pending, r)
		}
	}
	for _, r := range pending {
		m.db.Exec(
			`UPDATE xbot_long_term_memories SET search_text = ? WHERE id = ?`,
			buildSearchText(r.content, r.keywords, r.tags), r.id,
		)
	}
	if len(pending) > 0 {
		log.WithField("backfilled", len(pending)).Info("xbot-memory: backfilled search_text")
	}
}

// --- MemoryProvider interface ---

// Recall retrieves relevant memories for the current conversation.
// Uses BM25 keyword search (SQLite FTS5) — zero LLM calls.
//
// Session isolation (2026-09-02 redesign): ONLY global-scope, non-superseded
// long-term memories + the core summary are injected. The OLD "## Recent
// Sessions" section (query-anchored searchShortTerm) injected OTHER sessions'
// short-term summaries into unrelated conversations (live evidence: a GPU
// tuning session's [Compacted context] summary appeared in a design
// conversation) — cross-session recall now goes through memory_search on
// demand, never auto-injection. Total injected content is capped at
// recallMaxRunes; every injected memory carries its created date so the model
// can judge staleness itself.
func (m *XbotMemory) Recall(ctx context.Context, query string) (string, error) {
	var sb strings.Builder
	var err error
	sb.WriteString("# Memory\n\n")

	// 1. Core summary (MEMORY.md, ≤2000 chars)
	coreSummary := m.readCoreSummary()
	if coreSummary != "" {
		sb.WriteString("## Core\n")
		sb.WriteString(coreSummary)
		sb.WriteString("\n\n")
	}

	// 2. Long-term memories (BM25) — global scope, non-superseded only.
	// Session-scoped memories (auto-extracted task state from other sessions)
	// never leak into this session's injection; superseded entries (replaced by
	// newer contradicting facts) are filtered at the query level.
	var longTermMems []LongTermMemory
	longTermMems, err = m.searchLongTerm(query, defaultRecallTopK)
	if err != nil {
		log.WithError(err).Debug("xbot-memory: long-term search failed")
	}
	if len(longTermMems) > 0 {
		sb.WriteString("## Long-term Memories\n")
		for _, mem := range longTermMems {
			// created date on every line: the model is the final staleness
			// judge ("cluster moved from X" last month beats "cluster at X" last year)
			fmt.Fprintf(&sb, "- [%s] %s (created %s)\n", mem.Type, mem.Content, mem.CreatedAt.Format("2006-01-02"))
		}
		sb.WriteString("\n")
	}

	// 3. Tool hint
	sb.WriteString("Use `memory_search` to find more memories, `memory_add` to save new ones.\n")

	// Enforce a hard cap on total injected runes (attention budget). The core
	// summary and most relevant memories stay; the tail is cut with a marker.
	injected := sb.String()
	if n := len([]rune(injected)); n > recallMaxRunes {
		runes := []rune(injected)
		injected = string(runes[:recallMaxRunes]) + "\n...(memory truncated to budget)"
	}

	// Injectable content length (excluding the static header + tool hint) so
	// operators can confirm memory injection actually fired per turn.
	injectedRunes := len([]rune(injected))
	if injectedRunes > 0 {
		log.WithFields(log.Fields{
			"query":          truncateForLog(query, 120),
			"long_term":      len(longTermMems),
			"has_core":       coreSummary != "",
			"injected_chars": injectedRunes,
		}).Info("xbot-memory: Recall injected memories into prompt")
	}

	return injected, nil
}

// truncateForLog truncates a string for log output, adding an ellipsis.
// Rune-safe: never splits a UTF-8 multibyte char (Chinese would break).
func truncateForLog(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// truncateRunes truncates a string to at most max runes, appending "...".
// Rune-safe: a UTF-8 Chinese char is never split mid-sequence.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "..."
}

// fts5SafeQuery converts a raw user query into a SQLite FTS5 MATCH expression
// that cannot throw a syntax error on special characters.
//
// FTS5 MATCH grammar treats chars like `"` `'` `*` `:` `(` `)` `{` `}` `-`
// `+` `.` `/` as operators/punctuation — a raw query containing them raises
// "fts5: syntax error near ...". The safe approach: split on whitespace and
// wrap each token in double quotes (FTS5 string literal — everything inside
// is literal, no operator interpretation), joining with implicit AND.
//
// Chinese: unicode61 tokenizer treats a run of CJK characters as ONE token
// (no whitespace between them), so a query like "记忆" can't match content
// "记忆系统" — different single tokens. We space-separate CJK runs (each char
// becomes its own token) in BOTH the query and the indexed search_text column,
// so "记忆系统" → "记 忆 系 统" and "记忆" → "记 忆" overlap on "记" + "忆".
// For a 2-char query this is a substring match; longer queries still require
// every CJK char to appear (implicit AND), which is strict but predictable.
//
// Example:
//
//	`foo 记忆 bar`  →  `"foo" AND "记 忆" AND "bar"`
//
// Quotes inside a token are escaped by doubling (FTS5 string literal escape).
//
// ⚠️ 语义分工：本函数 AND 组合，仅用于 dedup 相似度判定（addLongTermMemory
// —— 全 token 命中才算"相似"，避免单词重合的无关记忆被误判重复）。搜索
// 一律用 fts5OrQuery —— BM25 排序场景下 AND 会让一个缺席词（查询 "frpc"
// vs 索引 "frps"）零化整个结果集（用户报告："2008 机器 IP 地址 frpc 转发"
// 搜不出含 "2008" 的记忆）。
func fts5SafeQuery(raw string) string {
	// Space-separate CJK runs so Chinese substrings become matchable tokens.
	raw = cjkSpaceRuns(raw)
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return `""` // match nothing safely
	}
	// Cap the number of tokens: an extremely long user message (pasted code,
	// log dump, document) could produce hundreds of AND terms — the MATCH
	// expression would exceed SQLite limits or degrade to meaningless AND of
	// every char. Keep the most distinctive tokens: skip common stopwords and
	// cap at maxQueryTokens. This keeps recall relevant and the query bounded.
	const maxQueryTokens = 24
	var quoted []string
	kept := 0
	for _, f := range fields {
		if kept >= maxQueryTokens {
			break
		}
		if isStopToken(f) {
			continue
		}
		// FTS5 string literal: double the double-quote to escape it.
		f = strings.ReplaceAll(f, `"`, `""`)
		quoted = append(quoted, `"`+f+`"`)
		kept++
	}
	if len(quoted) == 0 {
		// All tokens were stopwords — fall back to the first raw token so the
		// query still has SOME anchor instead of matching nothing.
		f := strings.ReplaceAll(fields[0], `"`, `""`)
		return `"` + f + `"`
	}
	return strings.Join(quoted, " AND ")
}

// fts5OrQuery builds an FTS5 MATCH expression with OR semantics for SEARCH
// (recall): any token hit recalls the row, bm25() ranks it. Multi-term BM25
// search must not AND-combine tokens — one absent word (query "frpc" vs
// indexed "frps") zeroes out the whole result set even when a high-IDF term
// ("2008") matches exactly (user report). AND stays reserved for dedup
// similarity (fts5SafeQuery) where strict full-token overlap is the point.
func fts5OrQuery(raw string) string {
	return strings.Join(fts5Tokens(raw), " OR ")
}

// fts5Tokens applies the same sanitization pipeline as fts5SafeQuery (CJK
// spacing, quoting/escaping, stopword filter, token cap) and returns the
// quoted literal tokens; the caller picks the joiner.
func fts5Tokens(raw string) []string {
	raw = cjkSpaceRuns(raw)
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return []string{`""`}
	}
	const maxQueryTokens = 24
	var quoted []string
	kept := 0
	for _, f := range fields {
		if kept >= maxQueryTokens {
			break
		}
		if isStopToken(f) {
			continue
		}
		f = strings.ReplaceAll(f, `"`, `""`)
		quoted = append(quoted, `"`+f+`"`)
		kept++
	}
	if len(quoted) == 0 {
		f := strings.ReplaceAll(fields[0], `"`, `""`)
		return []string{`"` + f + `"`}
	}
	return quoted
}

// isStopToken reports whether a token is a common noise word that adds no
// retrieval value. Chinese is space-separated into SINGLE chars by
// cjkSpaceRuns, so a Chinese char like 会 is ambiguous (开会 vs 我会) — only
// pure particles (的/了/吗/呢/吧/啊) are dropped. English multi-char
// stopwords (the/a/is/to) are filtered normally. Skipping noise keeps long
// queries bounded and relevant.
func isStopToken(tok string) bool {
	// Pure Chinese particles — safe to drop even as single chars.
	switch tok {
	case "的", "了", "吗", "呢", "吧", "啊", "么", "着", "过":
		return true
	}
	// Any remaining CJK char is a real token (never a stopword).
	for _, r := range tok {
		if isCJKRune(r) {
			return false
		}
	}
	// ASCII multi-char stopwords.
	switch tok {
	case "the", "a", "an", "is", "are", "was", "were", "be", "been", "to", "of",
		"in", "on", "at", "for", "with", "and", "or", "but", "this", "that",
		"it", "you", "i", "we", "they", "he", "she", "can", "could", "would",
		"should", "do", "does", "did", "have", "has", "had", "not", "no", "yes":
		return true
	}
	return false
}

// cjkSpaceRuns inserts a space between adjacent CJK characters so the FTS5
// unicode61 tokenizer treats each CJK char as an individual token. It also
// inserts a space at CJK↔ASCII boundaries ("GLM模型" → "GLM 模 型") so an
// ASCII word and a following CJK char do not fuse into one token. Non-CJK
// characters (ASCII, digits, punctuation) keep their natural word boundaries.
func cjkSpaceRuns(s string) string {
	var sb strings.Builder
	prevIsCJK := false
	prevIsASCIIWord := false
	for _, r := range s {
		isCJK := isCJKRune(r)
		isASCIIWord := isASCIIWordRune(r)
		// Space between two CJK chars.
		if isCJK && prevIsCJK {
			sb.WriteByte(' ')
		}
		// Space at CJK↔ASCII-word boundary so tokens don't fuse.
		if (isCJK && prevIsASCIIWord) || (isASCIIWord && prevIsCJK) {
			sb.WriteByte(' ')
		}
		sb.WriteRune(r)
		prevIsCJK = isCJK
		prevIsASCIIWord = isASCIIWord
	}
	return sb.String()
}

// isASCIIWordRune reports whether r is an ASCII letter or digit (part of a
// natural English/numeric word token).
func isASCIIWordRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// isCJKRune reports whether r is a CJK ideograph (Han) — the primary script
// used in Chinese. Covers CJK Unified Ideographs, Ext A, Compatibility,
// and CJK Extension B/C/D planes.
func isCJKRune(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Extension A
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	case r >= 0x20000 && r <= 0x2A6DF: // CJK Extension B
		return true
	case r >= 0x2A700 && r <= 0x2B73F: // CJK Extension C
		return true
	case r >= 0x2B740 && r <= 0x2B81F: // CJK Extension D
		return true
	}
	return false
}

// Memorize processes conversation messages and stores memories.
// Called on /new command and session end.
// The LLM client+model come from the input — NEVER shared struct state (the
// provider instance serves all sessions; a shared field was raced by concurrent
// memory operations, 2026-09-02 incident).
func (m *XbotMemory) Memorize(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	if !input.ArchiveAll {
		return memory.MemorizeResult{OK: true}, nil
	}
	if input.LLMClient == nil {
		return memory.MemorizeResult{OK: true}, nil
	}
	client, model := input.LLMClient, input.Model

	// 1. Generate session summary → short-term memory
	summary, topics := m.generateSessionSummary(ctx, client, model, input.Messages)
	if summary != "" {
		m.addShortTermMemory(summary, topics, "")
	}

	// 2. Extract atomic memories → long-term memory
	entries := m.extractAtomicMemories(ctx, client, model, input.Messages, 0)
	for _, entry := range entries {
		if err := m.addLongTermMemory(entry); err != nil {
			log.WithError(err).Debug("xbot-memory: failed to add long-term memory")
		}
	}

	// 3. Update core summary
	m.updateCoreSummary(ctx, client, model)

	// 4. Decay heat scores
	m.decayMemories()

	// 5. Evict old short-term memories
	m.evictShortTerm()

	// 6. Enforce long-term memory cap (prune lowest-heat entries)
	m.pruneLongTerm()

	return memory.MemorizeResult{OK: true}, nil
}

// ConsolidateTurn implements memory.TurnConsolidator — lightweight INCREMENTAL
// memory extraction after each turn.
//
// Unlike Memorize (full archive on /new), this:
//   - Throttles: accumulates `consolidateThreshold` new messages before running
//     a real LLM extraction — so we don't burn 3 LLM calls after every turn.
//   - Incremental: only extracts atomic memories from the NEW messages; does NOT
//     regenerate session summary or core summary (those are cheap to defer to
//     /new or compression).
//
// LastConsolidated is updated by the caller (agent) with the message count it
// has already seen; we only process messages[LastConsolidated:].
func (m *XbotMemory) ConsolidateTurn(ctx context.Context, input memory.MemorizeInput) (memory.MemorizeResult, error) {
	if input.LLMClient == nil {
		return memory.MemorizeResult{OK: true}, nil
	}

	// Watermark: only messages after the last consolidation are NEW.
	// We do NOT slice the message list — the full list is passed to the
	// extraction LLM so the request prefix stays IDENTICAL across calls,
	// protecting the provider-side prefix cache. The watermark is passed as
	// a start index and marked in the prompt.
	start := input.LastConsolidated
	if start < 0 {
		start = 0
	}
	if start >= len(input.Messages) {
		// Nothing new since last consolidation.
		return memory.MemorizeResult{NewLastConsolidated: len(input.Messages), OK: true}, nil
	}

	// Time-based throttle: at most one consolidation per session per window.
	// Uses in-memory per-instance state; the watermark (LastConsolidated) is
	// still advanced every turn so nothing is lost on restart.
	m.mu.Lock()
	now := time.Now()
	shouldExtract := m.lastConsolidateAt.IsZero() || now.Sub(m.lastConsolidateAt) >= consolidateInterval
	if shouldExtract {
		m.lastConsolidateAt = now
	}
	m.mu.Unlock()
	if !shouldExtract {
		// Within the throttle window — skip LLM call, keep watermark advancing.
		return memory.MemorizeResult{NewLastConsolidated: len(input.Messages), OK: true}, nil
	}

	// Extract atomic memories from the full list, watermark = start.
	// No slice → identical prefix across consolidations.
	// The LLM client comes from THIS input (per-operation ownership — the shared
	// m.llmClient field was a cross-session race: concurrent sessions' memory
	// ops overwrote each other's model/endpoint, 2026-09-02 incident).
	entries := m.extractAtomicMemories(ctx, input.LLMClient, input.Model, input.Messages, start)
	added := 0
	for _, entry := range entries {
		if err := m.addLongTermMemory(entry); err == nil {
			added++
		}
	}
	if added > 0 {
		m.pruneLongTerm()
	}
	log.WithFields(log.Fields{
		"full_msgs": len(input.Messages),
		"new_msgs":  len(input.Messages) - start,
		"added":     added,
	}).Info("xbot-memory: ConsolidateTurn extracted memories")

	return memory.MemorizeResult{NewLastConsolidated: len(input.Messages), OK: true}, nil
}

// pruneLongTerm enforces the per-user long-term memory cap. Entries beyond
// longTermMaxEntries with the lowest heat_score are deleted.
func (m *XbotMemory) pruneLongTerm() {
	_, err := m.db.Exec(`
		DELETE FROM xbot_long_term_memories
		WHERE `+m.scopeWhere("")+` AND id NOT IN (
			SELECT id FROM xbot_long_term_memories
			WHERE `+m.scopeWhere("")+`
			ORDER BY heat_score DESC, importance DESC
			LIMIT ?
		)
	`, m.scopeArg(), m.scopeArg(), longTermMaxEntries)
	if err != nil {
		log.WithError(err).Debug("xbot-memory: pruneLongTerm failed")
	}
}

// Close releases resources.
func (m *XbotMemory) Close() error {
	// DB connection is shared — don't close it here.
	return nil
}

// --- Data types ---

// LongTermMemory is a row in xbot_long_term_memories.
type LongTermMemory struct {
	ID             int64
	UserID         int64
	TenantID       int64
	Type           string
	Content        string
	Keywords       string
	Tags           string
	SourceSession  string
	Importance     float64
	HeatScore      float64
	AccessCount    int
	CreatedAt      time.Time
	LastAccessedAt time.Time
	// Scope: 'global' (injected into every session's Recall) or 'session'
	// (auto-extracted session-local state — NEVER cross-session injected;
	// searchable on demand via memory_search). Auto-extraction paths
	// (ConsolidateTurn/PreCompress/Memorize) write 'session'; explicit
	// memory_add defaults to 'global' (the model decided it is durable).
	Scope string
	// SupersededBy: the ID of the entry that replaced this one (0 = active).
	// A new fact that contradicts an old one supersedes it instead of
	// double-storing both ("cluster moved from X to Y" keeps only the new one).
	SupersededBy int64
}

// ShortTermMemory is a row in xbot_short_term_memories.
type ShortTermMemory struct {
	ID          int64
	TenantID    int64
	SessionID   string
	Summary     string
	KeyTopics   string
	CreatedAt   time.Time
	HeatScore   float64
	AccessCount int
}

// MemoryEntry is a user-facing memory representation (for tools).
type MemoryEntry struct {
	ID         int64   `json:"id"`
	Type       string  `json:"type"`
	Content    string  `json:"content"`
	Keywords   string  `json:"keywords,omitempty"`
	Tags       string  `json:"tags,omitempty"`
	Importance float64 `json:"importance"`
	CreatedAt  string  `json:"created_at"`
	Score      float64 `json:"score,omitempty"` // BM25 score (lower = more relevant)
	Scope      string  `json:"scope,omitempty"` // 'global' or 'session' (provenance for the model)
}

// --- Internal search methods ---

func (m *XbotMemory) searchLongTerm(query string, topK int) ([]LongTermMemory, error) {
	if query == "" {
		// No query — return recent high-importance memories
		return m.recentLongTerm(topK)
	}

	// FTS5 BM25 search
	// Note: SQLite FTS5 bm25() returns negative values (lower = more relevant)
	// Session isolation (2026-09-02 redesign): the Recall injection path is
	// GLOBAL-scope, non-superseded only — session-scoped memories (auto-extracted
	// task state from other sessions) never leak into this session; superseded
	// entries (replaced by newer contradicting facts) are filtered at the query
	// level. memory_search (SearchMemories) searches all scopes on demand.
	rows, err := m.db.Query(`
		SELECT ltm.id, ltm.type, ltm.content, ltm.keywords, ltm.tags,
		       ltm.source_session, ltm.importance, ltm.heat_score, ltm.access_count,
		       ltm.created_at, ltm.last_accessed_at,
		       bm25(xbot_long_term_memories_fts) as score
		FROM xbot_long_term_memories_fts fts
		JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
		WHERE `+m.scopeWhere("ltm")+` AND ltm.scope = 'global' AND ltm.superseded_by IS NULL AND xbot_long_term_memories_fts MATCH ?
		ORDER BY score ASC
		LIMIT ?
	`, m.scopeArg(), fts5OrQuery(query), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []LongTermMemory
	for rows.Next() {
		var mem LongTermMemory
		var score float64
		if err := rows.Scan(&mem.ID, &mem.Type, &mem.Content, &mem.Keywords, &mem.Tags,
			&mem.SourceSession, &mem.Importance, &mem.HeatScore, &mem.AccessCount,
			&mem.CreatedAt, &mem.LastAccessedAt, &score); err != nil {
			continue
		}
		results = append(results, mem)
		// Update access count and heat
		m.touchMemory(mem.ID)
	}
	return results, nil
}

func (m *XbotMemory) recentLongTerm(topK int) ([]LongTermMemory, error) {
	// Global-scope, non-superseded only (same filter as searchLongTerm — this
	// feeds the Recall injection, updateCoreSummary and CompressContext, all
	// user-global paths).
	rows, err := m.db.Query(`
		SELECT id, type, content, keywords, tags, source_session,
		       importance, heat_score, access_count, created_at, last_accessed_at
		FROM xbot_long_term_memories
		WHERE `+m.scopeWhere("")+` AND scope = 'global' AND superseded_by IS NULL
		ORDER BY importance DESC, heat_score DESC, created_at DESC
		LIMIT ?
	`, m.scopeArg(), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []LongTermMemory
	for rows.Next() {
		var mem LongTermMemory
		if err := rows.Scan(&mem.ID, &mem.Type, &mem.Content, &mem.Keywords, &mem.Tags,
			&mem.SourceSession, &mem.Importance, &mem.HeatScore, &mem.AccessCount,
			&mem.CreatedAt, &mem.LastAccessedAt); err != nil {
			continue
		}
		results = append(results, mem)
	}
	return results, nil
}

func (m *XbotMemory) searchShortTerm(query string, topK int) ([]ShortTermMemory, error) {
	if query == "" {
		return m.recentShortTerm(topK)
	}

	rows, err := m.db.Query(`
		SELECT stm.id, stm.tenant_id, stm.session_id, stm.summary, stm.key_topics,
		       stm.created_at, stm.heat_score, stm.access_count,
		       bm25(xbot_short_term_memories_fts) as score
		FROM xbot_short_term_memories_fts fts
		JOIN xbot_short_term_memories stm ON stm.id = fts.rowid
		WHERE `+m.scopeWhere("stm")+` AND xbot_short_term_memories_fts MATCH ?
		ORDER BY score ASC
		LIMIT ?
	`, m.scopeArg(), fts5OrQuery(query), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ShortTermMemory
	for rows.Next() {
		var mem ShortTermMemory
		var score float64
		if err := rows.Scan(&mem.ID, &mem.TenantID, &mem.SessionID, &mem.Summary,
			&mem.KeyTopics, &mem.CreatedAt, &mem.HeatScore, &mem.AccessCount, &score); err != nil {
			continue
		}
		results = append(results, mem)
	}
	return results, nil
}

func (m *XbotMemory) recentShortTerm(topK int) ([]ShortTermMemory, error) {
	rows, err := m.db.Query(`
		SELECT id, tenant_id, session_id, summary, key_topics,
		       created_at, heat_score, access_count
		FROM xbot_short_term_memories
		WHERE `+m.scopeWhere("")+`
		ORDER BY created_at DESC
		LIMIT ?
	`, m.scopeArg(), topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ShortTermMemory
	for rows.Next() {
		var mem ShortTermMemory
		if err := rows.Scan(&mem.ID, &mem.TenantID, &mem.SessionID, &mem.Summary,
			&mem.KeyTopics, &mem.CreatedAt, &mem.HeatScore, &mem.AccessCount); err != nil {
			continue
		}
		results = append(results, mem)
	}
	return results, nil
}

// --- Write methods ---

func (m *XbotMemory) addShortTermMemory(summary, topics, sessionID string) error {
	_, err := m.db.Exec(`
		INSERT INTO xbot_short_term_memories (user_id, tenant_id, session_id, summary, key_topics)
		VALUES (?, ?, ?, ?, ?)
	`, m.scopeArg(), m.tenantID, sessionID, summary, topics)
	if err != nil {
		return err
	}
	log.WithField("session_id", sessionID).Debug("xbot-memory: short-term memory added")
	return nil
}

func (m *XbotMemory) addLongTermMemory(entry LongTermMemory) error {
	entry.UserID = m.scopeArg()
	entry.TenantID = m.tenantID

	// Check for similar memories using FTS5 BM25 similarity.
	// bm25() returns negative values; LOWER = more relevant (SQLite docs:
	// "better matches are assigned numerically lower scores" — same ordering
	// as the `ORDER BY score ASC` in searchLongTerm). A candidate whose
	// bm25() score is BELOW the threshold (more negative) has strong keyword
	// overlap → treat as duplicate and skip. This prevents memory bloat from
	// near-identical facts.
	var dupID int64
	err := m.db.QueryRow(`
		SELECT ltm.id
		FROM xbot_long_term_memories_fts fts
		JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
		WHERE `+m.scopeWhere("ltm")+`
		  AND ltm.superseded_by IS NULL
		  AND xbot_long_term_memories_fts MATCH ?
		  AND bm25(xbot_long_term_memories_fts) < ?
		ORDER BY bm25(xbot_long_term_memories_fts) ASC
		LIMIT 1
	`, m.scopeArg(), fts5SafeQuery(entry.Keywords), dedupSimilarityThreshold).Scan(&dupID)
	if err == nil && dupID > 0 {
		// Duplicate found — skip. Log at debug to avoid noise.
		log.WithFields(log.Fields{
			"dup_id": dupID,
			"type":   entry.Type,
		}).Debug("xbot-memory: duplicate memory skipped (BM25 similarity)")
		return nil
	}

	// Session isolation: resolve the scope ('global' default — legacy callers
	// and explicit memory_add; auto-extraction sets 'session').
	scope := entry.Scope
	if scope == "" {
		scope = "global"
	}

	result, err := m.db.Exec(`
		INSERT INTO xbot_long_term_memories (user_id, tenant_id, type, content, keywords, tags, source_session, importance, search_text, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.UserID, entry.TenantID, entry.Type, entry.Content, entry.Keywords, entry.Tags,
		entry.SourceSession, entry.Importance, buildSearchText(entry.Content, entry.Keywords, entry.Tags), scope)
	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()

	// Write Markdown file (source of truth, human-readable)
	m.writeMemoryFile(id, entry)

	log.WithFields(log.Fields{
		"id":   id,
		"type": entry.Type,
	}).Debug("xbot-memory: long-term memory added")
	return nil
}

// buildSearchText concatenates content + keywords + tags into the FTS search
// column with CJK runs space-separated (each Chinese char becomes its own
// token), matching the query-side cjkSpaceRuns transform in fts5SafeQuery.
func buildSearchText(parts ...string) string {
	return cjkSpaceRuns(strings.Join(parts, " "))
}

func (m *XbotMemory) writeMemoryFile(id int64, entry LongTermMemory) {
	if m.baseDir == "" {
		return
	}
	filename := fmt.Sprintf("%s_%d.md", entry.Type, id)
	path := filepath.Join(m.baseDir, "notes", filename)

	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s Memory #%d\n\n", entry.Type, id)
	fmt.Fprintf(&sb, "**Type:** %s\n", entry.Type)
	fmt.Fprintf(&sb, "**Keywords:** %s\n", entry.Keywords)
	if entry.Tags != "" {
		fmt.Fprintf(&sb, "**Tags:** %s\n", entry.Tags)
	}
	fmt.Fprintf(&sb, "**Importance:** %.2f\n", entry.Importance)
	fmt.Fprintf(&sb, "**Source Session:** %s\n", entry.SourceSession)
	fmt.Fprintf(&sb, "**Created:** %s\n\n", time.Now().Format(time.RFC3339))
	sb.WriteString(entry.Content)
	sb.WriteString("\n")

	// Update file_path in DB
	m.db.Exec(`UPDATE xbot_long_term_memories SET file_path = ? WHERE id = ?`, path, id)

	os.WriteFile(path, []byte(sb.String()), 0o644)
}

func (m *XbotMemory) touchMemory(id int64) {
	m.db.Exec(`
		UPDATE xbot_long_term_memories
		SET access_count = access_count + 1,
		    last_accessed_at = CURRENT_TIMESTAMP
		WHERE id = ?
	`, id)
}

func (m *XbotMemory) readCoreSummary() string {
	path := filepath.Join(m.baseDir, coreSummaryFileName)
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return ""
	}
	if len([]rune(text)) > coreSummaryMaxChars {
		runes := []rune(text)
		return string(runes[:coreSummaryMaxChars]) + "\n...(truncated)"
	}
	return text
}

func (m *XbotMemory) writeCoreSummary(content string) {
	path := filepath.Join(m.baseDir, coreSummaryFileName)
	// Atomic write: tmp + rename
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil {
		log.WithError(err).Error("xbot-memory: failed to write core summary")
		return
	}
	os.Rename(tmpPath, path)
}

func (m *XbotMemory) updateCoreSummary(ctx context.Context, client llm.LLM, model string) {
	if client == nil {
		return
	}

	currentSummary := m.readCoreSummary()

	// Get top high-importance memories
	mems, err := m.recentLongTerm(10)
	if err != nil || len(mems) == 0 {
		return
	}

	var memSB strings.Builder
	for _, mem := range mems {
		if mem.Importance >= 0.7 {
			fmt.Fprintf(&memSB, "- [%s] %s\n", mem.Type, mem.Content)
		}
	}
	if memSB.Len() == 0 {
		return
	}

	prompt := fmt.Sprintf(`Current core summary:
%s

Recent high-importance memories:
%s

Update the core summary to incorporate any new critical information.
Keep it under %d characters. Preserve existing important information.
Return ONLY the updated summary text, no explanations.`, currentSummary, memSB.String(), coreSummaryMaxChars)

	resp, err := m.generateLLM(ctx, client, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a memory consolidation agent. Update the core summary concisely."),
		llm.NewUserMessage(prompt),
	}, nil)
	if err != nil {
		return
	}

	if resp.Content != "" {
		m.writeCoreSummary(strings.TrimSpace(resp.Content))
	}
}

// --- Heat decay and eviction ---

func (m *XbotMemory) decayMemories() {
	// heat_score = importance * recency_factor * frequency_factor
	// recency_factor = exp(-days_since_last_access / half_life)
	// frequency_factor = log(1 + access_count)
	_, err := m.db.Exec(`
		UPDATE xbot_long_term_memories
		SET heat_score = importance *
		    exp(-(julianday('now') - julianday(last_accessed_at)) / ?) *
		    log(1 + access_count)
		WHERE `+m.scopeWhere("")+`
	`, heatHalfLifeDays, m.scopeArg())
	if err != nil {
		log.WithError(err).Debug("xbot-memory: decay failed")
	}

	// Slow importance decay for low-heat memories
	_, err = m.db.Exec(`
		UPDATE xbot_long_term_memories
		SET importance = importance * 0.95
		WHERE heat_score < ? AND `+m.scopeWhere("")+`
	`, forgetThreshold, m.scopeArg())
	if err != nil {
		log.WithError(err).Debug("xbot-memory: forget decay failed")
	}
}

func (m *XbotMemory) evictShortTerm() {
	// Keep only the most recent N short-term memories
	_, err := m.db.Exec(`
		DELETE FROM xbot_short_term_memories
		WHERE id NOT IN (
			SELECT id FROM xbot_short_term_memories
			WHERE `+m.scopeWhere("")+`
			ORDER BY created_at DESC
			LIMIT ?
		) AND `+m.scopeWhere("")+`
	`, m.scopeArg(), defaultShortTermCapacity, m.scopeArg())
	if err != nil {
		log.WithError(err).Debug("xbot-memory: short-term eviction failed")
	}
}

// --- LLM-driven memory extraction ---

func (m *XbotMemory) generateSessionSummary(ctx context.Context, client llm.LLM, model string, messages []llm.ChatMessage) (string, string) {
	if client == nil || len(messages) == 0 {
		return "", ""
	}

	// Format messages for the LLM
	var msgSB strings.Builder
	for _, msg := range messages {
		role := msg.Role
		content := msg.Content
		if len([]rune(content)) > 500 {
			content = truncateRunes(content, 500)
		}
		fmt.Fprintf(&msgSB, "[%s] %s\n", role, content)
	}

	prompt := fmt.Sprintf(`Summarize the following conversation in 2-3 sentences.
Focus on key decisions, outcomes, and important context.

Conversation:
%s

Return ONLY the summary, no preamble. Also provide 3-5 comma-separated key topics.`, msgSB.String())

	resp, err := m.generateLLM(ctx, client, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a conversation summarizer. Be concise and factual."),
		llm.NewUserMessage(prompt),
	}, nil)
	if err != nil || resp.Content == "" {
		return "", ""
	}

	// Simple topic extraction from the summary
	topics := extractKeywords(resp.Content)
	return resp.Content, topics
}

// extractMemoriesToolDef is the internal tool for LLM-based memory extraction.
type extractMemoriesToolDef struct{}

func (t *extractMemoriesToolDef) Name() string { return "extract_memories" }
func (t *extractMemoriesToolDef) Description() string {
	return "Extract durable, cross-session atomic memories (facts, preferences, decisions, skills) from the conversation. NEVER include session-local state, transient task progress, or system-injected metadata."
}
func (t *extractMemoriesToolDef) Parameters() []llm.ToolParam {
	return []llm.ToolParam{
		{Name: "memories", Type: "array", Required: true, Description: "Extracted atomic memories",
			Items: &llm.ToolParamItems{
				Type: "object",
				Properties: map[string]any{
					"type":       map[string]any{"type": "string", "description": "Memory type: fact/preference/event/decision/skill"},
					"content":    map[string]any{"type": "string", "description": "Single self-contained fact or event (1-2 sentences)"},
					"keywords":   map[string]any{"type": "string", "description": "3-5 comma-separated keywords for search"},
					"tags":       map[string]any{"type": "string", "description": "1-3 comma-separated category tags"},
					"importance": map[string]any{"type": "number", "description": "0.0-1.0 importance score"},
				},
				Required: []string{"type", "content", "keywords"},
			}},
	}
}

type extractedMemory struct {
	Type       string  `json:"type"`
	Content    string  `json:"content"`
	Keywords   string  `json:"keywords"`
	Tags       string  `json:"tags"`
	Importance float64 `json:"importance"`
}

// extractAtomicMemories extracts atomic memories from conversation messages.
//
// IMPORTANT: messages is the FULL message list (never sliced). startIdx marks
// the consolidation watermark — messages[0:startIdx] were already processed in
// a previous extraction; only messages[startIdx:] are new. We pass the full
// list so the LLM request prefix stays IDENTICAL across calls (protecting
// provider-side prefix cache). The prompt tells the model which range to focus
// on instead of physically slicing.
//
// Garbage filtering: system-injected context blocks (<context>, <system-reminder>,
// <dynamic-context>, peer/collaborator notes, workdir hints, sender/language
// meta) are STRIPPED before sending to the LLM. These are already in the system
// prompt — storing them as memories is pure bloat (the "垃圾记忆" complaint).
func (m *XbotMemory) extractAtomicMemories(ctx context.Context, client llm.LLM, model string, messages []llm.ChatMessage, startIdx int) []LongTermMemory {
	if client == nil || len(messages) == 0 {
		return nil
	}
	if startIdx < 0 {
		startIdx = 0
	}
	if startIdx >= len(messages) {
		return nil
	}

	// Format messages, stripping system-injected garbage blocks.
	// Also skip non-user/assistant/system roles that carry no memory value.
	var msgSB strings.Builder
	for i, msg := range messages {
		role := msg.Role
		content := msg.Content

		// Strip <system-reminder> / <context> / <dynamic-context> blocks —
		// they contain injected meta (workdir, peers, language, sender) that
		// must NEVER become memories.
		content = stripInjectedBlocks(content)

		// Skip messages that became empty after stripping.
		if strings.TrimSpace(content) == "" {
			continue
		}
		if len([]rune(content)) > 800 {
			content = truncateRunes(content, 800)
		}

		// Tag new vs old so the LLM focuses on the incremental window while
		// still seeing identical prefixes (prefix-cache friendly).
		marker := "old"
		if i >= startIdx {
			marker = "NEW"
		}
		fmt.Fprintf(&msgSB, "[%s|%s] %s\n", role, marker, content)
	}

	// If the new window contributes nothing, bail.
	text := msgSB.String()
	if text == "" {
		return nil
	}

	prompt := fmt.Sprintf(`Analyze the following conversation and extract atomic memories.

	REMEMBER THE PURPOSE: these memories are stored ONCE and shown to the agent in
	ALL FUTURE SESSIONS — every conversation, every channel, every chat. They are
	the agent's LONG-TERM knowledge about this user and their projects.

For each memory, provide:
- type: one of "fact", "preference", "event", "decision", "skill"
- content: a single, self-contained fact or event (1-2 sentences)
- keywords: 3-5 comma-separated keywords for search
- tags: 1-3 comma-separated category tags
- importance: 0.0-1.0 (how important is this for future interactions?)

IMPORTANT — what to EXTRACT (from messages tagged [NEW]):
- Durable facts about the user: identity, job, machines, accounts, projects
- Long-term preferences, habits, and communication style
- Project-specific technical knowledge: architecture, file paths, dependencies
- Key decisions and their rationale (these persist across sessions)
- Reusable skills and patterns (how to deploy, how to test, tool knowledge)
- Errors with lasting fixes (a recurring failure + its solution)

IMPORTANT — what to NEVER extract (session-specific state):
- Session-local progress: "working on X", "currently fixing Y", "about to do Z"
- Transient context: the current task's step-by-step flow, open questions
	pending in THIS conversation, temporary file contents, one-off command output
	- Anything that only makes sense within THIS conversation and would confuse or
	mislead the agent in a DIFFERENT future session
- System-injected metadata: working directories, peer/collaborator notes,
	sender names, language preferences, timestamps, tool call counts
- Messages tagged [old] — already processed in a previous extraction

TEST FOR EVERY CANDIDATE: "Would this be useful, correct, and non-confusing
to the agent in a brand-new session next week?" If the answer is no, drop it.
	If there are no NEW messages worth remembering, return an empty memories array.

Conversation:
%s`, text)

	resp, err := m.generateLLM(ctx, client, model, []llm.ChatMessage{
		llm.NewSystemMessage("You are a long-term memory curator. Extract only DURABLE, CROSS-SESSION memories. Never store session-local state, transient task progress, or system-injected metadata. If nothing durable exists, return an empty array."),
		llm.NewUserMessage(prompt),
	}, []llm.ToolDefinition{&extractMemoriesToolDef{}})
	if err != nil || resp == nil {
		return nil
	}

	// Parse tool calls
	var entries []LongTermMemory
	for _, tc := range resp.ToolCalls {
		if tc.Name != "extract_memories" {
			continue
		}
		var args struct {
			Memories []extractedMemory `json:"memories"`
		}
		if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
			continue
		}
		for _, mem := range args.Memories {
			if mem.Type == "" || mem.Content == "" {
				continue
			}
			if mem.Importance == 0 {
				mem.Importance = 0.5
			}
			entries = append(entries, LongTermMemory{
				Type:       mem.Type,
				Content:    mem.Content,
				Keywords:   mem.Keywords,
				Tags:       mem.Tags,
				Importance: mem.Importance,
				// Session isolation (2026-09-02 redesign): auto-extracted
				// memories are SESSION-scoped — they never inject into other
				// sessions' Recall (the global pool is explicit-add only, via
				// memory_add with default scope='global' or PR-4's profile
				// promotion). This kills the "auto-extraction garbage pollutes
				// every session" problem at the storage level.
				Scope: "session",
			})
		}
	}

	return entries
}

// stripInjectedBlocks removes system-injected context from a message before it
// is fed to the memory extraction LLM. Without this, the LLM treats injected
// metadata (workdir, peers, sender, language, timestamps, tool stats) as
// conversation facts and stores them as memories — the "垃圾记忆" problem.
//
// Strips:
//   - <context>...</context>
//   - <system-reminder>...</system-reminder>
//   - <dynamic-context>...</dynamic-context>
//   - standalone lines starting with known injection markers (📂 👥 ⚠️ ✅
//     "已完成 N 次工具调用", "本轮使用:", "TODO:", "协作规则:", "行为提醒:")
func stripInjectedBlocks(content string) string {
	if content == "" {
		return content
	}

	// Strip XML-injected blocks.
	for _, tag := range []string{"context", "system-reminder", "dynamic-context"} {
		open := "<" + tag + ">"
		close := "</" + tag + ">"
		for {
			start := strings.Index(content, open)
			if start < 0 {
				break
			}
			end := strings.Index(content[start:], close)
			if end < 0 {
				// Unclosed block — drop from start to end of string.
				content = content[:start]
				break
			}
			end = start + end + len(close)
			content = content[:start] + content[end:]
		}
	}

	// Strip standalone injection-marker lines.
	var sb strings.Builder
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if isInjectedMetaLine(trimmed) {
			continue
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// isInjectedMetaLine reports whether a line is system-injected metadata
// (never worth remembering).
func isInjectedMetaLine(line string) bool {
	prefixes := []string{
		"📂", "👥", "⚠️", "✅", "❌",
		"已完成", "本轮使用:", "TODO:", "协作规则:", "行为提醒:",
		"正在处理中", "用户原始需求",
		"现在时间：", "当前时间：",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return false
}

// extractKeywords does simple keyword extraction from text.
func extractKeywords(text string) string {
	// Simple approach: take first few words, filter common words
	words := strings.Fields(strings.ToLower(text))
	var keywords []string
	stopWords := map[string]bool{
		"the": true, "a": true, "an": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true,
		"and": true, "or": true, "but": true, "in": true, "on": true,
		"at": true, "to": true, "for": true, "of": true, "with": true,
		"this": true, "that": true, "it": true, "from": true, "by": true,
	}
	for _, w := range words {
		if len(w) > 2 && !stopWords[w] {
			keywords = append(keywords, w)
			if len(keywords) >= 5 {
				break
			}
		}
	}
	return strings.Join(keywords, ", ")
}

// --- Public API for tools ---

// SearchMemories searches across all memory tiers.
func (m *XbotMemory) SearchMemories(ctx context.Context, query string, memType string, limit int) ([]MemoryEntry, error) {
	if limit <= 0 {
		limit = 10
	}

	var entries []MemoryEntry

	// Search long-term memories (superseded entries filtered — only current
	// facts are visible; the DB keeps the supersede chain for rollback).
	var rows *sql.Rows
	var err error
	if memType != "" && memType != "all" {
		rows, err = m.db.Query(`
			SELECT ltm.id, ltm.type, ltm.content, ltm.keywords, ltm.tags,
			       ltm.importance, ltm.created_at, ltm.scope,
			       bm25(xbot_long_term_memories_fts) as score
			FROM xbot_long_term_memories_fts fts
			JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
			WHERE `+m.scopeWhere("ltm")+` AND ltm.type = ? AND ltm.superseded_by IS NULL AND xbot_long_term_memories_fts MATCH ?
			ORDER BY score ASC
			LIMIT ?
		`, m.scopeArg(), memType, fts5OrQuery(query), limit)
	} else {
		rows, err = m.db.Query(`
			SELECT ltm.id, ltm.type, ltm.content, ltm.keywords, ltm.tags,
			       ltm.importance, ltm.created_at, ltm.scope,
			       bm25(xbot_long_term_memories_fts) as score
			FROM xbot_long_term_memories_fts fts
			JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
			WHERE `+m.scopeWhere("ltm")+` AND ltm.superseded_by IS NULL AND xbot_long_term_memories_fts MATCH ?
			ORDER BY score ASC
			LIMIT ?
		`, m.scopeArg(), fts5OrQuery(query), limit)
	}
	if err != nil {
		return nil, err
	}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var e MemoryEntry
			if err := rows.Scan(&e.ID, &e.Type, &e.Content, &e.Keywords, &e.Tags,
				&e.Importance, &e.CreatedAt, &e.Scope, &e.Score); err != nil {
				continue
			}
			entries = append(entries, e)
		}
	}

	// Also search short-term memories
	stMems, _ := m.searchShortTerm(query, 3)
	for _, stm := range stMems {
		entries = append(entries, MemoryEntry{
			ID:        stm.ID,
			Type:      "session_summary",
			Content:   stm.Summary,
			Keywords:  stm.KeyTopics,
			CreatedAt: stm.CreatedAt.Format(time.RFC3339),
		})
	}

	if len(entries) > limit {
		entries = entries[:limit]
	}

	return entries, nil
}

// AddMemory manually adds a long-term memory entry.
// The entry's Scope is respected ("" defaults to 'global' — the explicit
// memory_add path; auto-extraction writes 'session' via addLongTermMemory).
func (m *XbotMemory) AddMemory(ctx context.Context, entry LongTermMemory) (int64, error) {
	entry.UserID = m.scopeArg()
	entry.TenantID = m.tenantID
	if entry.Importance == 0 {
		entry.Importance = 0.5
	}
	if entry.Keywords == "" {
		entry.Keywords = extractKeywords(entry.Content)
	}
	scope := entry.Scope
	if scope == "" {
		scope = "global"
	}

	result, err := m.db.Exec(`
		INSERT INTO xbot_long_term_memories (user_id, tenant_id, type, content, keywords, tags, source_session, importance, search_text, scope)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.UserID, entry.TenantID, entry.Type, entry.Content, entry.Keywords, entry.Tags,
		entry.SourceSession, entry.Importance, buildSearchText(entry.Content, entry.Keywords, entry.Tags), scope)
	if err != nil {
		return 0, err
	}

	id, _ := result.LastInsertId()
	entry.ID = id

	// Supersede chain (2026-09-02 redesign): an explicit add is the model
	// DELIBERATELY writing an updated fact. Strong-matching ACTIVE GLOBAL
	// entries are marked superseded_by=<new id> (rows preserved for rollback,
	// filtered from injection/search) instead of double-storing contradictory
	// facts ("cluster at X" + "cluster moved to Y" both injected — the model
	// could not tell which was current: the "stale memory" complaint).
	// Session-scoped auto-extraction (addLongTermMemory) keeps its dedup-skip
	// and NEVER supersedes: task-local state must not invalidate user-level facts.
	if scope == "global" {
		rows, err := m.db.Query(`
			SELECT ltm.id
			FROM xbot_long_term_memories_fts fts
			JOIN xbot_long_term_memories ltm ON ltm.id = fts.rowid
			WHERE `+m.scopeWhere("ltm")+` AND ltm.scope = 'global' AND ltm.superseded_by IS NULL
			  AND ltm.id != ?
			  AND xbot_long_term_memories_fts MATCH ?
			  AND bm25(xbot_long_term_memories_fts) < ?
		`, m.scopeArg(), id, fts5SafeQuery(entry.Keywords), dedupSimilarityThreshold)
		if err == nil {
			var staleIDs []int64
			for rows.Next() {
				var staleID int64
				if err := rows.Scan(&staleID); err == nil {
					staleIDs = append(staleIDs, staleID)
				}
			}
			rows.Close()
			for _, staleID := range staleIDs {
				if _, err := m.db.Exec(`UPDATE xbot_long_term_memories SET superseded_by = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id, staleID); err != nil {
					log.WithError(err).WithFields(log.Fields{
						"stale_id": staleID,
						"new_id":   id,
					}).Warn("xbot-memory: supersede update failed")
				} else {
					log.WithFields(log.Fields{
						"stale_id": staleID,
						"new_id":   id,
						"type":     entry.Type,
					}).Debug("xbot-memory: stale memory superseded")
				}
			}
		}
	}

	m.writeMemoryFile(id, entry)
	return id, nil
}

// DeleteMemory deletes a long-term memory by ID.
func (m *XbotMemory) DeleteMemory(ctx context.Context, id int64) error {
	// Get file_path before deleting
	var filePath string
	m.db.QueryRow(`SELECT file_path FROM xbot_long_term_memories WHERE id = ? AND `+m.scopeWhere("")+``, id, m.scopeArg()).Scan(&filePath)

	_, err := m.db.Exec(`DELETE FROM xbot_long_term_memories WHERE id = ? AND `+m.scopeWhere("")+``, id, m.scopeArg())
	if err != nil {
		return err
	}

	// Remove Markdown file
	if filePath != "" {
		os.Remove(filePath)
	}
	return nil
}

// ListMemories lists memories with optional filtering.
func (m *XbotMemory) ListMemories(ctx context.Context, memType string, limit int) ([]MemoryEntry, error) {
	if limit <= 0 {
		limit = 20
	}

	log.WithFields(log.Fields{
		"user_id":   m.scopeArg(),
		"tenant_id": m.tenantID,
		"type":      memType,
		"limit":     limit,
	}).Info("xbot-memory: ListMemories called")

	var rows *sql.Rows
	var err error
	if memType != "" && memType != "all" {
		rows, err = m.db.Query(`
			SELECT id, type, content, keywords, tags, importance, created_at, scope
			FROM xbot_long_term_memories
			WHERE `+m.scopeWhere("")+` AND superseded_by IS NULL AND type = ?
			ORDER BY importance DESC, created_at DESC
			LIMIT ?
		`, m.scopeArg(), memType, limit)
	} else {
		rows, err = m.db.Query(`
			SELECT id, type, content, keywords, tags, importance, created_at, scope
			FROM xbot_long_term_memories
			WHERE `+m.scopeWhere("")+` AND superseded_by IS NULL
			ORDER BY importance DESC, created_at DESC
			LIMIT ?
		`, m.scopeArg(), limit)
	}
	if err != nil {
		log.WithError(err).WithField("user_id", m.scopeArg()).Error("xbot-memory: ListMemories query failed")
		return nil, err
	}
	defer rows.Close()

	var entries []MemoryEntry
	for rows.Next() {
		var e MemoryEntry
		if err := rows.Scan(&e.ID, &e.Type, &e.Content, &e.Keywords, &e.Tags, &e.Importance, &e.CreatedAt, &e.Scope); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// UpdateMemory updates a memory's content.
func (m *XbotMemory) UpdateMemory(ctx context.Context, id int64, content, keywords, tags string, importance float64) error {
	_, err := m.db.Exec(`
		UPDATE xbot_long_term_memories
		SET content = ?, keywords = ?, tags = ?, importance = ?,
		    search_text = ?, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND `+m.scopeWhere("")+`
	`, content, keywords, tags, importance, buildSearchText(content, keywords, tags), id, m.scopeArg())
	return err
}

// --- CompressionAware interface implementation ---

// PreCompress extracts critical information from messages about to be compressed
// and saves it to long-term memory before the compression discards them.
// The LLM client+model come from the input (per-operation ownership — NEVER
// shared struct state; see the XbotMemory struct comment).
func (m *XbotMemory) PreCompress(ctx context.Context, input memory.PreCompressInput) (*memory.PreCompressResult, error) {
	if input.LLMClient == nil {
		return &memory.PreCompressResult{}, nil
	}
	client, model := input.LLMClient, input.Model

	// 1. Extract atomic memories from messages about to be compressed
	//    (full list, watermark 0 — compression discards everything, so extract all)
	entries := m.extractAtomicMemories(ctx, client, model, input.MessagesToCompress, 0)
	savedCount := 0
	for _, entry := range entries {
		entry.SourceSession = input.SessionID
		if err := m.addLongTermMemory(entry); err == nil {
			savedCount++
		}
	}

	// 2. Generate session summary → short-term memory
	summary, topics := m.generateSessionSummary(ctx, client, model, input.MessagesToCompress)
	if summary != "" {
		m.addShortTermMemory(summary, topics, input.SessionID)
	}

	// 3. Generate PreserveHints — high-importance memories that the
	//    compression LLM must include in its summary
	var hints []string
	for _, entry := range entries {
		if entry.Importance >= 0.7 {
			hints = append(hints, fmt.Sprintf("[%s] %s", entry.Type, entry.Content))
		}
	}

	log.WithFields(log.Fields{
		"saved_count": savedCount,
		"hint_count":  len(hints),
		"session_id":  input.SessionID,
	}).Info("xbot-memory: PreCompress completed")

	return &memory.PreCompressResult{
		SavedCount:    savedCount,
		PreserveHints: hints,
		SkipCompress:  false,
	}, nil
}

// PostCompress saves the compaction summary and updates memory state after compression.
// The LLM client+model come from the input (per-operation ownership). Historical
// incident (2026-09-02 chat_BD94FA4BB469): PostCompress did NOT receive a client
// and read a shared mutable m.llmClient field — the single-operator deployment
// shares ONE XbotMemory instance across ALL sessions, so a concurrent session's
// ConsolidateTurn/PreCompress overwrote the field between this compression's
// PreCompress and PostCompress, sending the core-summary update to ANOTHER
// session's model/endpoint (F64D's extraction ran with feishu's deepseek config
// and vice versa — a perfect swap). With input.LLMClient the core-summary
// update always uses THIS compression's own client; nil skips the LLM leg
// (summary save + decay still run).
func (m *XbotMemory) PostCompress(ctx context.Context, input memory.PostCompressInput) error {
	// 1. Save compaction summary as a short-term memory entry
	if input.CompactionSummary != "" {
		m.addShortTermMemory(input.CompactionSummary, "compaction_summary", input.SessionID)
	}

	// 2. Update core summary
	m.updateCoreSummary(ctx, input.LLMClient, input.Model)

	// 3. Decay heat scores
	m.decayMemories()

	log.WithFields(log.Fields{
		"session_id":        input.SessionID,
		"removed_msg_count": input.RemovedMessageCount,
	}).Info("xbot-memory: PostCompress completed")

	return nil
}

// CompressContext provides memory context to the compression LLM,
// helping it understand what information is already saved (safe to compress away).
func (m *XbotMemory) CompressContext(ctx context.Context) (string, error) {
	var sb strings.Builder

	// Return current core summary
	coreSummary := m.readCoreSummary()
	if coreSummary != "" {
		sb.WriteString("Already saved in long-term memory (safe to compress away):\n")
		sb.WriteString(coreSummary)
		sb.WriteString("\n")
	}

	// List high-importance memories so the compression LLM knows these are backed up
	mems, err := m.recentLongTerm(10)
	if err == nil && len(mems) > 0 {
		sb.WriteString("\nHigh-importance memories already saved:\n")
		for _, mem := range mems {
			if mem.Importance >= 0.7 {
				fmt.Fprintf(&sb, "- [%s] %s\n", mem.Type, mem.Content)
			}
		}
	}

	return sb.String(), nil
}
