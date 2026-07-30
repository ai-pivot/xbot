package vectordb

import (
	"context"
	"testing"
	"time"
)

// mockRetriever is a test Retriever that returns preset results.
type mockRetriever struct {
	name    string
	entries []ArchivalEntry
}

func (m *mockRetriever) Name() string { return m.name }
func (m *mockRetriever) Search(_ context.Context, _ int64, _ SearchRequest) ([]ArchivalEntry, error) {
	return m.entries, nil
}

func makeEntry(id string, sim float32, ts time.Time) ArchivalEntry {
	return ArchivalEntry{ID: id, Similarity: sim, CreatedAt: ts}
}

func TestRRFFuser_TwoLists(t *testing.T) {
	now := time.Now()
	listA := []ArchivalEntry{
		makeEntry("a", 0.9, now),
		makeEntry("b", 0.8, now),
		makeEntry("c", 0.7, now),
	}
	listB := []ArchivalEntry{
		makeEntry("b", 5.0, now), // "b" appears in both lists
		makeEntry("d", 4.0, now),
	}

	fuser := &RRFFuser{K: 60}
	result := fuser.Fuse([][]ArchivalEntry{listA, listB}, 10)

	if len(result) < 3 {
		t.Fatalf("expected at least 3 unique results, got %d", len(result))
	}

	// "b" appears in both lists, should rank first (highest RRF score).
	if result[0].ID != "b" {
		t.Errorf("expected 'b' first (dual-hit), got '%s'", result[0].ID)
	}

	// All entries should have RRF scores (not original cosine scores).
	if result[0].Similarity <= 0 {
		t.Error("expected positive RRF score")
	}
}

func TestRRFFuser_SingleList(t *testing.T) {
	now := time.Now()
	list := []ArchivalEntry{
		makeEntry("a", 0.9, now),
		makeEntry("b", 0.8, now),
	}

	fuser := &RRFFuser{K: 60}
	result := fuser.Fuse([][]ArchivalEntry{list}, 10)

	if len(result) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result))
	}
	// Single list preserves order.
	if result[0].ID != "a" {
		t.Errorf("expected 'a' first, got '%s'", result[0].ID)
	}
}

func TestRRFFuser_Empty(t *testing.T) {
	fuser := &RRFFuser{K: 60}
	result := fuser.Fuse(nil, 5)
	if result != nil {
		t.Errorf("expected nil for empty input, got %v", result)
	}
}

func TestRRFFuser_Limit(t *testing.T) {
	now := time.Now()
	list := []ArchivalEntry{
		makeEntry("a", 0.9, now),
		makeEntry("b", 0.8, now),
		makeEntry("c", 0.7, now),
	}

	fuser := &RRFFuser{K: 60}
	result := fuser.Fuse([][]ArchivalEntry{list}, 2)
	if len(result) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(result))
	}
}

func TestTemporalRanker_RecencyBoost(t *testing.T) {
	now := time.Now()
	old := now.AddDate(0, 0, -90)

	entries := []ArchivalEntry{
		makeEntry("old", 1.0, old),  // high score, old
		makeEntry("new", 0.9, now),  // slightly lower score, recent
	}

	ranker := &TemporalRanker{Weight: 0.3, HalfLife: 30 * 24 * time.Hour}
	result := ranker.Rank(entries)

	// "new" should be boosted above "old" despite slightly lower initial score.
	if result[0].ID != "new" {
		t.Errorf("expected 'new' first after temporal boost, got '%s'", result[0].ID)
	}
}

func TestTemporalRanker_Disabled(t *testing.T) {
	now := time.Now()
	entries := []ArchivalEntry{
		makeEntry("a", 0.9, now),
		makeEntry("b", 0.8, now),
	}

	// Weight=0 → no change.
	ranker := &TemporalRanker{Weight: 0, HalfLife: 30 * 24 * time.Hour}
	result := ranker.Rank(entries)

	if result[0].ID != "a" {
		t.Errorf("expected 'a' first when temporal disabled, got '%s'", result[0].ID)
	}
}

func TestHybridSearcher_VectorOnly(t *testing.T) {
	now := time.Now()
	searcher := NewHybridSearcher(
		[]Retriever{
			&mockRetriever{name: "vector", entries: []ArchivalEntry{makeEntry("v1", 0.9, now)}},
			&mockRetriever{name: "bm25", entries: nil}, // BM25 returns nothing (keyword empty)
		},
		&RRFFuser{K: 60},
		&TemporalRanker{Weight: 0.3, HalfLife: 30 * 24 * time.Hour},
	)

	result, err := searcher.Search(context.Background(), 1, SearchRequest{
		Query: "test",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result))
	}
	if result[0].ID != "v1" {
		t.Errorf("expected 'v1', got '%s'", result[0].ID)
	}
}

func TestHybridSearcher_Hybrid(t *testing.T) {
	now := time.Now()
	searcher := NewHybridSearcher(
		[]Retriever{
			&mockRetriever{name: "vector", entries: []ArchivalEntry{
				makeEntry("shared", 0.9, now),
				makeEntry("vec_only", 0.8, now),
			}},
			&mockRetriever{name: "bm25", entries: []ArchivalEntry{
				makeEntry("shared", 5.0, now),
				makeEntry("bm25_only", 3.0, now),
			}},
		},
		&RRFFuser{K: 60},
		nil, // no post-ranker
	)

	result, err := searcher.Search(context.Background(), 1, SearchRequest{
		Query:   "test",
		Keyword: "shared",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(result) != 3 {
		t.Fatalf("expected 3 unique results, got %d", len(result))
	}
	// "shared" appears in both lists → highest RRF score → first.
	if result[0].ID != "shared" {
		t.Errorf("expected 'shared' first (dual-hit), got '%s'", result[0].ID)
	}
}

func TestHybridSearcher_EmptyResults(t *testing.T) {
	searcher := NewHybridSearcher(
		[]Retriever{
			&mockRetriever{name: "vector", entries: nil},
			&mockRetriever{name: "bm25", entries: nil},
		},
		&RRFFuser{K: 60},
		nil,
	)

	result, err := searcher.Search(context.Background(), 1, SearchRequest{
		Query: "test",
		Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil for empty results, got %v", result)
	}
}
