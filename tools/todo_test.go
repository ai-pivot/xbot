package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTodoManager_SessionIsolation(t *testing.T) {
	mgr := NewTodoManager()

	// 主 Agent 的 ToolContext
	mainCtx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "session-1",
	}

	// SubAgent 的 ToolContext（与主 Agent 共享 Channel + ChatID，但 AgentID 不同）
	subCtx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main/ministry-works",
		Channel: "cli",
		ChatID:  "session-1",
	}

	// 主 Agent 写入 2 个 TODO
	mainTool := &TodoWriteTool{Manager: mgr}
	_, err := mainTool.Execute(mainCtx, `{"todos":[{"id":1,"text":"main-task-1","done":false},{"id":2,"text":"main-task-2","done":false}]}`)
	if err != nil {
		t.Fatalf("main TodoWrite failed: %v", err)
	}

	// SubAgent 写入 3 个 TODO
	_, err = mainTool.Execute(subCtx, `{"todos":[{"id":1,"text":"sub-task-1","done":false},{"id":2,"text":"sub-task-2","done":false},{"id":3,"text":"sub-task-3","done":false}]}`)
	if err != nil {
		t.Fatalf("sub TodoWrite failed: %v", err)
	}

	// 验证主 Agent 的 TODO 不受影响
	mainItems := mgr.GetTodos(mgr.sessionKey(mainCtx))
	if len(mainItems) != 2 {
		t.Fatalf("main agent should have 2 TODOs, got %d", len(mainItems))
	}
	for i, item := range mainItems {
		want := "main-task-" + string(rune('1'+i))
		if item.Text != want {
			t.Errorf("main TODO[%d] text = %q, want %q", i, item.Text, want)
		}
	}

	// 验证 SubAgent 的 TODO 独立存储
	subItems := mgr.GetTodos(mgr.sessionKey(subCtx))
	if len(subItems) != 3 {
		t.Fatalf("sub agent should have 3 TODOs, got %d", len(subItems))
	}
	for i, item := range subItems {
		want := "sub-task-" + string(rune('1'+i))
		if item.Text != want {
			t.Errorf("sub TODO[%d] text = %q, want %q", i, item.Text, want)
		}
	}
}

func TestTodoManager_SessionKey_BackwardsCompatible(t *testing.T) {
	mgr := NewTodoManager()

	// 无 AgentID 时应回退到 Channel:ChatID（向后兼容）
	ctx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "",
		Channel: "cli",
		ChatID:  "session-1",
	}
	key := mgr.sessionKey(ctx)
	if key != "cli:session-1" {
		t.Errorf("sessionKey without AgentID = %q, want %q", key, "cli:session-1")
	}

	// 主 Agent AgentID="main"（不含 "/"），保持 Channel:ChatID 不变
	ctx1b := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "session-1",
	}
	key1b := mgr.sessionKey(ctx1b)
	if key1b != "cli:session-1" {
		t.Errorf("sessionKey with main AgentID = %q, want %q", key1b, "cli:session-1")
	}

	// SubAgent AgentID 含 "/"，使用 AgentID:Channel:ChatID
	ctx2 := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main/explore",
		Channel: "cli",
		ChatID:  "session-1",
	}
	key2 := mgr.sessionKey(ctx2)
	if key2 != "main/explore:cli:session-1" {
		t.Errorf("sessionKey with SubAgent AgentID = %q, want %q", key2, "main/explore:cli:session-1")
	}

	// 无 Channel/ChatID 时返回空
	ctx3 := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
	}
	key3 := mgr.sessionKey(ctx3)
	if key3 != "" {
		t.Errorf("sessionKey without Channel/ChatID = %q, want empty", key3)
	}
}

func TestTodoListTool_Isolation(t *testing.T) {
	mgr := NewTodoManager()

	mainCtx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "session-1",
	}
	subCtx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main/reviewer",
		Channel: "cli",
		ChatID:  "session-1",
	}

	// 主 Agent 写入
	writeTool := &TodoWriteTool{Manager: mgr}
	_, _ = writeTool.Execute(mainCtx, `{"todos":[{"id":1,"text":"main-task","done":false}]}`)

	// SubAgent 写入
	_, _ = writeTool.Execute(subCtx, `{"todos":[{"id":1,"text":"sub-task","done":true}]}`)

	// TodoListTool 验证隔离
	listTool := &TodoListTool{Manager: mgr}

	mainResult, _ := listTool.Execute(mainCtx, "")
	if mainResult == nil {
		t.Fatal("main TodoList returned nil")
		return
	}
	// 主 Agent 应该看到 0/1 完成
	if !strings.Contains(mainResult.Summary, "0/1") {
		t.Errorf("main TodoList summary = %q, should show 0/1", mainResult.Summary)
	}

	subResult, _ := listTool.Execute(subCtx, "")
	if subResult == nil {
		t.Fatal("sub TodoList returned nil")
		return
	}
	// SubAgent 应该看到 1/1 完成
	if !strings.Contains(subResult.Summary, "1/1") {
		t.Errorf("sub TodoList summary = %q, should show 1/1", subResult.Summary)
	}
}
