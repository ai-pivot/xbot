package flat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupTestDir(t *testing.T) (string, int64) {
	t.Helper()
	dir := t.TempDir()
	memDir := filepath.Join(dir, "memory")
	os.MkdirAll(memDir, 0o755)
	return memDir, 42
}

func TestFlatMemory_Recall_Empty(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	result, err := m.Recall(context.Background(), "any query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result != "" {
		t.Errorf("Expected empty, got %q", result)
	}
}

func TestFlatMemory_Recall_WithContent(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	// Write MEMORY.md directly
	memoryPath := filepath.Join(memDir, memoryFileName)
	content := "# Facts\n- User likes Go\n- Prefers concise code"
	if err := os.WriteFile(memoryPath, []byte(content), 0o644); err != nil {
		t.Fatalf("Failed to write MEMORY.md: %v", err)
	}

	result, err := m.Recall(context.Background(), "ignored query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result == "" {
		t.Fatal("Expected non-empty result")
	}
	if !strings.Contains(result, "Core Memory") {
		t.Errorf("Expected 'Core Memory' header, got: %s", result)
	}
	if !strings.Contains(result, "User likes Go") {
		t.Errorf("Expected memory content, got: %s", result)
	}
}

func TestFlatMemory_Recall_Truncation(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	// Write MEMORY.md that exceeds maxMemoryChars
	longContent := strings.Repeat("x", 1500)
	memoryPath := filepath.Join(memDir, memoryFileName)
	if err := os.WriteFile(memoryPath, []byte(longContent), 0o644); err != nil {
		t.Fatalf("Failed to write MEMORY.md: %v", err)
	}

	result, err := m.Recall(context.Background(), "any query")
	if err != nil {
		t.Fatalf("Recall failed: %v", err)
	}
	if result == "" {
		t.Fatal("Expected non-empty result")
	}
	if !strings.Contains(result, "truncated") {
		t.Errorf("Expected truncation hint, got: %s", result)
	}
	// Should be approximately maxMemoryChars + header
	if len(result) > 1200 {
		t.Errorf("Result too long, expected truncation: got %d chars", len(result))
	}
}

func TestFlatMemory_Close(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	if err := m.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}
}

func TestFlatMemory_BaseDir(t *testing.T) {
	memDir, tenantID := setupTestDir(t)
	m := New(tenantID, memDir)

	if m.BaseDir() != memDir {
		t.Errorf("Expected %q, got %q", memDir, m.BaseDir())
	}
}
