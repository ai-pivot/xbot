package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"xbot/llm"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// 首轮注入 + 稀疏节奏（1, 10, 20 …）
//
// 背景（2026-10-28）：reminder 曾经由 postToolProcessing 构建到跨迭代字段
// s.systemReminder，而 LLM 请求发生在第 i 轮**工具执行之前**。构建落后请求一轮
// → 结构性 off-by-one：即使谓词为真，第 1 轮请求里的 reminder 也是空串。
// 现在 reminder 在请求前按需构建（llmMessages → buildSystemReminder），
// 这组测试从「LLM 实际收到的 messages」这一外部可观测面来验证，
// 而不是只看谓词返回值（谓词绿不代表请求里有）。
// ---------------------------------------------------------------------------

// findSystemReminderInMessages 在一条请求的 messages 里查找 transient
// system_reminder fake tool pair，返回 reminder 文本、是否成对、是否出现。
//
// 成对判据：assistant(tool_calls[… name=system_reminder]) 之后必须有一条
// tool(message, tool_call_id 相同) —— 只有一半会让 SanitizeMessages 剔消息。
func findSystemReminderInMessages(msgs []llm.ChatMessage) (text string, paired bool, present bool) {
	for i := 0; i < len(msgs); i++ {
		m := msgs[i]
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Name != systemReminderToolName {
				continue
			}
			// 找配对的 tool 消息（引擎的 fake pair 紧邻其后）
			for j := i + 1; j < len(msgs); j++ {
				if msgs[j].Role == "tool" && msgs[j].ToolCallID == tc.ID {
					return msgs[j].Content, true, true
				}
			}
			return "", false, true
		}
	}
	return "", false, false
}

// buildReminderTurnConfig 构造一个「工具调用 N-1 次 + 最后一次纯文本收尾」的 turn。
// mock 每轮返回一个**唯一**工具调用（不复用同参数：即便 loop breaker 默认关闭，
// 也不给未来的开关留误判空间）。
func buildReminderTurnConfig(iterations int) (RunConfig, *mockLLM) {
	return buildReminderTurnConfigStart(0, iterations)
}

// buildReminderTurnConfigStart 是 buildReminderTurnConfig 的 resume 版本：
// iterStart>0 时引擎从 iterStart+1 开始编号（engine.go: i := n + s.iterStart）。
func buildReminderTurnConfigStart(iterStart, iterations int) (RunConfig, *mockLLM) {
	responses := make([]llm.LLMResponse, 0, iterations)
	for n := 1; n < iterations; n++ {
		responses = append(responses, llm.LLMResponse{
			FinishReason: llm.FinishReasonToolCalls,
			ToolCalls: []llm.ToolCall{{
				ID:        fmt.Sprintf("call_%d", n),
				Name:      "Echo",
				Arguments: fmt.Sprintf(`{"n":%d}`, n),
			}},
		})
	}
	responses = append(responses, llm.LLMResponse{Content: "done"})

	mock := &mockLLM{responses: responses}
	cfg := RunConfig{
		LLMClient: mock,
		Model:     "test-model",
		Tools:     newTestRegistry(),
		Messages:  baseMessages(),
		AgentID:   "main",
		Channel:   "web",
		ChatID:    "chat1",
		// SessionKey 让 todoKey() 非空（否则 todo 分支直接跳过，测试就测不到
		// 「空 todo 引导」这条路径）。
		SessionKey: "web:chat1",
		// resume turn：引擎首个迭代号 = IterationStart+1
		IterationStart: iterStart,
		ToolExecutor: func(_ context.Context, _ llm.ToolCall) (*tools.ToolResult, error) {
			return &tools.ToolResult{Summary: "ok"}, nil
		},
	}
	return cfg, mock
}

// reminderMatrixRow 是逐轮矩阵的一行。
type reminderMatrixRow struct {
	Iteration int    `json:"iteration"`
	Present   bool   `json:"present"`
	Paired    bool   `json:"paired"`
	TextLen   int    `json:"text_len"`
	Summary   string `json:"summary"`
	// Text 是 reminder 全文（不截断）。不进矩阵表（矩阵只看出现/配对/长度），
	// 由 TestSystemReminderInjectionMatrix 单独落盘成 reminder-text-first-iteration.txt。
	Text string `json:"-"`
}

// runReminderMatrix 跑一个 iterations 轮的 turn（iterStart=0），返回逐轮矩阵。
func runReminderMatrix(t *testing.T, iterations int) []reminderMatrixRow {
	t.Helper()
	return runReminderMatrixStart(t, 0, iterations)
}

// runReminderMatrixStart 是 resume 版本：IterationStart=iterStart，
// 第 idx 次 LLM 请求对应的 iteration 是 iterStart+1+idx。
func runReminderMatrixStart(t *testing.T, iterStart, iterations int) []reminderMatrixRow {
	t.Helper()
	cfg, mock := buildReminderTurnConfigStart(iterStart, iterations)
	out := Run(context.Background(), cfg)
	if out.Error != nil {
		t.Fatalf("iterStart=%d, %d 轮 turn 运行失败: %v", iterStart, iterations, out.Error)
	}
	if len(mock.calls) != iterations {
		t.Fatalf("LLM 请求次数 = %d, want %d（矩阵无法对齐 iteration）", len(mock.calls), iterations)
	}
	rows := make([]reminderMatrixRow, 0, iterations)
	for idx, call := range mock.calls {
		iter := iterStart + 1 + idx
		text, paired, present := findSystemReminderInMessages(call.Messages)
		row := reminderMatrixRow{Iteration: iter, Present: present, Paired: paired, TextLen: len(text), Text: text}
		if present {
			row.Summary = summarizeReminder(text)
		}
		rows = append(rows, row)
	}
	return rows
}

// summarizeReminder 给矩阵打一行摘要（前 80 字符，压掉换行）。
func summarizeReminder(text string) string {
	s := strings.ReplaceAll(text, "\n", " ")
	if r := []rune(s); len(r) > 80 {
		return string(r[:80]) + "…"
	}
	return s
}

// writeDump 在设置了 XBOT_REQ_DUMP_DIR 时把内容落盘（未设置则只是不写盘，
// 测试仍需通过 —— 证据落盘与断言解耦，CI 里不因缺目录而红）。
func writeDump(t *testing.T, filename, content string) {
	t.Helper()
	dir := os.Getenv("XBOT_REQ_DUMP_DIR")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filename, err)
	}
}

// dumpMatrix 把矩阵打到 stdout，并在设置了 XBOT_REQ_DUMP_DIR 时落盘。
// filename 由调用方给（不同轮数各写各的文件 —— 覆盖写，所以绝不能共用一个名字，
// 也不能改成 append：append 会让重复 go test 的产出叠加，证据不可复现）。
func dumpMatrix(t *testing.T, filename string, title string, rows []reminderMatrixRow) {
	t.Helper()
	var sb strings.Builder
	fmt.Fprintf(&sb, "# %s\n", title)
	fmt.Fprintf(&sb, "%-10s %-10s %-8s %-9s %s\n", "iteration", "reminder", "paired", "text_len", "summary")
	for _, r := range rows {
		fmt.Fprintf(&sb, "%-10d %-10v %-8v %-9d %s\n", r.Iteration, r.Present, r.Paired, r.TextLen, r.Summary)
	}
	t.Logf("\n%s", sb.String())
	writeDump(t, filename, sb.String())
}

// TestSystemReminderInjectionMatrix 验证首轮注入 + 稀疏节奏：
//   - 3 轮 turn：仅第 1 轮含 system_reminder（assistant tool_call + tool 结果成对）；
//   - 12 轮 turn：仅第 1、10 轮含；2–9、11–12 不含。
//
// 硬约束：绝不能每轮都注入 —— 逐轮都出现同一组「凭空冒出的工具调用」会让模型
// 误以为要反复调用 system_reminder（用户 2026-10-20 报过的问题）。
func TestSystemReminderInjectionMatrix(t *testing.T) {
	t.Run("3turn", func(t *testing.T) {
		rows := runReminderMatrix(t, 3)
		dumpMatrix(t, "per-iteration-matrix-3turn.txt", "3-turn injection matrix", rows)
		for _, r := range rows {
			want := r.Iteration == 1
			if r.Present != want {
				t.Errorf("iteration %d: reminder present = %v, want %v（首轮必建，其余轮次不得出现）", r.Iteration, r.Present, want)
			}
			if r.Present && !r.Paired {
				t.Errorf("iteration %d: reminder 未成对（assistant tool_call 无对应 tool 结果）—— SanitizeMessages 会剔掉它", r.Iteration)
			}
			if r.Present && r.TextLen == 0 {
				t.Errorf("iteration %d: reminder 文本为空 —— 伪注入，模型看不到任何内容", r.Iteration)
			}
		}
		// 首轮 reminder 全文原文（不截断）落盘：这是「模型实际看到的那段文本」，
		// 首轮空 todo 的引导措辞必须能从证据里直接读到，不能只给长度。
		first := rows[0]
		if !first.Present || first.Text == "" {
			t.Fatal("iteration 1 reminder 缺失，无法产出 reminder-text-first-iteration.txt")
		}
		writeDump(t, "reminder-text-first-iteration.txt",
			fmt.Sprintf("# iteration 1 system_reminder 全文（len=%d bytes / %d runes）\n\n%s\n",
				len(first.Text), len([]rune(first.Text)), first.Text))
	})

	t.Run("12turn", func(t *testing.T) {
		rows := runReminderMatrix(t, 12)
		dumpMatrix(t, "per-iteration-matrix-12turn.txt", "12-turn injection matrix", rows)
		wantSet := map[int]bool{1: true, 10: true}
		for _, r := range rows {
			want := wantSet[r.Iteration]
			if r.Present != want {
				t.Errorf("iteration %d: reminder present = %v, want %v（序列必须是 1, 10, 20 …）", r.Iteration, r.Present, want)
			}
			if r.Present && (!r.Paired || r.TextLen == 0) {
				t.Errorf("iteration %d: reminder 不完整（paired=%v len=%d）", r.Iteration, r.Paired, r.TextLen)
			}
		}
	})
}

// TestSystemReminderLazyBuild 确认构建是**真懒**：非插入轮不得调用
// ListActiveFn（跨进程回调，有真实开销）。这是「先判 iteration 再构建」顺序的
// 行为级证据 —— 把顺序写反了这个测试会红。
func TestSystemReminderLazyBuild(t *testing.T) {
	cfg, _ := buildReminderTurnConfig(12)
	var mu sync.Mutex
	var listActiveCalls []string
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		ListActiveFn: func(channel, chatID string) []SubAgentStatus {
			mu.Lock()
			defer mu.Unlock()
			listActiveCalls = append(listActiveCalls, fmt.Sprintf("%s/%s", channel, chatID))
			return nil
		},
	}
	out := Run(context.Background(), cfg)
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}
	// 12 轮里只有 iteration 1 和 10 是插入轮 → 最多 2 次（每次插入轮构建一次）。
	if len(listActiveCalls) > 2 {
		t.Fatalf("ListActiveFn 被调用 %d 次，期望 ≤2（仅插入轮 1/10 构建）—— 构建不懒，每轮都在拉状态", len(listActiveCalls))
	}
	if len(listActiveCalls) == 0 {
		t.Fatal("ListActiveFn 从未被调用 —— 说明插入轮根本没构建 reminder")
	}
	t.Logf("ListActiveFn calls = %d（插入轮 1/10，非插入轮不构建）", len(listActiveCalls))
}

// reqBodyDump 是一条被捕获的请求体。
type reqBodyDump struct {
	Index int
	Body  string
}

// fakeChatCompletionServer 启动一个假的 OpenAI chat_completions 上游：前
// toolCalls 次请求返回 tool_call，之后返回纯文本收尾。
// 每条请求的原始 body 都记录下来（这就是「模型真正看到的东西」）。
func fakeChatCompletionServer(t *testing.T, toolCalls int) (*httptest.Server, func() []reqBodyDump) {
	t.Helper()
	var mu sync.Mutex
	var dumps []reqBodyDump
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"test-model","object":"model"}]}`)
			return
		}
		mu.Lock()
		idx := len(dumps)
		dumps = append(dumps, reqBodyDump{Index: idx, Body: string(body)})
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if idx < toolCalls {
			fmt.Fprintf(w, `{"id":"cmpl-%d","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"Echo","arguments":"{\"n\":%d}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, idx, idx+1, idx+1)
			return
		}
		fmt.Fprintf(w, `{"id":"cmpl-%d","object":"chat.completion","created":1,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`, idx)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []reqBodyDump {
		mu.Lock()
		defer mu.Unlock()
		out := make([]reqBodyDump, len(dumps))
		copy(out, dumps)
		return out
	}
}

// wireMessage 是 OpenAI chat body 里 message 的最小视图。
type wireMessage struct {
	Role      string `json:"role"`
	Content   any    `json:"content"`
	ToolCalls []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
	ToolCallID string `json:"tool_call_id"`
}

type wireBody struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []wireMessage `json:"messages"`
}

// reminderInBody 在真实 HTTP body 的 messages 里找 system_reminder 对。
func reminderInBody(t *testing.T, raw string) (found bool, paired bool, text string) {
	t.Helper()
	var body wireBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("请求 body 不是合法 JSON: %v\nbody=%s", err, raw)
	}
	for i, m := range body.Messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != systemReminderToolName {
				continue
			}
			for j := i + 1; j < len(body.Messages); j++ {
				if body.Messages[j].Role == "tool" && body.Messages[j].ToolCallID == tc.ID {
					if s, ok := body.Messages[j].Content.(string); ok {
						return true, true, s
					}
					return true, true, fmt.Sprintf("%v", body.Messages[j].Content)
				}
			}
			return true, false, ""
		}
	}
	return false, false, ""
}

// TestSystemReminderInjection25Iterations 是稀疏节奏的最强判据：25 轮 turn 里
// 注入点必须**恰好**是 1 / 10 / 20，其余 22 轮一律没有。矩阵打印全部 25 轮。
//
// 口径（首辅 2026-10-28 裁定）：注入点是 1/10/20，**不是** 1/11/21 —— 后者只能
// 由「第 N 轮末尾生成、第 N+1 轮消费」的旧相位结构导出，那套结构已作废。
func TestSystemReminderInjection25Iterations(t *testing.T) {
	rows := runReminderMatrix(t, 25)
	dumpMatrix(t, "per-iteration-matrix-25turn.txt", "25-turn injection matrix", rows)

	wantHit := map[int]bool{1: true, 10: true, 20: true}
	var hits []int
	for _, r := range rows {
		if r.Present {
			hits = append(hits, r.Iteration)
		}
		if r.Present != wantHit[r.Iteration] {
			t.Errorf("iteration %d: reminder present = %v, want %v", r.Iteration, r.Present, wantHit[r.Iteration])
		}
		if r.Present && (!r.Paired || r.TextLen == 0) {
			t.Errorf("iteration %d: reminder 不完整（paired=%v len=%d）", r.Iteration, r.Paired, r.TextLen)
		}
	}
	if fmt.Sprint(hits) != fmt.Sprint([]int{1, 10, 20}) {
		t.Fatalf("注入点 = %v, want [1 10 20]", hits)
	}
	t.Logf("injection points over 25 iterations: %v", hits)
}

// TestSystemReminderNoAdjacentIterations 是 2026-10-20 事故的回归：
// 当时 reminder 每轮都注入（字段跨迭代保留），模型每轮都看到一组「凭空冒出的
// 工具调用」，误以为要反复调用 system_reminder。
//
// 判据：24 轮 turn 里**任意相邻两轮不得同时为 true**。
//
// 判别力（变异检验 D5）：把 shouldInsertSystemReminder 改成恒 return true，
// 这个用例必须变红（相邻两轮全为 true）—— 因此它不能被写成「只要出现就算过」。
func TestSystemReminderNoAdjacentIterations(t *testing.T) {
	rows := runReminderMatrix(t, 24)
	dumpMatrix(t, "per-iteration-matrix-24turn-adjacency.txt", "24-turn adjacency matrix", rows)

	prevHit := false
	prevIter := 0
	for _, r := range rows {
		if r.Present && prevHit {
			t.Fatalf("iteration %d 与 %d 连续两轮都注入了 system_reminder —— 2026-10-20 事故形态（逐轮注入）", prevIter, r.Iteration)
		}
		prevHit = r.Present
		prevIter = r.Iteration
	}
	// 反向判据：不能因为「从不注入」而让上面的检查空过。
	var total int
	for _, r := range rows {
		if r.Present {
			total++
		}
	}
	if total != 3 { // 24 轮：1, 10, 20
		t.Fatalf("24 轮注入次数 = %d, want 3（1/10/20）—— 过少说明谓词被改瘫，过多说明逐轮注入", total)
	}
	t.Logf("24-turn: %d injections, no adjacent pairs", total)
}

// TestSystemReminderLLMMessagesIdempotent 验证同一 iteration 连调两次
// llmMessages 的结果逐字节一致（幂等）：reminder 内容里不得混入时间戳、随机量
// 或自增计数。幂等是「同一轮重试不产生不同 prompt」的前提，也让矩阵可复现。
func TestSystemReminderLLMMessagesIdempotent(t *testing.T) {
	cfg, _ := buildReminderTurnConfig(1)
	s := newRunState(cfg)
	s.initDynamicInjector()

	// 非污染判据：llmMessages 只能返回新切片，不得 append 进 s.messages。
	// 只比字符串会漏掉「顺手 append 到源切片」这种错 —— 那会把 fake pair 写进
	// 持久化视图，watermark 虚高（历史事故：AskUser 报 no pending messages）。
	before := len(s.messages)
	beforeMsgs := len(s.messages)
	for i := 0; i < 3; i++ {
		_ = s.llmMessages(1)
	}
	if len(s.messages) != beforeMsgs {
		t.Fatalf("llmMessages 改动了 s.messages 长度：%d → %d（源切片被污染）", beforeMsgs, len(s.messages))
	}
	t.Logf("s.messages length stable at %d across 3 llmMessages calls (iteration 1)", before)

	for _, iter := range []int{1, 2, 10} {
		first := s.llmMessages(iter)
		second := s.llmMessages(iter)
		if len(first) != len(second) {
			t.Fatalf("iteration %d: 两次调用条数不同 %d vs %d", iter, len(first), len(second))
		}
		for i := range first {
			if first[i].Role != second[i].Role || first[i].Content != second[i].Content || first[i].ToolCallID != second[i].ToolCallID {
				t.Fatalf("iteration %d: 第 %d 条消息两次调用不一致\nfirst=%+v\nsecond=%+v", iter, i, first[i], second[i])
			}
			if len(first[i].ToolCalls) != len(second[i].ToolCalls) {
				t.Fatalf("iteration %d: 第 %d 条消息 tool_calls 数量不一致", iter, i)
			}
			for j := range first[i].ToolCalls {
				a, b := first[i].ToolCalls[j], second[i].ToolCalls[j]
				if a.ID != b.ID || a.Name != b.Name || a.Arguments != b.Arguments {
					t.Fatalf("iteration %d: 第 %d 条 tool_call[%d] 不一致: %+v vs %+v", iter, i, j, a, b)
				}
			}
		}
		t.Logf("iteration %d: %d messages, byte-identical across two calls", iter, len(first))
	}

	// s.messages 必须零污染：llmMessages 不得把 fake pair 写回源切片。
	for _, m := range s.messages {
		for _, tc := range m.ToolCalls {
			if tc.Name == systemReminderToolName {
				t.Fatal("llmMessages 污染了 s.messages（fake pair 被写回源切片）")
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 真实 HTTP 链路：断言实际请求 body 里带着 system_reminder 对
// ---------------------------------------------------------------------------

// TestSystemReminderHTTPBody 用真实 llm.NewOpenAILLM（Stream:false）+ httptest
// 假上游，捕获**实际 HTTP body JSON**：断言 body 里存在带 "system_reminder" 的
// assistant tool_call 及对应 tool 结果，且只出现在首轮。
//
// 需要 XBOT_REQ_DUMP_DIR 指定落盘目录（未设置则 skip —— 这测试的价值在于
// 留下可复查的 body 证据，不给目录就等于白跑）。
func TestSystemReminderHTTPBody(t *testing.T) {
	dumpDir := os.Getenv("XBOT_REQ_DUMP_DIR")
	if dumpDir == "" {
		t.Skip("XBOT_REQ_DUMP_DIR 未设置 —— 跳过 HTTP body 证据测试")
	}

	srv, dumpsFn := fakeChatCompletionServer(t, 2 /* 前两轮 tool_call，第三轮收尾 */)
	client := llm.NewOpenAILLM(llm.OpenAIConfig{
		BaseURL:      srv.URL + "/v1",
		APIKey:       "test-key",
		DefaultModel: "test-model",
		APIType:      llm.APITypeChatCompletions,
	})

	out := Run(context.Background(), RunConfig{
		LLMClient: client,
		Model:     "test-model",
		// 注册一个真实工具，让请求 body 里带 tools 字段 —— 否则「模型该用
		// TodoWrite 建计划」这条指令缺少可调用的工具面，测的不是真实形态。
		Tools:      newTestRegistry(&mockTool{name: "Echo", result: &tools.ToolResult{Summary: "ok"}}),
		Messages:   baseMessages(),
		AgentID:    "main",
		Channel:    "web",
		ChatID:     "chat1",
		SessionKey: "web:chat1",
		Stream:     false, // 走 Generate（非流式），body 是普通 JSON
		ToolExecutor: func(_ context.Context, _ llm.ToolCall) (*tools.ToolResult, error) {
			return &tools.ToolResult{Summary: "ok"}, nil
		},
	})
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}

	dumps := dumpsFn()
	if len(dumps) != 3 {
		t.Fatalf("上游收到 %d 条请求，期望 3（3 轮 turn）", len(dumps))
	}

	var sb strings.Builder
	for _, d := range dumps {
		fmt.Fprintf(&sb, "===== request #%d (iteration %d) =====\n%s\n\n", d.Index+1, d.Index+1, d.Body)
	}
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dumpDir, err)
	}
	if err := os.WriteFile(filepath.Join(dumpDir, "req-bodies-3turn.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write req-bodies-3turn.txt: %v", err)
	}

	for _, d := range dumps {
		iter := d.Index + 1
		found, paired, text := reminderInBody(t, d.Body)
		t.Logf("iteration %d: system_reminder in body = %v (paired=%v, len=%d)", iter, found, paired, len(text))
		want := iter == 1
		if found != want {
			t.Errorf("iteration %d: HTTP body 里 system_reminder = %v, want %v", iter, found, want)
		}
		if found {
			if !paired {
				t.Errorf("iteration %d: body 里只有 assistant tool_call，没有对应 tool 结果 —— 上游会 400（tool_call 未响应）", iter)
			}
			if !strings.Contains(text, "<system-reminder") {
				t.Errorf("iteration %d: tool 结果不是 reminder 正文: %q", iter, text)
			}
		}
	}

	// 首轮 body 必须真的带着工具定义（否则「模型该用 TodoWrite」无从谈起）
	if !strings.Contains(dumps[0].Body, `"tools"`) {
		t.Error("首轮 body 不含 tools 字段 —— 请求构造异常")
	}
}

// ---------------------------------------------------------------------------
// G1（2026-10-28 御前定稿）：resume turn 的首轮注入 + 六行参数化矩阵
// ---------------------------------------------------------------------------

// TestSystemReminderInjectionMatrixParameterized 覆盖六种 iterStart。
//
// 规则：首轮 = iterStart+1 必插；其后每 10 轮一次；首轮与下一个 10k **相邻**时
// 跳过该 10k（否则会连续两轮注入，破「任意相邻两轮不同时注入」的硬不变量）。
// 每行统一跑 20 轮（从 iterStart+1 起），因此 30/40 不会进入窗口。
func TestSystemReminderInjectionMatrixParameterized(t *testing.T) {
	cases := []struct {
		iterStart int
		want      []int
	}{
		{0, []int{1, 10, 20}},
		{1, []int{2, 10, 20}},
		{4, []int{5, 10, 20}},
		{8, []int{9, 20}},   // 10 与首轮 9 相邻 → 跳过
		{18, []int{19, 30}}, // 20 与首轮 19 相邻 → 跳过
		{9, []int{10, 20}},  // 首轮正好是 10，重合只算一次
	}
	for _, c := range cases {
		rows := runReminderMatrixStart(t, c.iterStart, 20)
		var hits []int
		for _, r := range rows {
			if r.Present {
				hits = append(hits, r.Iteration)
				if !r.Paired || r.TextLen == 0 {
					t.Errorf("iterStart=%d iteration %d: reminder 不完整（paired=%v len=%d）",
						c.iterStart, r.Iteration, r.Paired, r.TextLen)
				}
			}
		}
		if fmt.Sprint(hits) != fmt.Sprint(c.want) {
			t.Errorf("iterStart=%d 注入点 = %v, want %v", c.iterStart, hits, c.want)
		}
		// 相邻轮不变量对每种 iterStart 都必须成立
		prev := false
		for _, r := range rows {
			if r.Present && prev {
				t.Fatalf("iterStart=%d iteration %d 与上一轮连续注入 —— 违反「相邻两轮不得同时注入」", c.iterStart, r.Iteration)
			}
			prev = r.Present
		}
		t.Logf("iterStart=%d → 注入点 %v（20 轮，无相邻）", c.iterStart, hits)
	}
}

// TestSystemReminderAdjacentSkipOnResume 是六行表中的两行（8/18）的独立守卫：
// 跳过相邻 10k 的规则若被删掉，这里会红（9、10 或 19、20 连续两轮注入）。
func TestSystemReminderAdjacentSkipOnResume(t *testing.T) {
	for _, iterStart := range []int{8, 18} {
		first := iterStart + 1
		rows := runReminderMatrixStart(t, iterStart, 20)
		if len(rows) != 20 {
			t.Fatalf("iterStart=%d: 行数 = %d", iterStart, len(rows))
		}
		// 首轮必须注入
		if !rows[0].Present {
			t.Fatalf("iterStart=%d: 首轮 %d 未注入 reminder", iterStart, first)
		}
		// 下一个 10k（= first+1）必须被跳过
		if rows[1].Present {
			t.Fatalf("iterStart=%d: iteration %d（首轮的相邻 10k）仍注入了 —— 会连续两轮注入", iterStart, rows[1].Iteration)
		}
		t.Logf("iterStart=%d: 首轮 %d 注入，相邻 10k %d 已跳过", iterStart, first, first+1)
	}
}

// TestSystemReminderHTTPBodyResumeTurn 用真实 llm.NewOpenAILLM + httptest 捕获
// iterStart=1 的 resume turn 请求体：iteration 2（首轮）必须含 fake pair，
// 3/4 不含。与 iterStart=0 的那份（req-bodies-3turn.txt）成对留证。
func TestSystemReminderHTTPBodyResumeTurn(t *testing.T) {
	dumpDir := os.Getenv("XBOT_REQ_DUMP_DIR")
	if dumpDir == "" {
		t.Skip("XBOT_REQ_DUMP_DIR 未设置 —— 跳过 resume HTTP body 证据测试")
	}

	srv, dumpsFn := fakeChatCompletionServer(t, 2 /* 前两轮 tool_call，第三轮收尾 */)
	client := llm.NewOpenAILLM(llm.OpenAIConfig{
		BaseURL:      srv.URL + "/v1",
		APIKey:       "test-key",
		DefaultModel: "test-model",
		APIType:      llm.APITypeChatCompletions,
	})

	out := Run(context.Background(), RunConfig{
		LLMClient:      client,
		Model:          "test-model",
		Tools:          newTestRegistry(),
		Messages:       baseMessages(),
		AgentID:        "main",
		Channel:        "web",
		ChatID:         "chat1",
		SessionKey:     "web:chat1",
		Stream:         false,
		IterationStart: 1, // resume turn：首个迭代号 = 2
		ToolExecutor: func(_ context.Context, _ llm.ToolCall) (*tools.ToolResult, error) {
			return &tools.ToolResult{Summary: "ok"}, nil
		},
	})
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}

	dumps := dumpsFn()
	if len(dumps) != 3 {
		t.Fatalf("上游收到 %d 条请求，期望 3（resume turn 3 轮）", len(dumps))
	}
	var sb strings.Builder
	for _, d := range dumps {
		fmt.Fprintf(&sb, "===== request #%d (iteration %d, iterStart=1) =====\n%s\n\n", d.Index+1, d.Index+2, d.Body)
	}
	if err := os.MkdirAll(dumpDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dumpDir, err)
	}
	if err := os.WriteFile(filepath.Join(dumpDir, "req-bodies-resume-3turn.txt"), []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write req-bodies-resume-3turn.txt: %v", err)
	}

	for _, d := range dumps {
		iter := d.Index + 2
		found, paired, text := reminderInBody(t, d.Body)
		t.Logf("iteration %d: system_reminder in body = %v (paired=%v, len=%d)", iter, found, paired, len(text))
		want := iter == 2 // resume turn 的首轮
		if found != want {
			t.Errorf("iteration %d: HTTP body 里 system_reminder = %v, want %v", iter, found, want)
		}
		if found && !paired {
			t.Errorf("iteration %d: body 里只有 assistant tool_call，没有对应 tool 结果", iter)
		}
	}
}
