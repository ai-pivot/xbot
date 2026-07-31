package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

func TestBuildSystemReminder_Basic(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "user", Content: "Hello"},
		{Role: "assistant", Content: "Hi!"},
		{Role: "tool", Content: "Result"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Shell"}}, "", "main", "", "", "", nil)

	if !strings.Contains(result, "<system-reminder>") {
		t.Error("expected system-reminder tag")
	}
	// When there's a tool message after user message, it should show "正在处理中"
	if !strings.Contains(result, "用户原始需求（正在处理中，已执行 1 次工具调用）: Hello") {
		t.Errorf("expected user goal with processing hint, got:\n%s", result)
	}
	if !strings.Contains(result, "已完成 1 次工具调用") {
		t.Errorf("expected tool count, got:\n%s", result)
	}
	if !strings.Contains(result, "Shell") {
		t.Errorf("expected tool name in reminder, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NewMessage(t *testing.T) {
	// User just said something — no tool messages after it yet
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Fix the login bug"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Shell"}}, "", "main", "", "", "", nil)

	if !strings.Contains(result, "用户最新需求: Fix the login bug") {
		t.Errorf("expected '最新需求' for fresh message, got:\n%s", result)
	}
	if strings.Contains(result, "正在处理中") {
		t.Errorf("should NOT show processing hint for fresh message, got:\n%s", result)
	}
}

func TestBuildSystemReminder_OldMessage(t *testing.T) {
	// User said something long ago — many tool calls after it
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Refactor the codebase"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{Name: "Read"}}},
		{Role: "tool", Content: "file content"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{Name: "Shell"}}},
		{Role: "tool", Content: "build output"},
		{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{Name: "Edit"}}},
		{Role: "tool", Content: "edit result"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Grep"}}, "", "main", "", "", "", nil)

	if !strings.Contains(result, "用户原始需求（正在处理中，已执行 3 次工具调用）: Refactor the codebase") {
		t.Errorf("expected '原始需求' with processing count for old message, got:\n%s", result)
	}
	if strings.Contains(result, "用户最新需求") {
		t.Errorf("should NOT show '最新需求' for old message, got:\n%s", result)
	}
}

func TestBuildSystemReminder_SubAgent(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Do task X"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Read"}}, "", "main/worker", "", "", "", nil)

	if !strings.Contains(result, "执行任务: Do task X") {
		t.Errorf("SubAgent should show task prefix, got:\n%s", result)
	}
}

func TestBuildSystemReminder_WithTodo(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Read"}}, "2/5 完成", "main", "", "", "", nil)

	if !strings.Contains(result, "TODO: 2/5 完成") {
		t.Errorf("expected TODO summary, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NoContextEditHints(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "tool", Content: "result"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Shell"}}, "", "main", "", "", "", nil)

	if strings.Contains(result, "context_edit") {
		t.Errorf("should not contain context_edit hints, got:\n%s", result)
	}
}

func TestBuildSystemReminder_Empty(t *testing.T) {
	result := BuildSystemReminder(nil, nil, "", "main", "", "", "", nil)
	if result != "" {
		t.Errorf("expected empty result for nil messages, got: %q", result)
	}
}

func TestBuildSystemReminder_GitCommitTriggersPostDev(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix bug"},
	}

	// Shell with git commit should trigger post-dev reminder
	result := BuildSystemReminder(messages, []llm.ToolCall{{
		Name:      "Shell",
		Arguments: `{"command":"git commit -m \"fix: bug\" -a"}`,
	}}, "", "main", "", "", "", nil)

	if !strings.Contains(result, "post-dev") {
		t.Errorf("expected post-dev reminder on git commit, got:\n%s", result)
	}
	if !strings.Contains(result, "git commit") {
		t.Errorf("expected git commit mention, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NoPostDevWithoutGitCommit(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "fix bug"},
	}

	// Shell without git commit should NOT trigger post-dev reminder
	result := BuildSystemReminder(messages, []llm.ToolCall{{
		Name:      "Shell",
		Arguments: `{"command":"go build ./..."}`,
	}}, "", "main", "", "", "", nil)

	if strings.Contains(result, "post-dev") {
		t.Errorf("should not contain post-dev reminder without git commit, got:\n%s", result)
	}
	if strings.Contains(result, "knowledge-management") {
		t.Errorf("should not contain old knowledge-management reminder, got:\n%s", result)
	}
}
