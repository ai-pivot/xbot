package vectordb

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	log "xbot/logger"
)

// SearchRequest carries query parameters to all retrievers in a hybrid search pipeline.
type SearchRequest struct {
	Query   string // Natural language query for vector search
	Keyword string // Exact keyword for BM25 search (empty = skip BM25)
	Limit   int    // Maximum results to return
}

// SearchOptions is the public API contract for callers of SearchWithOptions.
// It controls which search strategies are activated.
type SearchOptions struct {
	Keyword string // BM25 keyword (empty = vector-only search)
}

// DefaultSearchOptions returns vector-only search with default temporal weighting.
var DefaultSearchOptions = SearchOptions{}

// Retriever is a single retrieval strategy that produces ranked results.
// Implementations: VectorRetriever, BM25Retriever, (future: GraphRetriever, ...)
type Retriever interface {
	// Name returns a human-readable identifier for logging and debugging.
	Name() string
	// Search executes the retrieval strategy. Returns ranked results (best first).
	// Implementations may return empty results if the request doesn't apply
	// (e.g., BM25Retriever returns empty when Keyword is empty).
	Search(ctx context.Context, tenantID int64, req SearchRequest) ([]ArchivalEntry, error)
}

// Fuser combines multiple ranked result lists into a single ranked list.
// Implementations: RRFFuser, (future: WeightedFuser, LearnedFuser)
type Fuser interface {
	// Fuse merges ranked lists. Each sub-slice is a Retriever's output (best first).
	// The output is a single ranked list of at most `limit` entries.
	Fuse(lists [][]ArchivalEntry, limit int) []ArchivalEntry
}

// PostRanker adjusts the fused results (e.g., temporal recency boost).
// Implementations: TemporalRanker, (future: DiversityRanker, CrossEncoderReranker)
type PostRanker interface {
	// Rank adjusts scores and/or reorders the fused entries.
	Rank(entries []ArchivalEntry) []ArchivalEntry
}

// HybridSearcher orchestrates: retrievers (parallel) → fuse → post-rank.
// It is constructed once and reused across searches.
type HybridSearcher struct {
	retrievers []Retriever
	fuser      Fuser
	postRanker PostRanker // nil = skip post-ranking
}

// NewHybridSearcher constructs a searcher with the given retrievers, fuser, and optional post-ranker.
func NewHybridSearcher(retrievers []Retriever, fuser Fuser, postRanker PostRanker) *HybridSearcher {
	return &HybridSearcher{
		retrievers: retrievers,
		fuser:      fuser,
		postRanker: postRanker,
	}
}

// Search runs all retrievers in parallel, fuses results, and applies post-ranking.
// Empty result lists from individual retrievers are skipped before fusion.
func (h *HybridSearcher) Search(ctx context.Context, tenantID int64, req SearchRequest) ([]ArchivalEntry, error) {
	if req.Limit <= 0 {
		req.Limit = 5
	}

	// Run retrievers in parallel.
	type result struct {
		entries []ArchivalEntry
		err     error
		name    string
	}
	results := make([]result, len(h.retrievers))
	var wg sync.WaitGroup
	for i, r := range h.retrievers {
		wg.Add(1)
		go func(idx int, retriever Retriever) {
			defer wg.Done()
			entries, err := retriever.Search(ctx, tenantID, req)
			results[idx] = result{entries: entries, err: err, name: retriever.Name()}
		}(i, r)
	}
	wg.Wait()

	// Collect non-empty result lists, log errors.
	var lists [][]ArchivalEntry
	for _, r := range results {
		if r.err != nil {
			log.WithError(r.err).WithField("retriever", r.name).Warn("Retriever failed")
			continue
		}
		if len(r.entries) > 0 {
			lists = append(lists, r.entries)
		}
	}

	if len(lists) == 0 {
		return nil, nil
	}

	// Fuse.
	fused := h.fuser.Fuse(lists, req.Limit)

	// Post-rank.
	if h.postRanker != nil {
		fused = h.postRanker.Rank(fused)
	}

	// Final clamp to limit.
	if len(fused) > req.Limit {
		fused = fused[:req.Limit]
	}

	return fused, nil
}

// --- RRFFuser ---

// RRFFuser implements Reciprocal Rank Fusion (Cormack et al., 2009).
// It combines ranked lists using only rank positions, requiring no score calibration.
// K is the smoothing constant (standard value: 60).
type RRFFuser struct {
	K float64
}

// Fuse merges ranked lists using RRF: score(d) = Σ 1/(k + rank_i(d)).
// Documents appearing in multiple lists get higher scores.
func (f *RRFFuser) Fuse(lists [][]ArchivalEntry, limit int) []ArchivalEntry {
	if len(lists) == 0 {
		return nil
	}
	// Single list: pass through (already ranked), clamp to limit.
	if len(lists) == 1 {
		if limit > 0 && len(lists[0]) > limit {
			return lists[0][:limit]
		}
		// Overwrite Similarity with RRF score for consistency.
		k := f.K
		if k == 0 {
			k = 60
		}
		for i := range lists[0] {
			lists[0][i].Similarity = float32(1.0 / (k + float64(i+1)))
		}
		return lists[0]
	}

	k := f.K
	if k == 0 {
		k = 60
	}

	type scored struct {
		entry     ArchivalEntry
		rrfScore  float64
		bestScore float32 // preserve highest original similarity for display
	}

	merged := make(map[string]*scored)

	for _, list := range lists {
		for rank, entry := range list {
			rrf := 1.0 / (k + float64(rank+1))
			if existing, ok := merged[entry.ID]; ok {
				existing.rrfScore += rrf
				if entry.Similarity > existing.bestScore {
					existing.bestScore = entry.Similarity
				}
			} else {
				merged[entry.ID] = &scored{
					entry:     entry,
					rrfScore:  rrf,
					bestScore: entry.Similarity,
				}
			}
		}
	}

	result := make([]ArchivalEntry, 0, len(merged))
	for _, s := range merged {
		s.entry.Similarity = float32(s.rrfScore)
		result = append(result, s.entry)
	}

	// Sort by RRF score descending.
	sort.Slice(result, func(i, j int) bool {
		return result[i].Similarity > result[j].Similarity
	})

	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}

	return result
}

// --- TemporalRanker ---

// TemporalRanker applies an exponential recency boost to fused results.
// final_score = score × (1 + Weight × exp(-age / HalfLife))
// Weight=0 disables temporal weighting. HalfLife is in days.
type TemporalRanker struct {
	Weight   float64       // α: temporal influence strength (0=disabled, 1=equal to RRF)
	HalfLife time.Duration // Half-life for exponential decay
}

// Rank applies temporal recency boost and re-sorts.
func (r *TemporalRanker) Rank(entries []ArchivalEntry) []ArchivalEntry {
	if r.Weight <= 0 || len(entries) == 0 {
		return entries
	}

	halfLifeSecs := r.HalfLife.Seconds()
	if halfLifeSecs <= 0 {
		return entries
	}

	now := time.Now()
	for i := range entries {
		if entries[i].CreatedAt.IsZero() {
			continue
		}
		ageSecs := now.Sub(entries[i].CreatedAt).Seconds()
		if ageSecs < 0 {
			ageSecs = 0 // future timestamps: no penalty
		}
		recency := math.Exp(-ageSecs / halfLifeSecs)
		boost := 1.0 + r.Weight*recency
		entries[i].Similarity = float32(float64(entries[i].Similarity) * boost)
	}

	// Re-sort by boosted score.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Similarity > entries[j].Similarity
	})

	return entries
}
