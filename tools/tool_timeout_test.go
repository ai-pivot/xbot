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
	if err == nil {
		t.Fatal("cancelled search should return error, not success")
	}
	// With pre-cancelled context, should return timeout/cancel error
	if !strings.Contains(err.Error(), "interrupted") && !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'interrupted/timed out' error for cancelled search, got: %v", err)
	}
	_ = result
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
	if err == nil {
		t.Fatal("cancelled glob should return error, not success")
	}
	// With pre-cancelled context, should return timeout/cancel error
	if !strings.Contains(err.Error(), "interrupted") && !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'interrupted/timed out' error for cancelled glob, got: %v", err)
	}
	_ = result
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
		"path":    largeFile,
		"content": "new",
		"rewrite": true,
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

// --- Grep partial results on cancellation ---

func TestGrepTool_PartialResultsOnCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	// Create multiple files with matches
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("file_%d.go", i)
		content := fmt.Sprintf("package main\n\nfunc hello%d() {}\n", i)
		if err := os.WriteFile(filepath.Join(tmpDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create a context that's already cancelled — WalkDir may collect
	// 0 matches before hitting the ctx check (SkipAll), but the key
	// assertion is: we get an error, not "No matches found" success.
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

	_, err := tool.Execute(toolCtx, string(input))
	if err == nil {
		t.Fatal("expected error for cancelled search with matches in dir")
	}
	if !strings.Contains(err.Error(), "interrupted") && !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected interrupted/timed out error, got: %v", err)
	}
}
