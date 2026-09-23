package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// buildHugeContextFile writes an AGENTS.md far larger than the injection budget
// and returns (dir, tail sentinel). The sentinel is the LAST line of the file;
// it must never reach the system prompt once the budget is enforced.
//
// Regression background (2026-09-23 incident): formatProjectContext injected the
// project context file IN FULL. xbot's own AGENTS.md is ~689 KB (≈460k tokens),
// so the system prompt ALONE exceeded the 200k context budget. Compression only
// rewrites conversation messages — never the system prompt — so every compaction
// was "ineffective" (the un-shrinkable part was over the line by itself) and the
// session looped: auto-compaction fired every 5 iterations forever while the
// model, fed a pathological prompt, repeated tool calls for hours.
func buildHugeContextFile(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	const sentinel = "SENTINEL-TAIL-MUST-NOT-BE-INJECTED"

	// Mixed CJK + latin so the rune-safety assertion is meaningful: a byte-wise
	// cut would land mid-rune and produce invalid UTF-8 in the system prompt.
	var b strings.Builder
	for b.Len() < 700_000 {
		b.WriteString("这是一条很长的项目指令，包含中文与 ASCII 混排内容，用于撑大上下文。\n")
		b.WriteString("long project instruction line with ascii payload\n")
	}
	b.WriteString(sentinel + "\n")

	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write huge AGENTS.md: %v", err)
	}
	return dir, sentinel
}

// TestLoadProjectContextFile_BoundsHugeFile is the incident reproduction: the
// formatted project context must stay within the documented budget
// (maxProjectContextChars) and point the model at the Read tool for the rest.
func TestLoadProjectContextFile_BoundsHugeFile(t *testing.T) {
	dir, sentinel := buildHugeContextFile(t)

	got := LoadProjectContextFile(dir)
	if got == "" {
		t.Fatal("LoadProjectContextFile returned empty for a huge AGENTS.md")
	}

	// Budget: the documented cap plus the (bounded, static) wrapper text.
	const wrapperOverhead = 4096
	if len(got) > maxProjectContextChars+wrapperOverhead {
		t.Errorf("project context is unbounded: got %d bytes, want <= %d (maxProjectContextChars=%d)",
			len(got), maxProjectContextChars+wrapperOverhead, maxProjectContextChars)
	}
	if strings.Contains(got, sentinel) {
		t.Error("tail sentinel leaked into the system prompt — the whole file was injected")
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, "Read") {
		t.Error("truncated context must carry a 'use the Read tool for the full file' hint")
	}
	// CDATA / XML contract must survive truncation.
	if !strings.Contains(got, "<![CDATA[") || !strings.Contains(got, "]]>") {
		t.Error("CDATA wrapper must stay intact after truncation")
	}
	// Rune safety: a byte-wise cut of CJK content would emit invalid UTF-8.
	if !utf8.ValidString(got) {
		t.Error("truncated project context is not valid UTF-8 (byte-wise cut mid-rune)")
	}
}

// TestProjectContextMiddleware_BoundsHugeFile pins the same contract on the
// middleware path (the one the main Agent actually runs).
func TestProjectContextMiddleware_BoundsHugeFile(t *testing.T) {
	dir, sentinel := buildHugeContextFile(t)

	m := NewProjectContextMiddleware()
	mc := newMC()
	mc.CWD = dir
	if err := m.Process(mc); err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	got := mc.SystemParts["05_project_context"]
	if got == "" {
		t.Fatal("expected 05_project_context to be set")
	}
	const wrapperOverhead = 4096
	if len(got) > maxProjectContextChars+wrapperOverhead {
		t.Errorf("05_project_context is unbounded: got %d bytes, want <= %d",
			len(got), maxProjectContextChars+wrapperOverhead)
	}
	if strings.Contains(got, sentinel) {
		t.Error("tail sentinel leaked into SystemParts — the whole file was injected")
	}
}

// TestProjectContext_SystemPromptStaysWithinBudget asserts the assembled system
// prompt for a huge AGENTS.md stays far below any realistic context window —
// the incident was a >460k-token system prompt.
func TestProjectContext_SystemPromptStaysWithinBudget(t *testing.T) {
	dir, _ := buildHugeContextFile(t)

	m := NewProjectContextMiddleware()
	mc := newMC()
	mc.CWD = dir
	mc.UserContent = "hi"
	if err := m.Process(mc); err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	prompt := mc.BuildSystemPrompt()

	// 689 KB of AGENTS.md ≈ 460k tokens at ~1.5 chars/token for mixed CJK.
	// Anything above ~64 KB of system prompt is already pathological.
	const budget = 64 * 1024
	if len(prompt) > budget {
		t.Errorf("assembled system prompt is %d bytes, want <= %d — the project context file is unbounded",
			len(prompt), budget)
	}
}
