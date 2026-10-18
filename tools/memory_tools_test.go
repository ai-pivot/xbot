package tools

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"xbot/storage/vectordb"
)

// mockRecallTimeRangeFunc returns a testable RecallTimeRangeFunc
// that records the arguments and returns specified entries.
func mockRecallTimeRangeFunc(entries []vectordb.RecallEntry) vectordb.RecallTimeRangeFunc {
	return func(tenantID int64, start, end time.Time, limit int) ([]vectordb.RecallEntry, error) {
		// Simple filtering for tests
		var results []vectordb.RecallEntry
		for _, e := range entries {
			matchStart := start.IsZero() || !e.CreatedAt.Before(start)
			matchEnd := end.IsZero() || !e.CreatedAt.After(end)
			if matchStart && matchEnd {
				results = append(results, e)
			}
			if len(results) >= limit && limit > 0 {
				break
			}
		}
		return results, nil
	}
}

func TestRecallMemorySearchTool_Name(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	if tool.Name() != "recall_memory_search" {
		t.Errorf("expected name 'recall_memory_search', got '%s'", tool.Name())
	}
}

func TestRecallMemorySearchTool_Parameters(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	params := tool.Parameters()

	names := make(map[string]bool)
	for _, p := range params {
		names[p.Name] = true
	}
	for _, expected := range []string{"start_date", "end_date", "limit"} {
		if !names[expected] {
			t.Errorf("missing parameter: %s", expected)
		}
	}
	if names["query"] {
		t.Error("recall_memory_search should NOT have a query parameter")
	}
}

func TestRecallMemorySearchTool_NoParams(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(nil),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "At least one") {
		t.Errorf("expected validation error, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_NotAvailable(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: nil, // not available
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{StartDate: "2026-03-01", EndDate: "2026-03-07"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "not available") {
		t.Errorf("expected 'not available' message, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_DateRange(t *testing.T) {
	entries := []vectordb.RecallEntry{
		{Entry: "Discussed Go generics", CreatedAt: time.Date(2026, 3, 1, 10, 0, 0, 0, time.Local)},
		{Entry: "Go error handling patterns", CreatedAt: time.Date(2026, 3, 2, 14, 0, 0, 0, time.Local)},
		{Entry: "Rust ownership model", CreatedAt: time.Date(2026, 3, 3, 9, 0, 0, 0, time.Local)},
	}

	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(entries),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{StartDate: "2026-03-01", EndDate: "2026-03-02"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Go generics") {
		t.Errorf("expected 'Go generics' in results, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "Go error handling") {
		t.Errorf("expected 'Go error handling' in results, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_DateRangeFilter(t *testing.T) {
	entries := []vectordb.RecallEntry{
		{Entry: "Old message", CreatedAt: time.Date(2026, 2, 28, 10, 0, 0, 0, time.Local)},
		{Entry: "Match message", CreatedAt: time.Date(2026, 3, 1, 14, 0, 0, 0, time.Local)},
		{Entry: "Future message", CreatedAt: time.Date(2026, 3, 5, 9, 0, 0, 0, time.Local)},
	}

	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(entries),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{
		StartDate: "2026-03-01",
		EndDate:   "2026-03-02",
	})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Match message") {
		t.Errorf("expected 'Match message' in results, got: %s", result.Summary)
	}
	if strings.Contains(result.Summary, "Old message") || strings.Contains(result.Summary, "Future message") {
		t.Errorf("should not contain out-of-range messages, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_InvalidDateFormat(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(nil),
		TenantID:        1,
	}

	// Bad start_date
	input, _ := json.Marshal(recallSearchArgs{StartDate: "03-01-2026"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Invalid start_date") {
		t.Errorf("expected date format error, got: %s", result.Summary)
	}

	// Bad end_date
	input, _ = json.Marshal(recallSearchArgs{EndDate: "not-a-date"})
	result, err = tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "Invalid end_date") {
		t.Errorf("expected date format error, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_NoResults(t *testing.T) {
	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(nil),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{StartDate: "2026-03-01", EndDate: "2026-03-07"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Summary, "No conversation history") {
		t.Errorf("expected no results message, got: %s", result.Summary)
	}
}

func TestRecallMemorySearchTool_ResultFormatting(t *testing.T) {
	entries := []vectordb.RecallEntry{
		{Entry: "First entry", CreatedAt: time.Date(2026, 3, 1, 10, 30, 0, 0, time.Local)},
		{Entry: "Second entry", CreatedAt: time.Date(2026, 3, 2, 15, 45, 0, 0, time.Local)},
	}

	tool := &RecallMemorySearchTool{}
	ctx := &ToolContext{
		RecallTimeRange: mockRecallTimeRangeFunc(entries),
		TenantID:        1,
	}

	input, _ := json.Marshal(recallSearchArgs{StartDate: "2026-03-01", EndDate: "2026-03-07"})
	result, err := tool.Execute(ctx, string(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Check formatting: header + numbered entries with timestamps
	if !strings.Contains(result.Summary, "## Recall Memory Results (2 entries)") {
		t.Errorf("expected header with count, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "1. [2026-03-01 10:30]") {
		t.Errorf("expected formatted timestamp, got: %s", result.Summary)
	}
	if !strings.Contains(result.Summary, "2. [2026-03-02 15:45]") {
		t.Errorf("expected second entry timestamp, got: %s", result.Summary)
	}
}

func TestLettaMemoryTools_IncludesRecall(t *testing.T) {
	tools := LettaMemoryTools()
	if !slices.ContainsFunc(tools, func(tool Tool) bool { return tool.Name() == "recall_memory_search" }) {
		t.Error("LettaMemoryTools should include recall_memory_search")
	}
	// Should have 6 tools total
	if len(tools) != 6 {
		t.Errorf("expected 6 Letta tools, got %d", len(tools))
	}
}

// TestMemoryContentRejectReason —— 「严禁往记忆里写 system reminder 这种东西」的**代码级**判据
// （用户定向 2026-10-14）。
//
// 为什么值得测：记忆是**全局**的且 `MEMORY.md` 每轮自动注入 system prompt ⇒ 一次误写会固化进
// 此后每个会话的提示词（自我污染）。而**提示词约束挡不住**（模型确实写过注入文本）⇒ 判据必须是
// 可执行代码。判别力：任一把 marker 从 `markers` 里删掉 ⇒ 对应用例必红。
func TestMemoryContentRejectReason(t *testing.T) {
	// 允许：客观、持久的事实（用户偏好 / 环境事实 / 仓库不变量）。
	allowed := []string{
		"- 用户偏好中文回复，代码注释也写中文。\n",
		"- 本机模拟器地址 127.0.0.1:5555，包名 com.chronostasys.xbot。\n",
		"- harmony 仓唯一门禁是 bash tools/gate.sh，必须 GATE_RC=0。\n",
		"- 服务端在 ~/.xbot/web/dist 提供静态产物。\n",
		"- 历史消息行里带 tool_call_id 字段（只记录字段名，不是回执）。\n",
	}
	for _, c := range allowed {
		if r := memoryContentRejectReason(c); r != "" {
			t.Errorf("objective fact should be allowed, got rejection %q for %q", r, c)
		}
	}

	// 拒绝：注入块 / 工具回执（写进去 = 把提示词注入固化进全局记忆）。
	rejected := []string{
		"<system-reminder role=\"reminder\"><note>AUTO-INJECTED</note></system-reminder>",
		"<user-msg>原话</user-msg>",
		"<dynamic-context>...</dynamic-context>",
		"<goal status=\"active\">long term goal</goal>",
		`{"tool_call_id":"call_abc123"}`,
		"这里有一堆 ToolCall 回执 tool_call_id=xyz",
	}
	for _, c := range rejected {
		if r := memoryContentRejectReason(c); r == "" {
			t.Errorf("injected/tool-output content MUST be rejected, but was allowed: %q", c)
		}
	}

	// 大小写不敏感（模型可能把标记写成大写）。
	if r := memoryContentRejectReason("<SYSTEM-REMINDER>"); r == "" {
		t.Error("marker detection must be case-insensitive")
	}
	// 空内容不拦（由别的校验负责）。
	if r := memoryContentRejectReason(""); r != "" {
		t.Errorf("empty content should not be rejected by this check, got %q", r)
	}
}
