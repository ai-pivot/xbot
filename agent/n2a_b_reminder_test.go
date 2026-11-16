package agent

// n2a_b_reminder_test —— N2a-B 的两条外部可观测判据。
//
// ② 注入面：真实 HTTP 请求体里，reminder append 段不得再出现用户原话回吐段；
//    <cwd>/<todos>/<guidelines> 骨架必须保留（逐项计数落盘供复核）。
// ③ todos 实时一致（核心）：SubAgent 的 run 必须能读到自己 key 上的**真实** todo，
//    并且 TodoWrite 改一条后，**下一次**请求的 <todos> 必须反映该改动。
//
// ③ 的价值在于它走的是**生产 wiring**：RunConfig 由 buildSubAgentRunConfig 构造
// （不是手搓字面量），所以「buildSubAgentRunConfig 漏接 TodoManager」这个根因
// 一旦回退，本用例必红（先红后绿见 mutation-red.log）。

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// todoBlockOf 从 reminder 全文里截出 <todos …>…</todos>（含空段形态）。
// 没有该段返回 ("", false)。
func todoBlockOf(reminder string) (string, bool) {
	i := strings.Index(reminder, "<todos")
	if i < 0 {
		return "", false
	}
	rest := reminder[i:]
	j := strings.Index(rest, "</todos>")
	if j < 0 {
		// <todos status="empty">…</todos> 也含 </todos>，走到这里说明截断异常
		return rest, true
	}
	return rest[:j+len("</todos>")], true
}

// reminderOfCall 从一次 mock LLM 调用的 messages 里取出 reminder 块（失败即 t.Fatalf）。
func reminderOfCall(t *testing.T, label string, msgs []llm.ChatMessage) string {
	t.Helper()
	text, present, _ := findSystemReminderInMessages(msgs)
	if !present {
		t.Fatalf("%s: 请求里没有 reminder 块（该轮本应是注入轮，见调用点的注入轮实测）", label)
	}
	return text
}

// ---------------------------------------------------------------------------
// ② 注入面：真实 HTTP body（llm.NewOpenAILLM + httptest 假上游）
// ---------------------------------------------------------------------------

// TestN2A_ReminderInjectionSurface —— 判据 ②。
//
// 需要 XBOT_REQ_DUMP_DIR 指定落盘目录（未设置则 skip —— 本测试的价值在于留下
// 可复查的请求体证据，不给目录就等于白跑，与 TestSystemReminderHTTPBody 同口径）。
func TestN2A_ReminderInjectionSurface(t *testing.T) {
	dumpDir := os.Getenv("XBOT_REQ_DUMP_DIR")
	if dumpDir == "" {
		t.Skip("XBOT_REQ_DUMP_DIR 未设置 —— 跳过注入面请求体证据测试")
	}

	// todo 侧真有内容：这样 <todos> 段一定会出现，能同时证明
	// 「骨架保留」与「没回吐用户原话」两件事，而不是只看到一片空白。
	t.Setenv("HOME", t.TempDir())
	mgr := tools.NewTodoManager()
	mgr.SetTodos("web:chat1", []tools.TodoItem{
		{Text: "预置步骤一", Status: "done"},
		{Text: "预置步骤二", Status: "doing"},
	})

	srv, dumpsFn := fakeChatCompletionServer(t, 1 /* 一轮 tool_call，一轮收尾 */)
	client := llm.NewOpenAILLM(llm.OpenAIConfig{
		BaseURL:      srv.URL + "/v1",
		APIKey:       "test-key",
		DefaultModel: "test-model",
		APIType:      llm.APITypeChatCompletions,
	})

	// reminder 的 <cwd> 来自 cfg.Session.GetCurrentDir()（见 runState.buildSystemReminder）
	// —— 不接 Session 就恒为空，本判据会退化成「cwd 计数 = 0」看不出保留与否。
	// 用 t.TempDir() 而不是硬编码路径：只断言**计数**，路径内容不参与判定。
	_, sess := newAgentHistorySession(t)
	testCWD := t.TempDir()
	sess.SetCurrentDir(testCWD)

	userMsg := "UNIQUE-N2A-B-USER-TEXT：把 5 层目录下的 README 改完再跑一次回归"
	out := Run(context.Background(), RunConfig{
		LLMClient:    client,
		Model:        "test-model",
		Tools:        newTestRegistry(&mockTool{name: "Echo", result: &tools.ToolResult{Summary: "ok"}}),
		Messages:     []llm.ChatMessage{llm.NewSystemMessage("You are a test agent."), llm.NewUserMessage(userMsg)},
		AgentID:      "main",
		Channel:      "web",
		ChatID:       "chat1",
		SessionKey:   "web:chat1",
		Stream:       false,
		Session:      sess,
		InitialCWD:   testCWD,
		TodoManager:  &todoManagerAdapter{mgr: mgr},
		ToolExecutor: nil, // 走 defaultToolExecutor，与生产一致
	})
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}

	dumps := dumpsFn()
	if len(dumps) != 2 {
		t.Fatalf("上游收到 %d 条请求，期望 2", len(dumps))
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# N2a-B 判据②：注入面计数（真实 HTTP 请求体）\n")
	fmt.Fprintf(&sb, "%-6s %-10s %-9s %-9s %-8s %-11s %s\n",
		"req", "reminder", "user-msg", "<cwd>", "<todos>", "<guidelines>", "reminder_len")

	for _, d := range dumps {
		label := fmt.Sprintf("req#%d", d.Index+1)
		body := parseWireBody(t, label, d.Body)
		last := len(body.Messages) - 1
		lastContent := contentString(body.Messages[last].Content)
		blk, present := reminderBlock(lastContent)

		// 落盘原始请求体（可 grep、可 diff）
		writeDump(t, fmt.Sprintf("n2a-b-req-%d-body.json", d.Index+1), d.Body)

		if !present {
			fmt.Fprintf(&sb, "%-6s %-10s %-9s %-9s %-8s %-11s %s\n", label, "ABSENT", "-", "-", "-", "-", "-")
			if d.Index == 0 {
				t.Fatalf("%s: 首轮必须有 reminder（每轮注入）", label)
			}
			continue
		}

		userMsgCount := strings.Count(blk, "<user-msg")
		cwdCount := strings.Count(blk, "<cwd>")
		todosCount := strings.Count(blk, "<todos")
		guidelinesCount := strings.Count(blk, "<guidelines>")
		fmt.Fprintf(&sb, "%-6s %-10s %-9d %-9d %-8d %-11d %d\n",
			label, "present", userMsgCount, cwdCount, todosCount, guidelinesCount, len(blk))

		// ② 的唯一硬断言：append 段里 0 处用户原话回吐
		if userMsgCount != 0 {
			t.Errorf("%s: append 段里出现 %d 处 <user-msg，N2a-B 已删除该段", label, userMsgCount)
		}
		if strings.Contains(blk, "UNIQUE-N2A-B-USER-TEXT") {
			t.Errorf("%s: 用户原话正文回吐进了 reminder", label)
		}
		// 骨架保留（口径冲突按 §2 执行：cwd/guidelines 保留，todos 实时）
		if cwdCount != 1 {
			t.Errorf("%s: <cwd> 计数 = %d, want 1（必须保留）", label, cwdCount)
		}
		if guidelinesCount != 1 {
			t.Errorf("%s: <guidelines> 计数 = %d, want 1（必须保留）", label, guidelinesCount)
		}
		if todosCount != 1 {
			t.Errorf("%s: <todos> 计数 = %d, want 1（manager 已接，必须实时输出）", label, todosCount)
		}
		// 实时性：首轮就应看到预置的 2 条（不是 status="empty"）
		if d.Index == 0 {
			if strings.Contains(blk, `status="empty"`) {
				t.Errorf("%s: 有 2 条预置 todo 却输出 status=\"empty\"", label)
			}
			if !strings.Contains(blk, "预置步骤一") || !strings.Contains(blk, "预置步骤二") {
				t.Errorf("%s: <todos> 未反映预置列表:\n%s", label, blk)
			}
		}
		// 落盘 append 段全文（供逐项 grep 复核）
		writeDump(t, fmt.Sprintf("n2a-b-req-%d-reminder.txt", d.Index+1), blk+"\n")
	}

	t.Logf("\n%s", sb.String())
	writeDump(t, "n2a-b-injection-matrix.txt", sb.String())
}

// ---------------------------------------------------------------------------
// ③ todos 实时一致（核心）：SubAgent 走 buildSubAgentRunConfig 的真实 wiring
// ---------------------------------------------------------------------------

// n2aRelaxedTodoItem 只用于从 <todos> 文本里取 (status, text) 对。
var n2aTodoItemRe = regexp.MustCompile(`<todo status="([^"]*)" id="(\d+)">([^<]*)</todo>`)

func parseTodoItems(t *testing.T, label, block string) [][2]string {
	t.Helper()
	var out [][2]string
	for _, m := range n2aTodoItemRe.FindAllStringSubmatch(block, -1) {
		out = append(out, [2]string{m[1], m[3]})
	}
	if len(out) == 0 {
		t.Fatalf("%s: <todos> 块里没有任何 <todo> 项:\n%s", label, block)
	}
	return out
}

func formatTodoItems(items [][2]string) string {
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, it[0]+":"+it[1])
	}
	return strings.Join(parts, " | ")
}

// TestN2A_SubAgentTodoInjectionIsLive —— 判据 ③（核心）。
//
// ⚠️ 频率口径（必读）：reminder 不是每轮注入 —— 由 shouldInsertSystemReminder 决定
// 注入轮（当前实现 = 1、10、20…，且由 TestShouldInsertSystemReminder 参数化锁定）。
// 本用例**实测**了这一点：扫 21 轮请求，带 reminder marker 的请求序号必须恰好是
// [1 10 20]（断言在下面 reminded 那段），所以三个探针位置不依赖注释里的口头事实。
// 若将来频率改成每轮（N2b），这条断言会先红，提醒后续维护者改探针。
//
// 本用例跑 21 轮、只在**三个实测注入轮**取 <todos>：
//
//	req#1  ← iteration 1  ：预置的 2 条（证明 SubAgent 读到自己 key 上的真实 todo）
//	req#10 ← iteration 10 ：轮1 写入的 3 条，其中第三条 pending
//	req#20 ← iteration 20 ：轮15 把第三条改成 doing 之后，必须看到 doing
//
// 这三处正好等价于「第一次请求 / 改动后的下一次注入请求 / 再下一次」，判据不变：
// 注入内容必须与该时刻 TodoManager 里的列表一致。
//
// SessionKey 推导（不许改 key 规则，只把推导写清楚）：
//
//	buildSubAgentRunConfig: subAgentID = parentCtx.AgentID + "/" + roleName
//	                        cfg.SessionKey = subAgentID
//	runState.todoKey():     AgentID 含 "/" ⇒ 返回 sessionKey（= cfg.SessionKey）
//	TodoManager.sessionKey(ctx): AgentID 含 "/" ⇒ ctx.SessionKey（= cfg.SessionKey）
//	⇒ 读（reminder）与写（TodoWrite）用同一个 key，预置也用同一个 key。
func TestN2A_SubAgentTodoInjectionIsLive(t *testing.T) {
	// 必须在 NewTodoManager 与 Run 之前：否则会读写真实 ~/.xbot/todos，
	// 读到脏文件导致假绿/假红。
	t.Setenv("HOME", t.TempDir())

	mgr := tools.NewTodoManager()
	reg := tools.NewRegistry()
	reg.Register(&tools.TodoWriteTool{Manager: mgr})
	reg.Register(&mockTool{name: "Echo", result: &tools.ToolResult{Summary: "ok"}})

	mt, _ := newAgentHistorySession(t)
	a := &Agent{
		multiSession: mt,
		tools:        reg,
		skills:       NewSkillStore(t.TempDir(), nil, nil),
		todoManager:  mgr,
	}

	ctx := WithUserContext(context.Background(), &UserContext{})
	parentCtx := &tools.ToolContext{
		AgentID:  "main",
		Channel:  "web",
		ChatID:   "chat-n2a-b",
		SenderID: "u1",
		UserID:   42,
		Role:     "admin",
	}

	// 真实生产 wiring：RunConfig 由 buildSubAgentRunConfig 构造
	cfg, err := a.buildSubAgentRunConfig(ctx, parentCtx, "把三件事做完", "", nil,
		tools.SubAgentCapabilities{}, "explorer", false, "inst-n2a", "")
	if err != nil {
		t.Fatalf("buildSubAgentRunConfig: %v", err)
	}
	if want := "main/explorer"; cfg.SessionKey != want {
		t.Fatalf("cfg.SessionKey = %q, want %q（上面的 key 推导基于此）", cfg.SessionKey, want)
	}
	if cfg.TodoManager == nil {
		t.Fatal("buildSubAgentRunConfig 没接 cfg.TodoManager —— SubAgent 每轮会注入假的 status=\"empty\"")
	}

	// 预置 2 条：写到「将来的 cfg.SessionKey」上
	preKey := cfg.SessionKey
	mgr.SetTodos(preKey, []tools.TodoItem{
		{Text: "预置一", Status: "done"},
		{Text: "预置二", Status: "pending"},
	})
	if got := mgr.GetTodos(preKey); len(got) != 2 {
		t.Fatalf("预置失败: %d 条", len(got))
	}

	// 21 轮：iter 1 写 3 条（第三条 pending），iter 15 把第三条改 doing，iter 21 收尾。
	const totalIterations = 21
	const changeAt = 15 // 1-based iteration
	round1 := `{"todos":[{"text":"步骤一","status":"done"},{"text":"步骤二","status":"doing"},{"text":"步骤三","status":"pending"}]}`
	roundChange := `{"todos":[{"text":"步骤一","status":"done"},{"text":"步骤二","status":"doing"},{"text":"步骤三","status":"doing"}]}`

	responses := make([]llm.LLMResponse, 0, totalIterations)
	for iter := 1; iter <= totalIterations; iter++ {
		switch iter {
		case totalIterations:
			responses = append(responses, llm.LLMResponse{Content: "done"})
		case 1:
			responses = append(responses, llm.LLMResponse{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls:    []llm.ToolCall{{ID: "call_1", Name: "TodoWrite", Arguments: round1}},
			})
		case changeAt:
			responses = append(responses, llm.LLMResponse{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls:    []llm.ToolCall{{ID: fmt.Sprintf("call_%d", iter), Name: "TodoWrite", Arguments: roundChange}},
			})
		default:
			responses = append(responses, llm.LLMResponse{
				FinishReason: llm.FinishReasonToolCalls,
				ToolCalls:    []llm.ToolCall{{ID: fmt.Sprintf("call_%d", iter), Name: "Echo", Arguments: fmt.Sprintf(`{"n":%d}`, iter)}},
			})
		}
	}

	mock := &mockLLM{responses: responses}
	cfg.LLMClient = mock
	cfg.Stream = false
	cfg.ToolExecutor = nil // defaultToolExecutor：与生产一致（buildToolContext 决定 key）

	out := Run(context.Background(), cfg)
	if out.Error != nil {
		t.Fatalf("Run: %v", out.Error)
	}
	if len(mock.calls) != totalIterations {
		t.Fatalf("LLM 请求次数 = %d, want %d", len(mock.calls), totalIterations)
	}

	// 只在三个注入轮取 <todos>。注入轮**实测**（不靠注释断言）：扫全部请求，
	// 记录哪些带 reminder marker，并断言集合 == {1,10,20}。
	// 依据：shouldInsertSystemReminder 的实现（1、10、20…，TestShouldInsertSystemReminder 锁定），
	// 本断言把「实测事实」钉进证据，半年后谁改频率这里先红。
	var reminded []int
	for i, call := range mock.calls {
		if _, present, _ := findSystemReminderInMessages(call.Messages); present {
			reminded = append(reminded, i+1)
		}
	}
	if fmt.Sprint(reminded) != fmt.Sprint([]int{1, 10, 20}) {
		t.Fatalf("实测注入轮 = %v, want [1 10 20]（reminder 频率变了？本用例的探针位置依赖这一事实）", reminded)
	}

	probes := []struct {
		iter int
		req  int // 1-based 请求序号
	}{{1, 1}, {10, 10}, {20, 20}}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# N2a-B 判据③：SubAgent <todos> 实时一致\n")
	fmt.Fprintf(&sb, "sessionKey(preKey) = %s（role=explorer, parent=main）\n", preKey)
	fmt.Fprintf(&sb, "实测注入轮（21 轮请求内带 reminder marker 的请求序号）= %v\n", reminded)
	fmt.Fprintf(&sb, "TodoWrite 改动落在 iteration %d\n\n", changeAt)

	blocks := make([]string, 0, len(probes))
	for _, p := range probes {
		call := mock.calls[p.req-1]
		label := fmt.Sprintf("req#%d(iteration %d)", p.req, p.iter)
		reminder := reminderOfCall(t, label, call.Messages)
		blk, ok := todoBlockOf(reminder)
		if !ok {
			t.Fatalf("%s: reminder 里没有 <todos> 段:\n%s", label, reminder)
		}
		blocks = append(blocks, blk)
		writeDump(t, fmt.Sprintf("crit3-req-%d-todos.txt", p.req), blk+"\n")
		writeDump(t, fmt.Sprintf("crit3-req-%d-reminder.txt", p.req), reminder+"\n")
		fmt.Fprintf(&sb, "%s: %s\n", label, blk)
	}

	// req#1：预置的 2 条（证明 SubAgent 读到自己 key 上的真实 todo，而非硬编码空）
	items1 := parseTodoItems(t, "req#1", blocks[0])
	if got := formatTodoItems(items1); got != "done:预置一 | pending:预置二" {
		t.Errorf("req#1 <todos> = %q, want %q（预置列表必须被读到）", got, "done:预置一 | pending:预置二")
	}
	if strings.Contains(blocks[0], `status="empty"`) {
		t.Error("req#1 出现 status=\"empty\" —— 明明是 2 条预置 todo")
	}

	// req#10：轮1 写入的 3 条，第三条 pending
	items2 := parseTodoItems(t, "req#10", blocks[1])
	if got := formatTodoItems(items2); got != "done:步骤一 | doing:步骤二 | pending:步骤三" {
		t.Errorf("req#10 <todos> = %q, want %q（轮1 的 TodoWrite 必须在本轮可见）",
			got, "done:步骤一 | doing:步骤二 | pending:步骤三")
	}

	// req#20：同 3 条，第三条已变 doing（核心：改动后的下一次注入必须反映）
	items3 := parseTodoItems(t, "req#20", blocks[2])
	if got := formatTodoItems(items3); got != "done:步骤一 | doing:步骤二 | doing:步骤三" {
		t.Errorf("req#20 <todos> = %q, want %q（iteration %d 的 TodoWrite 改动必须下一注入轮可见）",
			got, "done:步骤一 | doing:步骤二 | doing:步骤三", changeAt)
	}

	t.Logf("\n%s", sb.String())
	writeDump(t, "crit3-todo-matrix.txt", sb.String())
}
