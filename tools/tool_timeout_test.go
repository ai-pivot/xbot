package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- Grep context cancellation ---

func TestGrepTool_ContextCancellation(t *testing.T) {
	tmpDir := setupGrepTestDir(t)

	// Pre-cancel the context so WalkDir returns SkipAll immediately
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	toolCtx := &ToolContext{
		Ctx:           ctx,
		WorkspaceRoot: tmpDir,
	}
	tool := &GrepTool{}
	input, _ := json.Marshal(map[string]any{
		"pattern": "func",
		"path":    tmpDir,
	})

	result, err := tool.Execute(toolCtx, string(input))
	if err != nil {
		t.Fatalf("cancelled search should not return error: %v", err)
	}
	// With pre-cancelled context, WalkDir skips all files → 0 matches
	if !strings.Contains(result.Summary, "No matches found") {
		t.Errorf("expected 'No matches found' for cancelled search, got: %s", result.Summary)
	}
}

// --- Glob context cancellation ---

func TestGlobTool_ContextCancellation(t *testing.T) {
	tmpDir := setupGlobTestDir(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	toolCtx := &ToolContext{
		Ctx:           ctx,
		WorkspaceRoot: tmpDir,
	}
	tool := &GlobTool{}
	// Use ** pattern to hit globWithDoublestar which checks context
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.go",
		"path":    tmpDir,
	})

	result, err := tool.Execute(toolCtx, string(input))
	if err != nil {
		t.Fatalf("cancelled glob should not return error: %v", err)
	}
	// With pre-cancelled context, WalkDir skips immediately → 0 matches
	if !strings.Contains(result.Summary, "No files matched") {
		t.Errorf("expected 'No files matched' for cancelled glob, got: %s", result.Summary)
	}
}

// --- Glob early truncation ---

func TestGlobTool_EarlyTruncation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create more files than MaxGlobResults
	fileCount := MaxGlobResults + 50
	for i := 0; i < fileCount; i++ {
		name := fmt.Sprintf("file_%04d.txt", i)
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte("x"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	toolCtx := &ToolContext{
		Ctx:           context.Background(),
		WorkspaceRoot: tmpDir,
	}
	tool := &GlobTool{}
	// Use ** pattern to hit globWithDoublestar which has early truncation
	input, _ := json.Marshal(map[string]string{
		"pattern": "**/*.txt",
		"path":    tmpDir,
	})

	result, err := tool.Execute(toolCtx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Count actual file lines (exclude header and truncated message)
	lines := strings.Split(strings.TrimSpace(result.Summary), "\n")
	fileLines := 0
	for _, l := range lines {
		if strings.Contains(l, "file_") {
			fileLines++
		}
	}

	if fileLines != MaxGlobResults {
		t.Errorf("expected %d results, got %d", MaxGlobResults, fileLines)
	}
}

// --- Read file size limit ---

func TestReadTool_FileSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "large.txt")

	// Create a file larger than MaxReadFileSize
	if err := os.WriteFile(largeFile, make([]byte, MaxReadFileSize+1), 0644); err != nil {
		t.Fatal(err)
	}

	toolCtx := &ToolContext{
		Ctx:           context.Background(),
		WorkspaceRoot: tmpDir,
	}
	tool := &ReadTool{}
	input, _ := json.Marshal(map[string]string{"path": largeFile})
	result, err := tool.Execute(toolCtx, string(input))

	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
	_ = result
}

// --- Read context cancellation ---

func TestReadTool_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := os.WriteFile(testFile, []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	toolCtx := &ToolContext{
		Ctx:           ctx,
		WorkspaceRoot: tmpDir,
	}
	tool := &ReadTool{}
	input, _ := json.Marshal(map[string]string{"path": testFile})
	result, err := tool.Execute(toolCtx, string(input))

	if err == nil {
		t.Fatal("expected error for cancelled read, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") && !strings.Contains(err.Error(), "cancelled") {
		t.Errorf("expected timeout/cancel error, got: %v", err)
	}
	_ = result
}

// --- FileReplace file size limit ---

func TestFileReplaceTool_FileSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "large.txt")
	if err := os.WriteFile(largeFile, make([]byte, MaxEditFileSize+1), 0644); err != nil {
		t.Fatal(err)
	}

	toolCtx := &ToolContext{
		Ctx:           context.Background(),
		WorkspaceRoot: tmpDir,
	}
	tool := &FileReplaceTool{}
	input, _ := json.Marshal(map[string]string{
		"path":       largeFile,
		"old_string": "a",
		"new_string": "b",
	})
	_, err := tool.Execute(toolCtx, string(input))

	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
}

// --- FileCreate rewrite file size limit ---

func TestFileCreateTool_RewriteFileSizeLimit(t *testing.T) {
	tmpDir := t.TempDir()
	largeFile := filepath.Join(tmpDir, "existing.txt")
	if err := os.WriteFile(largeFile, make([]byte, MaxEditFileSize+1), 0644); err != nil {
		t.Fatal(err)
	}

	toolCtx := &ToolContext{
		Ctx:           context.Background(),
		WorkspaceRoot: tmpDir,
	}
	tool := &FileCreateTool{}
	input, _ := json.Marshal(map[string]any{
		"path":     largeFile,
		"content":  "new",
		"rewrite":  true,
	})
	_, err := tool.Execute(toolCtx, string(input))

	if err == nil {
		t.Fatal("expected error for rewrite of oversized file, got nil")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected 'too large' error, got: %v", err)
	}
}

// --- searchFile context cancellation (unit test for the function) ---

func TestSearchFile_ContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.go")
	// Create a file with enough lines to trigger the periodic check
	var content strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&content, "line %d\n", i)
	}
	if err := os.WriteFile(testFile, []byte(content.String()), 0644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	re := regexp.MustCompile("line")
	matches, err := searchFile(ctx, testFile, re, 0)
	if err == nil {
		t.Fatalf("expected error for cancelled searchFile, got %d matches", len(matches))
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}
