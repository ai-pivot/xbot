package pg

import (
	"testing"
	"unicode/utf8"

	"xbot/storage/sqlite"
)

// TestCachedModelsJSON pins the encoding helper shared by the subscription write
// paths. It must never yield the literal "null" (a nil slice marshals to that)
// and never an empty JSON string, because the column is NOT NULL and readers
// treat "" as "no cache".
func TestCachedModelsJSON(t *testing.T) {
	if got := cachedModelsJSON(&sqlite.LLMSubscription{}); got != "" {
		t.Errorf("nil CachedModels -> %q, want empty string", got)
	}
	if got := cachedModelsJSON(&sqlite.LLMSubscription{CachedModels: []string{}}); got != "" {
		t.Errorf("empty CachedModels -> %q, want empty string", got)
	}
	got := cachedModelsJSON(&sqlite.LLMSubscription{CachedModels: []string{"gpt-4o", "o3"}})
	if got != `["gpt-4o","o3"]` {
		t.Errorf("CachedModels -> %q, want JSON array", got)
	}
	if !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8: %q", got)
	}
}
