package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/llm"
)

// TestExtractMaskOffloadIDs verifies that the ID extraction correctly finds
// all mask/offload references in messages — this is the core mechanism that
// ensures compressed views can still recall original data.
func TestExtractMaskOffloadIDs(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "user", Content: "check this 📂 [offload:ol_abc12345] file"},
		{Role: "assistant", Content: "let me read it"},
		{Role: "tool", Content: "📂 [offload:ol_abc12345] Read(foo.go)\nfile content"},
		{Role: "tool", Content: "📂 [masked:mk_def67890] Shell(go test)\ntest output"},
		{Role: "assistant", Content: "done with 📂 [offload:ol_1a2b3c4d]"},
	}

	ids := extractMaskOffloadIDs(messages)

	expected := map[string]bool{
		"ol_abc12345": true,
		"mk_def67890": true,
		"ol_1a2b3c4d": true,
	}

	if len(ids) != len(expected) {
		t.Fatalf("expected %d IDs, got %d: %v", len(expected), len(ids), ids)
	}

	for id := range expected {
		if !ids[id] {
			t.Errorf("expected ID %s to be found, but it was not", id)
		}
	}
}

// TestExtractMaskOffloadIDs_ToolCallArgs verifies that IDs in tool call
// arguments (e.g. offload_recall tool args) are also extracted.
func TestExtractMaskOffloadIDs_ToolCallArgs(t *testing.T) {
	messages := []llm.ChatMessage{
		{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{
				{ID: "1", Name: "offload_recall", Arguments: `{"id":"ol_a1b2c3d4"}`},
				{ID: "2", Name: "recall_masked", Arguments: `{"id":"mk_e5f6a7b8"}`},
			},
		},
	}

	ids := extractMaskOffloadIDs(messages)
	if !ids["ol_a1b2c3d4"] {
		t.Error("expected ol_a1b2c3d4 from tool call args")
	}
	if !ids["mk_e5f6a7b8"] {
		t.Error("expected mk_e5f6a7b8 from tool call args")
	}
}

// TestExtractMaskOffloadIDs_NoIDs verifies that messages without markers
// return an empty set.
func TestExtractMaskOffloadIDs_NoIDs(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
	}

	ids := extractMaskOffloadIDs(messages)
	if len(ids) != 0 {
		t.Fatalf("expected 0 IDs, got %d: %v", len(ids), ids)
	}
}

// TestExtractMaskOffloadIDs_CompactedSummary verifies that mask/offload
// references in a compaction summary (user message with "[Compacted context]")
// are correctly extracted — this is the key scenario where the V2 prompt
// preserves references in the summary.
func TestExtractMaskOffloadIDs_CompactedSummary(t *testing.T) {
	messages := []llm.ChatMessage{
		{
			Role: "user",
			Content: `[Compacted context]

### Active Files
- auth.go (📂 [offload:ol_a1b2c3d4])

### Errors & Fixes
- Build error: see 📂 [masked:mk_e5f6a7b8]

### Recent Work
Implemented JWT auth, see 📂 [offload:ol_9c0d1e2f] for the full file content.`,
		},
	}

	ids := extractMaskOffloadIDs(messages)
	expected := []string{"ol_a1b2c3d4", "mk_e5f6a7b8", "ol_9c0d1e2f"}
	for _, id := range expected {
		if !ids[id] {
			t.Errorf("expected ID %s in compacted summary, but it was not found", id)
		}
	}
}

// TestCleanUnreferencedEntries_OffloadStore verifies that only unreferenced
// offload entries are cleaned, while referenced ones are preserved.
func TestCleanUnreferencedEntries_OffloadStore(t *testing.T) {
	dir := t.TempDir()
	store := NewOffloadStore(OffloadConfig{
		StoreDir:        dir,
		MaxResultTokens: 100, // low threshold to force offload
		MaxResultBytes:  10240,
	})

	// Store entries using MaybeOffload (with large content to trigger offloading)
	largeContent := strings.Repeat("important file content ", 100)
	store.MaybeOffload(context.Background(), "test:session", "Read", `{"path":"auth.go"}`, largeContent, "", "", "")

	largeContent2 := strings.Repeat("another important file ", 100)
	store.MaybeOffload(context.Background(), "test:session", "Read", `{"path":"config.go"}`, largeContent2, "", "", "")

	oldContent := strings.Repeat("old data not needed ", 100)
	store.MaybeOffload(context.Background(), "test:session", "Shell", `{"command":"ls"}`, oldContent, "", "", "")

	oldContent2 := strings.Repeat("more old data ", 100)
	store.MaybeOffload(context.Background(), "test:session", "Grep", `{"pattern":"todo"}`, oldContent2, "", "", "")

	// Get all stored IDs
	idx := store.getOrCreateIndex("test:session")
	idx.mu.RLock()
	allIDs := make([]string, 0, len(idx.entries))
	for _, e := range idx.entries {
		allIDs = append(allIDs, e.ID)
	}
	idx.mu.RUnlock()

	if len(allIDs) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(allIDs))
	}

	// Clean only first 2 entries (mark them as referenced)
	referenced := map[string]bool{
		allIDs[0]: true,
		allIDs[1]: true,
	}
	removed := store.CleanUnreferencedEntries("test:session", referenced)

	if removed != 2 {
		t.Fatalf("expected 2 removed, got %d", removed)
	}

	// Verify referenced entries can still be recalled
	for _, id := range []string{allIDs[0], allIDs[1]} {
		content, err := store.Recall("test:session", id)
		if err != nil {
			t.Errorf("referenced entry %s should still be recallable: %v", id, err)
		}
		if content == "" {
			t.Errorf("referenced entry %s should have content", id)
		}
	}

	// Verify unreferenced entries are gone
	for _, id := range []string{allIDs[2], allIDs[3]} {
		_, err := store.Recall("test:session", id)
		if err == nil {
			t.Errorf("unreferenced entry %s should have been cleaned", id)
		}
	}
}

// TestCleanUnreferencedEntries_MaskStore verifies that only unreferenced
// mask entries are cleaned, while referenced ones are preserved.
func TestCleanUnreferencedEntries_MaskStore(t *testing.T) {
	store := NewObservationMaskStore(10000, t.TempDir())

	// Create mask entries using Mask()
	obs1, _ := store.Mask("Shell", `{"command":"go test"}`, "test output line 1\n"+strings.Repeat("x", 500), 0)
	obs2, _ := store.Mask("Read", `{"path":"config.go"}`, "file content\n"+strings.Repeat("y", 500), 1)
	obs3, _ := store.Mask("Grep", `{"pattern":"TODO"}`, "grep results\n"+strings.Repeat("z", 500), 2)

	if obs1.ID == "" || obs2.ID == "" || obs3.ID == "" {
		t.Fatal("expected non-empty mask IDs")
	}

	// Clean only unreferenced entries (keep obs1 and obs2, clean obs3)
	referenced := map[string]bool{
		obs1.ID: true,
		obs2.ID: true,
	}
	removed := store.CleanUnreferencedEntries(referenced)

	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}

	// Verify referenced entries can still be recalled
	for _, id := range []string{obs1.ID, obs2.ID} {
		obs, err := store.Recall(id)
		if err != nil {
			t.Errorf("referenced entry %s should still be recallable: %v", id, err)
		}
		if obs.Content == "" {
			t.Errorf("referenced entry %s should have content", id)
		}
	}

	// Verify unreferenced entry is gone (wait for async disk deletion)
	time.Sleep(200 * time.Millisecond)
	_, err := store.Recall(obs3.ID)
	if err == nil {
		t.Errorf("unreferenced entry %s should have been cleaned", obs3.ID)
	}
}
