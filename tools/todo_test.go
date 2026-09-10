package tools

import (
	"context"
	"os"
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
	_, err := mainTool.Execute(mainCtx, `{"todos":[{"text":"main-task-1","status":"pending"},{"text":"main-task-2","status":"pending"}]}`)
	if err != nil {
		t.Fatalf("main TodoWrite failed: %v", err)
	}

	// SubAgent 写入 3 个 TODO
	_, err = mainTool.Execute(subCtx, `{"todos":[{"text":"sub-task-1","status":"pending"},{"text":"sub-task-2","status":"pending"},{"text":"sub-task-3","status":"pending"}]}`)
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

func TestTodoManager_SessionKey_RootSessionKeyPriority(t *testing.T) {
	mgr := NewTodoManager()

	// physicalChannel override 场景：web 用户浏览 CLI 会话。
	// SessionKey 被 override 成 "web:chat1"，RootSessionKey 是 canonical "cli:chat1"。
	// 主 Agent 的 todos 必须用 RootSessionKey（canonical），否则 GetActiveProgress
	// 恢复路径（读 "cli:chat1"）读不到 todos（"手机端实时显示、电脑端后打开
	// 不显示"的根因）。
	ctx := &ToolContext{
		Ctx:            context.Background(),
		AgentID:        "main",
		Channel:        "cli",
		ChatID:         "chat1",
		SessionKey:     "web:chat1", // physicalChannel override
		RootSessionKey: "cli:chat1", // canonical
	}
	key := mgr.sessionKey(ctx)
	if key != "cli:chat1" {
		t.Errorf("BUG: main-agent sessionKey with physicalChannel override = %q, want %q (canonical RootSessionKey)", key, "cli:chat1")
	}

	// SubAgent：仍然用 SessionKey（subAgentID）隔离。
	subCtx := &ToolContext{
		Ctx:            context.Background(),
		AgentID:        "main/explore",
		Channel:        "cli",
		ChatID:         "chat1",
		SessionKey:     "main/explore", // subAgentID
		RootSessionKey: "cli:chat1",    // parent canonical
	}
	subKey := mgr.sessionKey(subCtx)
	if subKey != "main/explore" {
		t.Errorf("SubAgent sessionKey = %q, want %q (subAgentID isolation)", subKey, "main/explore")
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
	_, _ = writeTool.Execute(mainCtx, `{"todos":[{"text":"main-task","status":"pending"}]}`)

	// SubAgent 写入
	_, _ = writeTool.Execute(subCtx, `{"todos":[{"text":"sub-task","status":"done"}]}`)

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

// 严格校验：LLM 发旧格式 done: true（无 status 字段）必须报错，不做任何兼容转换。
// 用户明确要求：schema 就是 status 必填，违反 schema → 报错让 LLM 自行纠正。
func TestTodoWrite_LegacyDoneRejected(t *testing.T) {
	mgr := NewTodoManager()
	ctx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "chat-strict",
	}
	tool := &TodoWriteTool{Manager: mgr}

	// 旧格式：done: true，无 status → 必须报错（json 静默丢弃 done，status 为空）
	res, err := tool.Execute(ctx, `{"todos":[{"text":"task-a","done":true},{"text":"task-b","done":false}]}`)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	if !res.IsError {
		t.Errorf("legacy done format must be REJECTED with IsError=true, got summary: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "status") {
		t.Errorf("error message must mention the 'status' field, got: %q", res.Summary)
	}

	// 确认没有半写状态：报错时不得写入任何 TODO
	todos := mgr.GetTodos(mgr.sessionKey(ctx))
	if len(todos) != 0 {
		t.Errorf("rejected call must NOT write todos, got %d items", len(todos))
	}

	// 旧格式 done: false → 同样报错（无 status 就是无 status）
	res2, _ := tool.Execute(ctx, `{"todos":[{"text":"x","done":false}]}`)
	if !res2.IsError {
		t.Errorf("done:false without status must also be rejected, got: %q", res2.Summary)
	}
}

// 严格校验：text 必填（用户要求 2026-09-04）——空/缺失 text 的条目必须报错，
// 不做静默接受（否则渲染出空行 TODO）。与 status 校验同模式：报错让 LLM 自行纠正。
func TestTodoWrite_EmptyTextRejected(t *testing.T) {
	// 磁盘隔离：GetTodos 内存未命中时从 ~/.xbot/todos/<hash>.json 懒加载。
	// 不隔离 HOME 时，红灯运行的未校验写入会持久化到真实用户目录，绿灯
	// 运行读到脏数据（"rejected call must NOT write todos, got 1 items" 的
	// 假失败）。t.TempDir() 让本测试的持久化与真实 HOME 完全隔离。
	t.Setenv("HOME", t.TempDir())
	mgr := NewTodoManager()
	ctx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "chat-strict-text",
	}
	tool := &TodoWriteTool{Manager: mgr}

	// 缺失 text（json 静默丢弃缺失字段 → text 为空字符串）→ 必须报错
	res, err := tool.Execute(ctx, `{"todos":[{"status":"doing"},{"text":"real-task","status":"pending"}]}`)
	if err != nil {
		t.Fatalf("Execute returned err: %v", err)
	}
	if !res.IsError {
		t.Errorf("missing text must be REJECTED with IsError=true, got summary: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "text") {
		t.Errorf("error message must mention the 'text' field, got: %q", res.Summary)
	}

	// 确认没有半写状态：报错时不得写入任何 TODO（含合法的第 2 项）
	todos := mgr.GetTodos(mgr.sessionKey(ctx))
	if len(todos) != 0 {
		t.Errorf("rejected call must NOT write todos, got %d items", len(todos))
	}

	// 空白字符串 text（"   "）同样报错
	res2, _ := tool.Execute(ctx, `{"todos":[{"text":"   ","status":"doing"}]}`)
	if !res2.IsError {
		t.Errorf("whitespace-only text must be rejected, got: %q", res2.Summary)
	}
}

// 严格校验：非法 status 值报错。
func TestTodoWrite_InvalidStatusRejected(t *testing.T) {
	mgr := NewTodoManager()
	ctx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "chat-strict",
	}
	tool := &TodoWriteTool{Manager: mgr}

	res, _ := tool.Execute(ctx, `{"todos":[{"text":"task","status":"finished"}]}`)
	if !res.IsError {
		t.Errorf("invalid status 'finished' must be rejected, got: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, `"finished"`) {
		t.Errorf("error message should echo the invalid value, got: %q", res.Summary)
	}

	// 空字符串 status 同样报错
	res2, _ := tool.Execute(ctx, `{"todos":[{"text":"task","status":""}]}`)
	if !res2.IsError {
		t.Errorf("empty status must be rejected, got: %q", res2.Summary)
	}
}

// 合法三态正常工作。
func TestTodoWrite_ValidStatusAccepted(t *testing.T) {
	mgr := NewTodoManager()
	ctx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "chat-valid",
	}
	tool := &TodoWriteTool{Manager: mgr}

	res, err := tool.Execute(ctx, `{"todos":[{"text":"a","status":"done"},{"text":"b","status":"doing"},{"text":"c","status":"pending"}]}`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if res.IsError {
		t.Fatalf("valid statuses must be accepted, got error: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "1/3") {
		t.Errorf("summary should show 1/3 done, got: %q", res.Summary)
	}
	if !strings.Contains(res.Summary, "1 项进行中") {
		t.Errorf("summary should show 1 doing, got: %q", res.Summary)
	}

	todos := mgr.GetTodos(mgr.sessionKey(ctx))
	if len(todos) != 3 {
		t.Fatalf("expected 3 todos, got %d", len(todos))
	}
	for i, it := range todos {
		if !isValidStatus(it.Status) {
			t.Errorf("todo %d has invalid status %q", i+1, it.Status)
		}
	}
}

// TestTodoWrite_PreservesAgentOrder 验证 TODO 顺序 == agent 给的顺序，绝不被重排。
//
// 历史 bug（本次修复）：SetTodos 之前有一行
//
//	slices.SortFunc(todos, func(a, b TodoItem) int { return cmp.Compare(a.ID, b.ID) })
//
// —— agent 用 id 表达优先级/依赖时，展示顺序被 id 重排，与它写的计划不符
// （web todo panel 用户可见：「有时候按照 id 排序了」）。现在列表顺序由数组
// 位置决定，id 参数已彻底移除。
//
// 复现方式：输入顺序刻意不等于任何自然排序（既非 id 序、也非 text 字典序），
// 断言输出 == 输入顺序。带排序时该断言红灯。
func TestTodoWrite_PreservesAgentOrder(t *testing.T) {
	mgr := NewTodoManager()
	ctx := &ToolContext{
		Ctx:     context.Background(),
		AgentID: "main",
		Channel: "cli",
		ChatID:  "order-preserve",
	}
	tool := &TodoWriteTool{Manager: mgr}

	// 顺序刻意「乱」：zebra → apple → mango（不是字典序/也不是编号序）
	in := `{"todos":[` +
		`{"text":"zebra step","status":"pending"},` +
		`{"text":"apple step","status":"doing"},` +
		`{"text":"mango step","status":"done"}]}`
	if _, err := tool.Execute(ctx, in); err != nil {
		t.Fatalf("todo_write failed: %v", err)
	}

	key := mgr.sessionKey(ctx)
	want := []string{"zebra step", "apple step", "mango step"}
	got := mgr.GetTodos(key)
	if len(got) != len(want) {
		t.Fatalf("got %d todos, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Fatalf("order changed at index %d: got %q, want %q (full=%v) — the list order IS the plan order",
				i, got[i].Text, want[i], todoTexts(got))
		}
	}

	// 持久化 round-trip 也必须保持顺序
	if err := mgr.SaveToFile(key); err != nil {
		t.Fatalf("SaveToFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(todoFilePath(key)) })

	loaded := NewTodoManager()
	if err := loaded.LoadFromFile(key); err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	got2 := loaded.GetTodos(key)
	if len(got2) != len(want) {
		t.Fatalf("after reload got %d todos, want %d", len(got2), len(want))
	}
	for i := range want {
		if got2[i].Text != want[i] {
			t.Fatalf("order changed after persistence at index %d: got %q, want %q",
				i, got2[i].Text, want[i])
		}
	}
}

func todoTexts(items []TodoItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Text
	}
	return out
}
