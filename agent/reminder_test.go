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
	// task: 有 tool message 在 user 之后 → kind=user_processing（历史需求处理中）
	if !strings.Contains(result, "<task>") || !strings.Contains(result, "<kind>user_processing</kind>") {
		t.Errorf("expected user_processing task, got:\n%s", result)
	}
	if !strings.Contains(result, "<content>Hello</content>") {
		t.Errorf("expected task content Hello, got:\n%s", result)
	}
	if strings.Contains(result, "已完成 ") || strings.Contains(result, "已执行 ") {
		t.Errorf("should NOT contain tool count, got:\n%s", result)
	}
	if !strings.Contains(result, "<guidelines>") {
		t.Errorf("expected guidelines, got:\n%s", result)
	}
}

func TestBuildSystemReminder_NewMessage(t *testing.T) {
	// User just said something — no tool messages after it yet
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Fix the login bug"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Shell"}}, "", "main", "", "", "", nil)

	if !strings.Contains(result, "<kind>user_latest</kind>") || !strings.Contains(result, "<content>Fix the login bug</content>") {
		t.Errorf("expected user_latest task for fresh message, got:\n%s", result)
	}
	if strings.Contains(result, "<kind>user_processing</kind>") {
		t.Errorf("should NOT show user_processing for fresh message, got:\n%s", result)
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

	if !strings.Contains(result, "<kind>user_processing</kind>") || !strings.Contains(result, "<content>Refactor the codebase</content>") {
		t.Errorf("expected user_processing task for old message, got:\n%s", result)
	}
	if strings.Contains(result, "<kind>user_latest</kind>") {
		t.Errorf("should NOT show user_latest for old message, got:\n%s", result)
	}
}

func TestBuildSystemReminder_SubAgent(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "Do task X"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Read"}}, "", "main/worker", "", "", "", nil)

	if !strings.Contains(result, "<kind>subagent</kind>") || !strings.Contains(result, "<content>Do task X</content>") {
		t.Errorf("SubAgent should show subagent task, got:\n%s", result)
	}
}

func TestBuildSystemReminder_WithTodo(t *testing.T) {
	messages := []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}

	result := BuildSystemReminder(messages, []llm.ToolCall{{Name: "Read"}}, "2/5 完成", "main", "", "", "", nil)

	if !strings.Contains(result, "<todo>2/5 完成</todo>") {
		t.Errorf("expected XML todo, got:\n%s", result)
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

	if !strings.Contains(result, "<guideline>主动维护知识文档和代码质量</guideline>") {
		t.Errorf("expected '主动维护知识文档和代码质量' guideline, got:\n%s", result)
	}
	if strings.Contains(result, "post-dev") {
		t.Errorf("should not contain old post-dev keyword, got:\n%s", result)
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
