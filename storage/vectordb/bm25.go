package vectordb

import (
	"context"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	chromem "github.com/philippgille/chromem-go"

	log "xbot/logger"
)

// hash2hex returns the first 4 bytes of SHA256(name) as hex.
// This mirrors chromem-go's internal collection directory naming (collection.go:549).
func hash2hex(name string) string {
	hash := sha256.Sum256([]byte(name))
	return hex.EncodeToString(hash[:4])
}

// --- Tokenization ---

// stopWords is a minimal set of English stop words filtered during tokenization.
var stopWords = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "been": {}, "being": {}, "have": {}, "has": {}, "had": {},
	"do": {}, "does": {}, "did": {}, "will": {}, "would": {}, "could": {},
	"should": {}, "may": {}, "might": {}, "must": {}, "shall": {},
	"of": {}, "in": {}, "on": {}, "at": {}, "to": {}, "for": {}, "and": {},
	"or": {}, "not": {}, "no": {}, "with": {}, "by": {}, "as": {},
	"this": {}, "that": {}, "it": {}, "from": {}, "if": {},
}

// tokenize splits text into lowercase terms for BM25 indexing.
// ASCII words are split on non-alphanumeric characters.
// CJK characters are treated as individual tokens (character-level).
// Terms shorter than 2 characters and stop words are filtered out.
// Single CJK characters are kept (len==1 for runes != len<2 for bytes).
func tokenize(text string) []string {
	text = strings.ToLower(text)
	var tokens []string

	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			tokens = append(tokens, string(r))
		}
	}

	// Split ASCII tokens on non-alphanumeric.
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			t := buf.String()
			if !isStopWord(t) && len(t) >= 2 {
				tokens = append(tokens, t)
			}
			buf.Reset()
		}
	}
	for _, r := range text {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			buf.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()

	// Dedup while preserving order.
	seen := make(map[string]struct{}, len(tokens))
	result := make([]string, 0, len(tokens))
	for _, t := range tokens {
		if _, ok := seen[t]; ok {
			continue
		}
		seen[t] = struct{}{}
		result = append(result, t)
	}
	return result
}

func isStopWord(w string) bool {
	_, ok := stopWords[w]
	return ok
}

// --- BM25 Index ---

// bm25Doc holds the preprocessed data for a single document.
type bm25Doc struct {
	ID        string
	Content   string
	CreatedAt time.Time
	terms     []string // tokenized content
}

// termFreq counts term occurrences in a document's terms slice.
func (d *bm25Doc) termFreq(term string) int {
	count := 0
	for _, t := range d.terms {
		if t == term {
			count++
		}
	}
	return count
}

// docLen returns the number of tokens in the document.
func (d *bm25Doc) docLen() int { return len(d.terms) }

// BM25Index is an in-memory BM25 index over archival documents.
// It is loaded from chromem-go's gob files (single source of truth).
type BM25Index struct {
	mu   sync.RWMutex
	docs map[string]*bm25Doc // id → doc
	k1   float64
	b    float64
}

// NewBM25Index creates an empty BM25 index with standard parameters.
func NewBM25Index() *BM25Index {
	return &BM25Index{
		docs: make(map[string]*bm25Doc),
		k1:   1.2,
		b:    0.75,
	}
}

// Add inserts or updates a document in the index.
func (idx *BM25Index) Add(id, content string, ts time.Time) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	idx.docs[id] = &bm25Doc{
		ID:        id,
		Content:   content,
		CreatedAt: ts,
		terms:     tokenize(content),
	}
}

// Remove deletes a document from the index by ID.
func (idx *BM25Index) Remove(id string) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.docs, id)
}

// Count returns the number of indexed documents.
func (idx *BM25Index) Count() int {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return len(idx.docs)
}

// bm25Result holds a single search result with its BM25 score.
type bm25Result struct {
	ID    string
	Score float64
}

// Search runs a BM25 query and returns the top-N results by score.
// Returns empty if the query has no matching terms or the index is empty.
func (idx *BM25Index) Search(query string, limit int) []bm25Result {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if len(idx.docs) == 0 || limit <= 0 {
		return nil
	}

	queryTerms := tokenize(query)
	if len(queryTerms) == 0 {
		return nil
	}

	// Precompute corpus statistics.
	N := len(idx.docs)
	totalLen := 0
	df := make(map[string]int) // document frequency per term
	for _, doc := range idx.docs {
		totalLen += doc.docLen()
		seen := make(map[string]bool)
		for _, t := range doc.terms {
			if !seen[t] {
				df[t]++
				seen[t] = true
			}
		}
	}
	avgdl := 0.0
	if N > 0 {
		avgdl = float64(totalLen) / float64(N)
	}

	// Score each document.
	type scored struct {
		id    string
		score float64
	}
	var results []scored
	for _, doc := range idx.docs {
		var score float64
		for _, term := range queryTerms {
			tf := float64(doc.termFreq(term))
			if tf == 0 {
				continue
			}
			n := float64(df[term])
			idf := math.Log(1 + (float64(N)-n+0.5)/(n+0.5))
			dl := float64(doc.docLen())
			denom := tf + idx.k1*(1-idx.b+idx.b*dl/avgdl)
			score += idf * tf * (idx.k1 + 1) / denom
		}
		if score > 0 {
			results = append(results, scored{id: doc.ID, score: score})
		}
	}

	// Sort by score descending.
	sort.Slice(results, func(i, j int) bool {
		return results[i].score > results[j].score
	})

	if len(results) > limit {
		results = results[:limit]
	}

	out := make([]bm25Result, len(results))
	for i, r := range results {
		out[i] = bm25Result{ID: r.id, Score: r.score}
	}
	return out
}

// LoadFromDir loads all documents from a chromem-go collection directory.
// It reads each *.gob file (excluding the metadata file "00000000.gob"),
// decodes the chromem.Document, and adds it to the index.
// Files that fail to decode are skipped with a warning.
func (idx *BM25Index) LoadFromDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no directory = empty index
		}
		return fmt.Errorf("read dir %s: %w", dir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "00000000.gob" {
			continue // collection metadata, skip
		}
		if !strings.HasSuffix(name, ".gob") {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			log.WithError(err).WithField("file", path).Warn("BM25: failed to read gob file, skipping")
			continue
		}

		var doc chromem.Document
		if err := gob.NewDecoder(strings.NewReader(string(data))).Decode(&doc); err != nil {
			log.WithError(err).WithField("file", path).Warn("BM25: failed to decode gob file, skipping")
			continue
		}

		if doc.ID == "" || doc.Content == "" {
			continue
		}

		var ts time.Time
		if tsStr, ok := doc.Metadata["created_at"]; ok {
			if parsed, err := time.Parse(time.RFC3339, tsStr); err == nil {
				ts = parsed
			}
		}

		idx.Add(doc.ID, doc.Content, ts)
	}

	log.WithField("doc_count", idx.Count()).Info("BM25 index loaded from disk")
	return nil
}

// --- BM25Retriever ---

// BM25Retriever implements the Retriever interface using BM25 keyword search.
// It returns empty results when Keyword is empty (BM25 is opt-in).
type BM25Retriever struct {
	svc *ArchivalService
}

// Name returns the retriever identifier.
func (r *BM25Retriever) Name() string { return "bm25" }

// Search runs BM25 on the tenant's index. Returns empty when Keyword is empty.
func (r *BM25Retriever) Search(ctx context.Context, tenantID int64, req SearchRequest) ([]ArchivalEntry, error) {
	if req.Keyword == "" {
		return nil, nil
	}

	idx, err := r.svc.getOrCreateBM25Index(tenantID)
	if err != nil || idx == nil || idx.Count() == 0 {
		return nil, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}

	results := idx.Search(req.Keyword, limit)
	if len(results) == 0 {
		return nil, nil
	}

	// Convert BM25 results to ArchivalEntry by looking up content from the index.
	entries := make([]ArchivalEntry, 0, len(results))
	for _, res := range results {
		doc := idx.getDoc(res.ID)
		if doc == nil {
			continue
		}
		entries = append(entries, ArchivalEntry{
			ID:         res.ID,
			TenantID:   tenantID,
			Content:   doc.Content,
			CreatedAt:  doc.CreatedAt,
			Similarity: float32(res.Score),
		})
	}
	return entries, nil
}

// getDoc returns the document by ID (thread-safe read).
func (idx *BM25Index) getDoc(id string) *bm25Doc {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	return idx.docs[id]
}
