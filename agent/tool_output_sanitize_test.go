package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"xbot/llm"
	"xbot/tools"
)

// TestUpdateToolResultLineSanitizesToolOutput pins the agent boundary: tool
// output is arbitrary bytes (Shell `cat` on an ELF, MCP servers, hook scripts),
// and PostgreSQL text cannot store NUL — SQLSTATE 22021
// ("invalid byte sequence for encoding UTF8": 0x00) aborted the whole INSERT,
// losing the turn's persistence. The tool message, its content and the
// offloaded/raw summary must all be valid UTF-8 without NUL.
func TestUpdateToolResultLineSanitizesToolOutput(t *testing.T) {
	s := &runState{}
	batch := &toolExecBatch{results: make([]toolExecResult, 1)}
	entry := toolCallEntry{index: 0}
	tc := llm.ToolCall{Name: "Shell", Arguments: `{"command":"cat payload.bin"}`}
	res := &tools.ToolResult{
		Summary: "ELF header\x00\x00 then \xff\xfe bytes",
		Tips:    "hint text",
	}

	s.updateToolResultLine(context.Background(), entry, batch, tc, res, nil, time.Millisecond)

	got := batch.results[0].llmContent
	assertTextSafe(t, "llmContent", got)
	assertTextSafe(t, "content", batch.results[0].content)
	assertTextSafe(t, "result.Summary", res.Summary)
	if !strings.Contains(got, "ELF header") || !strings.Contains(got, "hint text") {
		t.Errorf("sanitisation dropped payload/tips: %q", got)
	}
}

// TestUpdateToolResultLineSanitizesExecutionError covers the failure path: the
// wrapped exec error can embed raw tool output (sandbox stderr, command bytes).
func TestUpdateToolResultLineSanitizesExecutionError(t *testing.T) {
	s := &runState{}
	batch := &toolExecBatch{results: make([]toolExecResult, 1)}
	entry := toolCallEntry{index: 0}
	tc := llm.ToolCall{Name: "Shell", Arguments: `{"command":"cat payload.bin"}`}

	s.updateToolResultLine(context.Background(), entry, batch, tc, nil,
		fmt.Errorf("sandbox exec failed: output\x00\xff"), time.Millisecond)

	assertTextSafe(t, "error llmContent", batch.results[0].llmContent)
	assertTextSafe(t, "error content", batch.results[0].content)
}

// TestNewSyntheticToolPairSanitizesContent covers injected (synthetic) tool
// results — background-task / subagent completions carry the same arbitrary
// bytes and take a different construction path.
func TestNewSyntheticToolPairSanitizesContent(t *testing.T) {
	_, toolMsg := newSyntheticToolPair("bg_subagent_completed", "bgsub_1", "done\x00\xff")

	assertTextSafe(t, "synthetic tool content", toolMsg.Content)
	if !strings.HasPrefix(toolMsg.Content, "done") {
		t.Errorf("payload lost: %q", toolMsg.Content)
	}
}

func assertTextSafe(t *testing.T, label, s string) {
	t.Helper()
	if strings.ContainsRune(s, 0) {
		t.Errorf("%s still contains NUL: %q", label, s)
	}
	if !utf8.ValidString(s) {
		t.Errorf("%s is not valid UTF-8: %q", label, s)
	}
}
