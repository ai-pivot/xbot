package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// buildContextFile writes an AGENTS.md with at least n CHARACTERS (CJK + ASCII
// mix) and returns (dir, tail sentinel). The sentinel is the last line; it must
// never reach the system prompt once the budget is enforced.
//
// Regression background (2026-09-23 incident): formatProjectContext injected the
// project context file IN FULL. xbot's own AGENTS.md is 690 KB / 500k chars
// (≈180k tokens: CJK ≈1 token/char, ASCII ≈0.25 token/char), so the system
// prompt ALONE nearly filled a 200k context window. Compression only rewrites
// conversation messages — never the system prompt — so every compaction was
// "ineffective" (the un-shrinkable part was over the line by itself) and the
// session looped: auto-compaction fired every 5 iterations forever while the
// model, fed a pathological prompt, repeated tool calls for hours.
func buildContextFile(t *testing.T, chars int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	const sentinel = "SENTINEL-TAIL-MUST-NOT-BE-INJECTED"

	// Two lines ≈ 78 characters (mixed CJK 3-byte and ASCII 1-byte).
	const block = "这是一条很长的项目指令，包含中文与 ASCII 混排内容，用于撑大上下文。\n" +
		"long project instruction line with ascii payload\n"

	var b strings.Builder
	for utf8.RuneCountInString(b.String()) < chars {
		b.WriteString(block)
	}
	b.WriteString(sentinel + "\n")

	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write AGENTS.md: %v", err)
	}
	return dir, sentinel
}

// TestLoadProjectContextFile_BoundsHugeFile is the incident reproduction: the
// formatted project context must stay within the documented budget
// (maxProjectContextChars CHARACTERS) and point the model at the Read tool.
func TestLoadProjectContextFile_BoundsHugeFile(t *testing.T) {
	dir, sentinel := buildContextFile(t, 500_000) // ≈ xbot's own AGENTS.md

	got := LoadProjectContextFile(dir)
	if got == "" {
		t.Fatal("LoadProjectContextFile returned empty for a huge AGENTS.md")
	}

	// Budget is in CHARACTERS (runes) — never bytes: a CJK char is 3 UTF-8 bytes.
	if n := utf8.RuneCountInString(got); n > maxProjectContextChars+4096 {
		t.Errorf("project context is unbounded: got %d chars, want <= %d (maxProjectContextChars=%d)",
			n, maxProjectContextChars+4096, maxProjectContextChars)
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
	dir, sentinel := buildContextFile(t, 500_000)

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
	if n := utf8.RuneCountInString(got); n > maxProjectContextChars+4096 {
		t.Errorf("05_project_context is unbounded: got %d chars, want <= %d",
			n, maxProjectContextChars+4096)
	}
	if strings.Contains(got, sentinel) {
		t.Error("tail sentinel leaked into SystemParts — the whole file was injected")
	}
}

// TestFormatProjectContext_BudgetIsGenerousButBounded pins BOTH halves of the
// contract: a file below the budget is injected in FULL (the budget must not be
// conservative — model contexts are ≥200k tokens), and a file above it is cut.
func TestFormatProjectContext_BudgetIsGenerousButBounded(t *testing.T) {
	t.Run("under_budget_is_injected_in_full", func(t *testing.T) {
		dir, sentinel := buildContextFile(t, maxProjectContextChars/2) // 50k chars
		content, _ := NewProjectContextMiddleware().load(dir)

		got := formatProjectContext(content, "AGENTS.md")
		if strings.Contains(got, "truncated") {
			t.Error("a file below the budget must NOT be truncated")
		}
		if !strings.Contains(got, sentinel) {
			t.Error("a file below the budget must be injected in full (tail sentinel missing)")
		}
	})

	t.Run("over_budget_is_cut", func(t *testing.T) {
		dir, sentinel := buildContextFile(t, maxProjectContextChars*2)
		content, _ := NewProjectContextMiddleware().load(dir)

		got := formatProjectContext(content, "AGENTS.md")
		if !strings.Contains(got, "truncated") {
			t.Error("a file above the budget must be truncated")
		}
		if strings.Contains(got, sentinel) {
			t.Error("tail sentinel leaked — file above the budget was injected in full")
		}
		if n := utf8.RuneCountInString(got); n > maxProjectContextChars+4096 {
			t.Errorf("formatted context = %d chars, want <= %d", n, maxProjectContextChars+4096)
		}
	})
}

// TestProjectContext_SystemPromptStaysWithinBudget asserts the assembled system
// prompt for a huge AGENTS.md stays bounded — the incident was a prompt whose
// system part alone nearly filled the whole context window.
func TestProjectContext_SystemPromptStaysWithinBudget(t *testing.T) {
	dir, _ := buildContextFile(t, 500_000)

	m := NewProjectContextMiddleware()
	mc := newMC()
	mc.CWD = dir
	mc.UserContent = "hi"
	if err := m.Process(mc); err != nil {
		t.Fatalf("Process() error: %v", err)
	}
	prompt := mc.BuildSystemPrompt()

	// 500k chars of AGENTS.md ≈ 180k tokens. Budget: the cap plus bounded
	// wrapper text — anything beyond that is a system-prompt blowup.
	const wrapperBudget = 8192
	if n := utf8.RuneCountInString(prompt); n > maxProjectContextChars+wrapperBudget {
		t.Errorf("assembled system prompt is %d chars, want <= %d — the project context file is unbounded",
			n, maxProjectContextChars+wrapperBudget)
	}
}
