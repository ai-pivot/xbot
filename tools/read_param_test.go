package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadToolFilePathAlias verifies the file_path → path parameter fallback:
// some models trained on OpenAI function-calling conventions pass
// {"file_path": "..."} instead of the canonical {"path": "..."} — the
// fallback maps it so the call works instead of failing with "path is required".
func TestReadToolFilePathAlias(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello file_path alias"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "canonical.txt"), []byte("canonical path param"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := &ReadTool{}
	ctx := &ToolContext{WorkingDir: dir}

	// 1. file_path only (OpenAI-style) → reads the file via the fallback.
	result, err := tool.Execute(ctx, `{"file_path": "hello.txt"}`)
	if err != nil {
		t.Fatalf("Execute with file_path alias should fall back to path: %v", err)
	}
	if !strings.Contains(result.Summary, "hello file_path alias") {
		t.Errorf("file_path alias should read hello.txt, got: %s", result.Summary)
	}

	// 2. path (canonical) still works unchanged.
	result, err = tool.Execute(ctx, `{"path": "canonical.txt"}`)
	if err != nil {
		t.Fatalf("Execute with canonical path should work: %v", err)
	}
	if !strings.Contains(result.Summary, "canonical path param") {
		t.Errorf("canonical path should read canonical.txt, got: %s", result.Summary)
	}

	// 3. Both present → path wins (canonical parameter takes precedence).
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("from path"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("from file_path"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err = tool.Execute(ctx, `{"path": "a.txt", "file_path": "b.txt"}`)
	if err != nil {
		t.Fatalf("Execute with both params: %v", err)
	}
	if !strings.Contains(result.Summary, "from path") {
		t.Errorf("path must win over file_path when both present, got: %s", result.Summary)
	}

	// 4. Neither present → still errors with "path is required".
	_, err = tool.Execute(ctx, `{"max_lines": 10}`)
	if err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("missing both params should error 'path is required', got: %v", err)
	}
}
