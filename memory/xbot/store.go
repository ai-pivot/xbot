package xbot

import (
	"context"
	"strings"
	"time"

	log "xbot/logger"
	"xbot/storage"
	"xbot/storage/agentmem"
)

// This file is the ONLY place the provider touches storage: every function here
// converts between the provider's own types and the backend-neutral
// storage/agentmem types. No SQL, no driver, no dialect — that is what keeps the
// provider runnable on every backend (see docs/plans/memory-providers-pg-parity.md).

// queryTerms turns a raw user query into the token list the storage layer expects.
// The tokenizer is provider-owned (it defines what a "term" is); the backend only
// renders it as FTS5 MATCH or tsquery. CJK runs are space-separated so each char
// becomes its own token, matching how buildSearchText prepares the indexed text.
func queryTerms(raw string) []string {
	raw = cjkSpaceRuns(raw)
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return nil
	}
	const maxQueryTokens = 24
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(terms) >= maxQueryTokens {
			break
		}
		if isStopToken(f) {
			continue
		}
		terms = append(terms, f)
	}
	if len(terms) == 0 {
		// All tokens were stopwords — keep the first raw token so the query still
		// has an anchor instead of matching nothing.
		return []string{fields[0]}
	}
	return terms
}

// buildSearchText concatenates content + keywords + tags into the indexed text
// with CJK runs space-separated, so the index side matches the query side.
func buildSearchText(parts ...string) string {
	return cjkSpaceRuns(strings.Join(parts, " "))
}

// ── type conversion ──

func toAgentQuery(m *XbotMemory, rawQuery string, limit int, order agentmem.Order) agentmem.Query {
	return agentmem.Query{
		UserID:   m.scopeArg(),
		TenantID: m.tenantID,
		Terms:    queryTerms(rawQuery),
		Join:     agentmem.JoinAny,
		Limit:    limit,
		Order:    order,
	}
}

func fromAgentLongTerm(a agentmem.LongTermMemory) LongTermMemory {
	return LongTermMemory{
		ID:             a.ID,
		UserID:         a.UserID,
		TenantID:       a.TenantID,
		Type:           a.Type,
		Content:        a.Content,
		Keywords:       a.Keywords,
		Tags:           a.Tags,
		SourceSession:  a.SourceSession,
		Importance:     a.Importance,
		HeatScore:      a.HeatScore,
		AccessCount:    a.AccessCount,
		CreatedAt:      a.CreatedAt,
		LastAccessedAt: a.LastAccessedAt,
		Scope:          a.Scope,
		SupersededBy:   a.SupersededBy,
	}
}

func toAgentLongTerm(m LongTermMemory, searchText string) agentmem.LongTermMemory {
	return agentmem.LongTermMemory{
		ID:            m.ID,
		UserID:        m.UserID,
		TenantID:      m.TenantID,
		Type:          m.Type,
		Content:       m.Content,
		Keywords:      m.Keywords,
		Tags:          m.Tags,
		SourceSession: m.SourceSession,
		Importance:    m.Importance,
		SearchText:    searchText,
		Scope:         m.Scope,
	}
}

func fromAgentShortTerm(a agentmem.ShortTermMemory) ShortTermMemory {
	return ShortTermMemory{
		ID:          a.ID,
		TenantID:    a.TenantID,
		SessionID:   a.SessionID,
		Summary:     a.Summary,
		KeyTopics:   a.KeyTopics,
		CreatedAt:   a.CreatedAt,
		HeatScore:   a.HeatScore,
		AccessCount: a.AccessCount,
	}
}

// ensureSchema creates the agent-memory tables once per provider instance.
func (m *XbotMemory) ensureSchema(ctx context.Context) {
	if m.store == nil {
		return
	}
	if err := m.store.EnsureAgentMemorySchema(ctx); err != nil {
		log.WithError(err).Error("xbot-memory: failed to initialize schema")
	}
}

// backfillSearchText fills search_text for rows that predate CJK-aware indexing.
// The transform is provider-side (Go), so the storage layer only reports which
// rows are missing it.
func (m *XbotMemory) backfillSearchText(ctx context.Context) {
	if m.store == nil {
		return
	}
	rows, err := m.store.ListAgentLongTermMissingSearchText(ctx, m.scopeArg(), m.tenantID, 500)
	if err != nil || len(rows) == 0 {
		return
	}
	for _, r := range rows {
		text := buildSearchText(r.Content, r.Keywords, r.Tags)
		if err := m.store.SetAgentLongTermSearchText(ctx, r.ID, text); err != nil {
			log.WithError(err).Debug("xbot-memory: search_text backfill failed")
		}
	}
}

// scopeWhere/scopeArg remain the single definition of isolation: user_id only,
// never tenant_id (falling back to tenant_id re-split memories per session).

var _ = time.Time{}
var _ = storage.AgentMemoryStore(nil)
