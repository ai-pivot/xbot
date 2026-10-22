// Package agentmem holds the backend-neutral types of the official xbot memory
// provider's storage seam.
//
// They live in their own package because storage imports storage/sqlite (for the
// existing IterationRecord/HistoryRecordType types), so the SQLite backend cannot
// import storage back. A neutral package lets both sides — and the provider —
// share the types without an import cycle, and lets memory providers depend on
// these types without importing a concrete backend.
package agentmem

import "time"

// TermJoin selects how query terms combine.
type TermJoin string

const (
	// JoinAny matches rows containing ANY term (recall/search semantics).
	JoinAny TermJoin = "any"
	// JoinAll matches rows containing EVERY term (dedup similarity).
	JoinAll TermJoin = "all"
)

// Order selects the result ordering of a search.
type Order string

const (
	// OrderRelevance orders by text-match relevance, best first. The score scale
	// is a backend detail (bm25 is negative/lower-is-better, ts_rank is
	// positive/higher-is-better) and never leaves the backend.
	OrderRelevance Order = "relevance"
	// OrderImportance orders by importance, then heat, then recency.
	OrderImportance Order = "importance"
	// OrderRecency orders newest first.
	OrderRecency Order = "recency"
)

// Query is a backend-neutral memory search.
//
// Isolation: rows are ALWAYS scoped by user_id = UserID. UserID == 0 matches only
// ownerless (user_id=0) rows — it must NEVER fall back to tenant_id (that
// scattered one user's memories across sessions; 2026-09-02 incident).
type Query struct {
	UserID   int64
	TenantID int64
	// Terms are pre-tokenized query tokens (already CJK-split, stopword-filtered
	// and de-duplicated by the provider). Empty = no text filter (browse mode).
	Terms []string
	Join  TermJoin
	// Type filters long-term rows by type ("" = any).
	Type string
	// Scope filters long-term rows by scope: "global", "session", or "" (any).
	Scope string
	// SourceSession filters session-scoped rows ("" = any).
	SourceSession string
	// IncludeSuperseded includes rows replaced by a newer fact (default false:
	// only ACTIVE facts are visible).
	IncludeSuperseded bool
	Limit             int
	Order             Order
}

// LongTermMemory is one long-term memory row.
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
	FilePath       string
	// SearchText is the CJK-space-separated text the full-text index is built
	// from. The provider builds it (shared tokenizer); the backend stores/indexes it.
	SearchText string
	// Scope is "global" (injected into every session) or "session"
	// (auto-extracted task state — never cross-session injected).
	Scope string
	// SupersededBy is the id of the entry that replaced this one (0 = active).
	SupersededBy int64
	// Relevance is the backend's match score, best-first ordered. Only set by
	// search results; never persisted.
	Relevance float64
}

// ShortTermMemory is one short-term memory row.
type ShortTermMemory struct {
	ID             int64
	UserID         int64
	TenantID       int64
	SessionID      string
	Summary        string
	KeyTopics      string
	CreatedAt      time.Time
	LastAccessedAt time.Time
	AccessCount    int
	HeatScore      float64
}
