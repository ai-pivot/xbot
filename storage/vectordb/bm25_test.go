package vectordb

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

func encodeGob(doc chromem.Document) ([]byte, error) {
	var buf bytes.Buffer
	err := gob.NewEncoder(&buf).Encode(doc)
	return buf.Bytes(), err
}

func TestTokenize_English(t *testing.T) {
	tokens := tokenize("LoRA deployment on gpu3")
	want := map[string]bool{"lora": true, "deployment": true, "gpu3": true}
	for _, tok := range tokens {
		if !want[tok] {
			t.Errorf("unexpected token: %s", tok)
		}
	}
	// "on" is a stop word, should be filtered
	for _, tok := range tokens {
		if tok == "on" {
			t.Error("stop word 'on' should be filtered")
		}
	}
}

func TestTokenize_CodeIdentifiers(t *testing.T) {
	tokens := tokenize("dcp/comm.py GLM-5.2-FP8")
	want := map[string]bool{"dcp": true, "comm": true, "py": true, "glm": true, "fp8": true}
	for _, tok := range tokens {
		if !want[tok] && tok != "5" && tok != "2" {
			// "5" and "2" are length-1 tokens from "5.2", filtered out
			if len(tok) >= 2 && !want[tok] {
				t.Errorf("unexpected token: %s", tok)
			}
		}
	}
	if !contains(tokens, "dcp") {
		t.Error("expected token 'dcp'")
	}
	if !contains(tokens, "fp8") {
		t.Error("expected token 'fp8'")
	}
}

func TestTokenize_CJK(t *testing.T) {
	tokens := tokenize("部署 LoRA")
	if !contains(tokens, "部") || !contains(tokens, "署") {
		t.Errorf("expected CJK characters 部 署, got %v", tokens)
	}
	if !contains(tokens, "lora") {
		t.Error("expected token 'lora'")
	}
}

func TestTokenize_Dedup(t *testing.T) {
	tokens := tokenize("lora lora lora")
	count := 0
	for _, t := range tokens {
		if t == "lora" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 'lora' after dedup, got %d", count)
	}
}

func TestTokenize_Empty(t *testing.T) {
	tokens := tokenize("")
	if len(tokens) != 0 {
		t.Errorf("expected empty tokens, got %v", tokens)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestBM25AddRemove(t *testing.T) {
	idx := NewBM25Index()
	if idx.Count() != 0 {
		t.Fatalf("expected 0 docs, got %d", idx.Count())
	}

	idx.Add("doc1", "ssh-xbot frpc configuration", time.Now())
	idx.Add("doc2", "LoRA deployment progress", time.Now())
	if idx.Count() != 2 {
		t.Fatalf("expected 2 docs, got %d", idx.Count())
	}

	idx.Remove("doc1")
	if idx.Count() != 1 {
		t.Fatalf("expected 1 doc after remove, got %d", idx.Count())
	}
}

func TestBM25Search(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "ssh-xbot frpc proxy configuration on ubuntu", time.Now())
	idx.Add("doc2", "LoRA deployment with EAGLE on MI300X", time.Now())
	idx.Add("doc3", "frpc tunnel setup for ssh-xbot", time.Now())

	// Search for "ssh-xbot" — should match doc1 and doc3
	results := idx.Search("ssh-xbot", 5)
	if len(results) == 0 {
		t.Fatal("expected results for 'ssh-xbot'")
	}
	// Both doc1 and doc3 contain "ssh-xbot"
	ids := make(map[string]bool)
	for _, r := range results {
		ids[r.ID] = true
	}
	if !ids["doc1"] || !ids["doc3"] {
		t.Errorf("expected doc1 and doc3, got %v", results)
	}
	// doc2 should not be in results (no "ssh-xbot" match)
	if ids["doc2"] {
		t.Error("doc2 should not match 'ssh-xbot'")
	}

	// Results should be sorted by score descending
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Error("results not sorted by score descending")
		}
	}
}

func TestBM25Search_EmptyQuery(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "some content", time.Now())
	results := idx.Search("", 5)
	if len(results) != 0 {
		t.Errorf("expected empty results for empty query, got %v", results)
	}
}

func TestBM25Search_NoMatch(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("doc1", "hello world", time.Now())
	results := idx.Search("nonexistent", 5)
	if len(results) != 0 {
		t.Errorf("expected empty results for no match, got %v", results)
	}
}

func TestBM25Search_Limit(t *testing.T) {
	idx := NewBM25Index()
	idx.Add("d1", "keyword keyword keyword", time.Now())
	idx.Add("d2", "keyword keyword", time.Now())
	idx.Add("d3", "keyword", time.Now())
	results := idx.Search("keyword", 2)
	if len(results) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(results))
	}
}

func TestBM25LoadFromDir(t *testing.T) {
	dir := t.TempDir()

	// Create chromem-go documents and persist as gob files.
	docs := []chromem.Document{
		{ID: "aaa", Content: "ssh-xbot frpc config", Metadata: map[string]string{"created_at": time.Now().Format(time.RFC3339)}},
		{ID: "bbb", Content: "LoRA deploy on MI300X", Metadata: map[string]string{"created_at": time.Now().Format(time.RFC3339)}},
		{ID: "ccc", Content: "ssh-xbot systemd service", Metadata: map[string]string{"created_at": time.Now().Format(time.RFC3339)}},
	}
	for _, doc := range docs {
		path := filepath.Join(dir, hash2hex(doc.ID)+".gob")
		data, err := encodeGob(doc)
		if err != nil {
			t.Fatalf("encode gob: %v", err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
	}

	idx := NewBM25Index()
	if err := idx.LoadFromDir(dir); err != nil {
		t.Fatalf("LoadFromDir: %v", err)
	}
	if idx.Count() != 3 {
		t.Fatalf("expected 3 docs, got %d", idx.Count())
	}

	// Verify search works
	results := idx.Search("ssh-xbot", 5)
	if len(results) != 2 {
		t.Fatalf("expected 2 results for 'ssh-xbot', got %d", len(results))
	}
}

func TestBM25LoadFromDir_NonexistentDir(t *testing.T) {
	idx := NewBM25Index()
	err := idx.LoadFromDir("/nonexistent/path/12345")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent dir, got %v", err)
	}
	if idx.Count() != 0 {
		t.Errorf("expected 0 docs, got %d", idx.Count())
	}
}
