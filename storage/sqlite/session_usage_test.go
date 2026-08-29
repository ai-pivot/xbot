package sqlite

import (
	"testing"
)

// TestIterationHistoryUsageRoundTrip verifies the v59 usage columns
// (input_tokens / cached_tokens / model) round-trip through
// AppendIterationHistory → GetIterationHistoryByTurn, and that
// GetTenantUsageStats aggregates them per tenant.
func TestIterationHistoryUsageRoundTrip(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat-usage")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	// Turn 1: two iterations on model A with cache hits.
	recs := []IterationRecord{
		{TurnID: 1, Iteration: 1, Content: "a", Tokens: 100, TTFTMs: 800, TPOTMs: 40, TokensPerSec: 25, TotalMs: 4000, InputTokens: 5000, CachedTokens: 3000, Model: "model-a"},
		{TurnID: 1, Iteration: 2, Content: "b", Tokens: 50, TTFTMs: 900, TPOTMs: 45, TokensPerSec: 22, TotalMs: 2250, InputTokens: 6100, CachedTokens: 4000, Model: "model-a"},
		// Turn 2: one iteration on model B, no usage recorded (pre-v59 style row).
		{TurnID: 2, Iteration: 1, Content: "c", Tokens: 30, TTFTMs: 0, TPOTMs: 0, TokensPerSec: 0, TotalMs: 1000},
	}
	for _, rec := range recs {
		if err := sessionSvc.AppendIterationHistory(tenantID, 0, rec.TurnID, rec); err != nil {
			t.Fatalf("AppendIterationHistory failed: %v", err)
		}
	}

	// Round-trip: per-record fields survive the write/read cycle.
	got, err := sessionSvc.GetIterationHistoryByTurn(tenantID, 1)
	if err != nil {
		t.Fatalf("GetIterationHistoryByTurn failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 records for turn 1, got %d", len(got))
	}
	if got[0].InputTokens != 5000 || got[0].CachedTokens != 3000 || got[0].Model != "model-a" {
		t.Errorf("iteration 1 usage fields not round-tripped: got input=%d cached=%d model=%q", got[0].InputTokens, got[0].CachedTokens, got[0].Model)
	}

	// Aggregate: totals across all turns.
	stats, err := sessionSvc.GetTenantUsageStats(tenantID, 10)
	if err != nil {
		t.Fatalf("GetTenantUsageStats failed: %v", err)
	}
	if stats.IterationCount != 3 {
		t.Errorf("expected iteration_count=3, got %d", stats.IterationCount)
	}
	if stats.TurnCount != 2 {
		t.Errorf("expected turn_count=2, got %d", stats.TurnCount)
	}
	if stats.InputTokens != 11100 {
		t.Errorf("expected input_tokens=11100, got %d", stats.InputTokens)
	}
	if stats.OutputTokens != 180 {
		t.Errorf("expected output_tokens=180, got %d", stats.OutputTokens)
	}
	if stats.CachedTokens != 7000 {
		t.Errorf("expected cached_tokens=7000, got %d", stats.CachedTokens)
	}
	if stats.LLMTotalMs != 7250 {
		t.Errorf("expected llm_total_ms=7250, got %d", stats.LLMTotalMs)
	}
	// Averages must exclude unrecorded zeros (turn 2 has ttft/tpot/tokps = 0).
	if stats.AvgTTFTMs != 850 { // (800+900)/2, NOT (800+900+0)/3
		t.Errorf("expected avg_ttft_ms=850 (NULLIF excludes zeros), got %v", stats.AvgTTFTMs)
	}
	if stats.AvgTPOTMs != 42.5 { // (40+45)/2
		t.Errorf("expected avg_tpot_ms=42.5, got %v", stats.AvgTPOTMs)
	}

	// Per-model breakdown: model-a grouped; turn 2's legacy row (model='',
	// pre-v59 style) is EXCLUDED from the per-model split — nameless rows have
	// no model attribution and would show as a misleading "in=0 all-out"
	// entry (real-world: 55k pre-v59 history rows drowning real models).
	if len(stats.ByModel) != 1 {
		t.Fatalf("expected 1 model row (legacy model='' excluded), got %d (%+v)", len(stats.ByModel), stats.ByModel)
	}
	var modelA *UsageModelRow
	for i := range stats.ByModel {
		if stats.ByModel[i].Model == "model-a" {
			modelA = &stats.ByModel[i]
		}
	}
	if modelA == nil {
		t.Fatalf("model-a row missing in by_model: %+v", stats.ByModel)
	}
	if modelA.InputTokens != 11100 || modelA.CachedTokens != 7000 || modelA.Iterations != 2 || modelA.Turns != 1 {
		t.Errorf("model-a breakdown wrong: %+v", modelA)
	}

	// Recent iterations: newest first reversed to chronological order.
	if len(stats.RecentIterations) != 3 {
		t.Fatalf("expected 3 recent iterations, got %d", len(stats.RecentIterations))
	}
	if stats.RecentIterations[0].TurnID != 1 || stats.RecentIterations[0].Iteration != 1 {
		t.Errorf("recent iterations not chronological: [0] = turn %d iter %d", stats.RecentIterations[0].TurnID, stats.RecentIterations[0].Iteration)
	}
	if stats.RecentIterations[2].TurnID != 2 || stats.RecentIterations[2].Model != "" {
		t.Errorf("last recent iteration should be turn 2 (legacy, empty model): %+v", stats.RecentIterations[2])
	}

	// Cross-tenant isolation: another tenant sees nothing.
	otherID, _ := tenantSvc.GetOrCreateTenantID("test", "chat-other")
	other, err := sessionSvc.GetTenantUsageStats(otherID, 10)
	if err != nil {
		t.Fatalf("GetTenantUsageStats(other) failed: %v", err)
	}
	if other.IterationCount != 0 || other.InputTokens != 0 {
		t.Errorf("usage leaked across tenants: %+v", other)
	}
}

// TestGetTenantUsageStats_EmptyTenant verifies zero-value behavior on a
// session with no iteration history (fresh session, plugin zero-state).
func TestGetTenantUsageStats_EmptyTenant(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", "chat-empty")
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}

	stats, err := sessionSvc.GetTenantUsageStats(tenantID, 20)
	if err != nil {
		t.Fatalf("GetTenantUsageStats on empty tenant failed: %v", err)
	}
	if stats.IterationCount != 0 || stats.TurnCount != 0 || stats.InputTokens != 0 {
		t.Errorf("expected zero stats for empty tenant, got %+v", stats)
	}
	if stats.AvgTTFTMs != 0 || stats.AvgTPOTMs != 0 {
		t.Errorf("expected zero averages for empty tenant, got %+v", stats)
	}
	if len(stats.RecentIterations) != 0 {
		t.Errorf("expected no recent iterations, got %d", len(stats.RecentIterations))
	}
}
