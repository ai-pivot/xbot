package agent

// system_reminder_form_test —— N2（2026-10-30）新形态的直接判据：
// reminder 以**尾部内容拼接**注入，而不是「assistant(tool_calls[system_reminder])
// + tool 结果」的假消息对。
//
// 判据覆盖（对应派发单 §5 a/b/c/d + 附加不变量）：
//   a. 请求里不存在 system_reminder 伪工具调用；末条是 user 且尾含带 role 的 marker；
//   b. 多工具循环每轮恰好 1 处 marker，且位于末条消息；
//   c. 纯视图：s.messages 逐字节不变，且内部 0 处 marker；
//   d. 末条是 assistant（无 tool_calls）时，该条 content 逐字节不变，
//      reminder 出现在**新增的** user 消息里；
//   附. 不变量：到达 llmMessages 的 s.messages 不得以「assistant + 未配对 tool_calls」结尾
//      （本路径结构上不可能 —— callLLM 只在工具执行完/请求结束后调用；此处用请求体
//      反向锁住，循环被改坏时立刻红）。
// ---------------------------------------------------------------------------

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"xbot/llm"
	"xbot/protocol"
)

// messagesFingerprint 对消息切片做确定性指纹（逐元素 json.Marshal 后 sha256）。
// 用来证明 llmMessages 是纯视图：源切片连一个字节都没动。
func messagesFingerprint(t *testing.T, msgs []llm.ChatMessage) string {
	t.Helper()
	h := sha256.New()
	for i, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal messages[%d]: %v", i, err)
		}
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// newFormRunState 造一个不跑 Run 的 runState（只看 llmMessages 的纯函数行为）。
func newFormRunState(t *testing.T, msgs []llm.ChatMessage) *runState {
	t.Helper()
	cfg, _ := buildReminderTurnConfig(1)
	cfg.Messages = msgs
	s := newRunState(cfg)
	s.initDynamicInjector()
	return s
}

// assertNoDanglingToolCalls 是附加不变量的机械检查：请求里每条
// assistant(tool_calls) 都必须被后续 tool 消息逐条应答，且请求不得以
// 「assistant + 未配对 tool_calls」结尾。
//
// 结构上这是必然（callLLM 只在工具执行完或请求结束后调用），这里加用例是为了
// 让「循环被改坏」变成一条红用例，而不是靠人肉推理。
func assertNoDanglingToolCalls(t *testing.T, label string, msgs []llm.ChatMessage) {
	t.Helper()
	answered := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	for i, m := range msgs {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if !answered[tc.ID] {
				t.Fatalf("%s: messages[%d] 的 tool_call %q(%s) 没有对应 tool 结果 —— 悬空 tool_calls（循环被改坏？）",
					label, i, tc.Name, tc.ID)
			}
		}
	}
	if n := len(msgs); n > 0 {
		last := msgs[n-1]
		if last.Role == "assistant" && len(last.ToolCalls) > 0 {
			t.Fatalf("%s: 请求以「assistant + tool_calls」结尾（tool_call 未配对）", label)
		}
	}
}

// --- a/d: 末条 user → 拼进它的副本 ---

func TestReminderForm_UserTailAppended(t *testing.T) {
	origContent := "fix the login bug"
	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("you are a test agent"),
		llm.NewUserMessage(origContent),
	}
	s := newFormRunState(t, msgs)
	sourceBefore := s.messages[1].Content

	view := s.llmMessages(1)
	if len(view) != len(msgs) {
		t.Fatalf("末条是 user 时不得新增消息：条数 %d, want %d", len(view), len(msgs))
	}
	last := view[len(view)-1]
	if last.Role != "user" {
		t.Fatalf("末条 role = %q, want user", last.Role)
	}
	idx := strings.Index(last.Content, reminderMarker)
	if idx < 0 {
		t.Fatalf("末条 content 里没有 %q；内容=%.200q", reminderMarker, last.Content)
	}
	if !strings.HasPrefix(last.Content, origContent) {
		t.Fatalf("原 user 内容被改写：%.100q", last.Content)
	}
	if got := last.Content[len(origContent):idx]; got != "\n\n" {
		t.Fatalf("reminder 与原内容之间的分隔 = %q, want %q", got, "\n\n")
	}
	if !strings.HasSuffix(strings.TrimRight(last.Content, "\n"), reminderCloseTag) {
		t.Errorf("reminder 不在内容结尾（尾部拼接判据）")
	}
	// 源切片逐字节不动
	if s.messages[1].Content != sourceBefore {
		t.Fatalf("s.messages 被污染：%q → %q", sourceBefore, s.messages[1].Content)
	}
	if n := countReminderMarkers(s.messages); n != 0 {
		t.Fatalf("s.messages 里出现 %d 处 marker —— 纯视图被破坏", n)
	}
	// 0 处伪工具调用
	if hits := findFakeReminderToolCalls(view); len(hits) > 0 {
		t.Fatalf("视图里仍有伪工具调用: %v", hits)
	}
	t.Logf("user tail: %d msgs, marker@%d (content len %d → %d)", len(view), idx, len(sourceBefore), len(last.Content))
}

// --- d: 末条 assistant（无 tool_calls）→ 不污染它，追加独立 user ---

func TestReminderForm_AssistantTailUnpolluted(t *testing.T) {
	assistantContent := "I fixed it. Tests pass."
	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("you are a test agent"),
		llm.NewUserMessage("fix the login bug"),
		llm.NewAssistantMessage(assistantContent),
	}
	s := newFormRunState(t, msgs)
	idxBefore := len(s.messages) - 1

	view := s.llmMessages(1)
	if len(view) != len(msgs)+1 {
		t.Fatalf("末条是 assistant 时必须追加一条 user：条数 %d, want %d", len(view), len(msgs)+1)
	}
	// 原 assistant 逐字节相同（这是判据 d 的核心：绝不拼进模型自己的发言）
	if view[idxBefore].Role != "assistant" || view[idxBefore].Content != assistantContent {
		t.Fatalf("assistant content 被污染：role=%q content=%q, want %q",
			view[idxBefore].Role, view[idxBefore].Content, assistantContent)
	}
	// 同时源切片也没动
	if s.messages[idxBefore].Content != assistantContent {
		t.Fatalf("s.messages 的 assistant content 被污染: %q", s.messages[idxBefore].Content)
	}
	last := view[len(view)-1]
	if last.Role != "user" {
		t.Fatalf("新增末条 role = %q, want user", last.Role)
	}
	blk, ok := reminderBlock(last.Content)
	if !ok {
		t.Fatalf("新增 user 消息不含 reminder block: %.200q", last.Content)
	}
	if blk != last.Content {
		t.Errorf("追加的 user 消息 content 必须就是 reminder 本身（不得加包装/前缀）")
	}
	if hits := findFakeReminderToolCalls(view); len(hits) > 0 {
		t.Fatalf("视图里仍有伪工具调用: %v", hits)
	}
	t.Logf("assistant tail untouched (len %d bytes), appended user msg carrying %d bytes reminder",
		len(assistantContent), len(blk))
}

// --- tool 结尾 → 追加独立 user（绝不拼进 tool 结果）---

func TestReminderForm_ToolTailAppendsUserMessage(t *testing.T) {
	toolContent := `{"ok":true}`
	assistantMsg := llm.NewAssistantMessage("")
	assistantMsg.ToolCalls = []llm.ToolCall{{ID: "call_1", Name: "Echo", Arguments: `{"n":1}`}}
	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("you are a test agent"),
		llm.NewUserMessage("do it"),
		assistantMsg,
		llm.NewToolMessage("Echo", "call_1", `{"n":1}`, toolContent),
	}
	s := newFormRunState(t, msgs)

	view := s.llmMessages(1)
	if len(view) != len(msgs)+1 {
		t.Fatalf("末条是 tool 时必须追加一条 user：条数 %d, want %d", len(view), len(msgs)+1)
	}
	if view[len(view)-2].Role != "tool" || view[len(view)-2].Content != toolContent {
		t.Fatalf("tool 结果被污染：%+v", view[len(view)-2])
	}
	last := view[len(view)-1]
	if last.Role != "user" {
		t.Fatalf("新增末条 role = %q, want user", last.Role)
	}
	if _, ok := reminderBlock(last.Content); !ok {
		t.Fatalf("追加的 user 消息不含 reminder block: %.200q", last.Content)
	}
	if strings.Contains(view[len(view)-2].Content, reminderMarker) {
		t.Fatal("reminder 被拼进了 tool 结果 —— 模型会以为工具输出里带提醒")
	}
	assertNoDanglingToolCalls(t, "tool-tail view", view)
}

// --- c: 纯视图 ---

func TestReminderForm_PureView(t *testing.T) {
	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("you are a test agent"),
		llm.NewUserMessage("do it"),
		llm.NewAssistantMessage("working"),
		llm.NewToolMessage("Echo", "call_x", `{}`, "ok"),
	}
	// 制造一点 memory 风格的数据：裸 <system-reminder> 是合法数据，绝不能被当成判据
	msgs = append(msgs, llm.NewUserMessage("继续\n\n<system-reminder><![CDATA[# Memory\n\ncore fact]]></system-reminder>"))
	s := newFormRunState(t, msgs)

	before := messagesFingerprint(t, s.messages)
	beforeLen := len(s.messages)
	for i := 1; i <= 6; i++ {
		view := s.llmMessages(i)
		if view == nil {
			t.Fatalf("第 %d 次调用返回 nil", i)
		}
		// iter 1 是插入轮（shouldInsertSystemReminder），2..6 不是 —— 两种路径
		// 都必须保持纯视图。
	}
	after := messagesFingerprint(t, s.messages)
	if before != after {
		t.Fatalf("s.messages 指纹变了：%s → %s（llmMessages 不是纯视图）", before, after)
	}
	if len(s.messages) != beforeLen {
		t.Fatalf("s.messages 条数变了：%d → %d", beforeLen, len(s.messages))
	}
	if n := countReminderMarkers(s.messages); n != 0 {
		t.Fatalf("s.messages 里出现 %d 处带 role 的 marker", n)
	}
	// 裸标签必须原样保留（memory 注入是合法数据，本改造不许碰它的标签）
	if !strings.Contains(s.messages[beforeLen-1].Content, "<system-reminder><![CDATA[# Memory") {
		t.Fatal("memory 注入的裸 <system-reminder> 被改动 —— 越界")
	}
	t.Logf("6 calls → s.messages sha256=%s unchanged (%d msgs)", before, beforeLen)
}

// --- a/b: 真实 Run 的逐轮请求（稀疏插入轮 + 尾部落点）---

func TestReminderForm_InsertionRoundSingleTailReminder(t *testing.T) {
	const iterations = 3
	cfg, mock := buildReminderTurnConfig(iterations)
	out := Run(context.Background(), cfg)
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}
	if len(mock.calls) != iterations {
		t.Fatalf("LLM 请求 %d 次, want %d", len(mock.calls), iterations)
	}
	for idx, call := range mock.calls {
		iter := idx + 1
		msgs := call.Messages
		// 频率判据（N2a 单独钉住）：3 轮 turn 只有 iteration 1 是插入轮。
		wantMarkers := 0
		if iter == 1 {
			wantMarkers = 1
		}
		if n := countReminderMarkers(msgs); n != wantMarkers {
			t.Fatalf("iteration %d: marker 出现 %d 次, want %d（频率应为 1/10/20）", iter, n, wantMarkers)
		}
		if wantMarkers == 1 {
			last := msgs[len(msgs)-1]
			if last.Role != "user" {
				t.Fatalf("iteration %d: 末条 role=%q, want user", iter, last.Role)
			}
			if _, ok := reminderBlock(last.Content); !ok {
				t.Fatalf("iteration %d: reminder 不在末条消息里", iter)
			}
		}
		if hits := findFakeReminderToolCalls(msgs); len(hits) > 0 {
			t.Fatalf("iteration %d: 请求里仍有伪工具调用: %v", iter, hits)
		}
		assertNoDanglingToolCalls(t, fmt.Sprintf("iteration %d", iter), msgs)
	}
	t.Logf("%d rounds: only the insertion round carries a tail reminder; no fake tool pair, no dangling tool_calls", iterations)
}

// --- 附加不变量：更大规模的多工具循环里也不得出现悬空 tool_calls ---

func TestReminderForm_NoDanglingToolCallsOverLongLoop(t *testing.T) {
	const iterations = 8
	cfg, mock := buildReminderTurnConfig(iterations)
	out := Run(context.Background(), cfg)
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}
	for idx, call := range mock.calls {
		iter := idx + 1
		assertNoDanglingToolCalls(t, fmt.Sprintf("iteration %d", iter), call.Messages)
		// 8 轮 turn：插入轮只有 iteration 1（10 尚未到）；其余轮 0 处。
		want := 0
		if iter == 1 {
			want = 1
		}
		if n := countReminderMarkers(call.Messages); n != want {
			t.Fatalf("iteration %d: marker %d 处, want %d", iter, n, want)
		}
	}
	t.Logf("%d rounds: invariant holds (assistant tool_calls always answered before next request)", iterations)
}

// --- 空消息：无可注入内容 ---

func TestReminderForm_EmptyMessagesNoReminder(t *testing.T) {
	s := newFormRunState(t, nil)
	if got := s.llmMessages(1); len(got) != 0 {
		t.Fatalf("空 messages 不应注入 reminder，got %d 条", len(got))
	}
}

// --- 开销：reminder 从「1/10 轮」变成「每轮」后的直接成本 ---
//
// 只测 BuildSystemReminder 本身（纯函数）：ListActiveFn 的结果以切片形式喂入，
// 跨进程回调的耗时不在本基准内（它是 subagent 服务的 RTT，与本次改造无关，
// 但调用次数从 3 次/25 轮变成 25 次/25 轮 —— 换算时按次数乘）。
//
//	go test ./agent/ -run '^$' -bench 'BenchmarkBuildSystemReminder' -benchtime=10000x
func BenchmarkBuildSystemReminder(b *testing.B) {
	msgs := []llm.ChatMessage{
		llm.NewSystemMessage("You are xbot, an agent working in a repo."),
		llm.NewUserMessage("修一下 agent/engine_run.go 里 reminder 的落点问题，然后跑测试验证"),
	}
	for i := 0; i < 20; i++ {
		msgs = append(msgs,
			llm.NewAssistantMessage(fmt.Sprintf("step %d done", i)),
			llm.NewToolMessage("Echo", fmt.Sprintf("call_%d", i), "{}", fmt.Sprintf("result %d", i)))
	}
	todoItems := []TodoProgressItem{
		{Text: "读现有实现", Status: "completed"},
		{Text: "改 llmMessages", Status: "in_progress"},
		{Text: "重写测试", Status: "pending"},
		{Text: "跑门禁", Status: "pending"},
	}
	goal := &protocol.GoalInfo{Status: "active", Summary: "N2 system_reminder 形态改造"}
	subAgents := make([]SubAgentStatus, 0, 5)
	for i := 0; i < 5; i++ {
		subAgents = append(subAgents, SubAgentStatus{Role: "explore", Instance: fmt.Sprintf("worker-%d", i), Running: i%2 == 0})
	}

	cases := []struct {
		name      string
		subAgents []SubAgentStatus
	}{
		{"P0-nil", nil},
		{"P1-5sub", subAgents},
	}
	for _, c := range cases {
		b.Run(c.name, func(b *testing.B) {
			out := ""
			durs := make([]time.Duration, 0, b.N)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				t0 := time.Now()
				out = BuildSystemReminder(msgs, todoItems, goal, "main", "/home/smith/src/.xbot-worktrees/sr-form", "sr-form", c.subAgents)
				durs = append(durs, time.Since(t0))
			}
			b.StopTimer()
			sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
			p := func(q float64) time.Duration { return durs[int(float64(len(durs)-1)*q)] }
			b.ReportMetric(float64(p(0.50).Nanoseconds()), "p50-ns")
			b.ReportMetric(float64(p(0.95).Nanoseconds()), "p95-ns")
			if len(out) == 0 {
				b.Fatal("empty reminder")
			}
		})
	}
}

// --- d（函数级）：appendReminderToTail 不改入参、不产生 user→user 连续两条 ---

func TestAppendReminderToTail_Shapes(t *testing.T) {
	reminder := reminderMarker + "<note>AUTO-INJECTED</note>" + reminderCloseTag
	userMsg := llm.NewUserMessage("hello")
	src := []llm.ChatMessage{llm.NewSystemMessage("sys"), userMsg}
	before := messagesFingerprint(t, src)

	out := appendReminderToTail(src, reminder, 0)
	if len(out) != 2 {
		t.Fatalf("末条 user → 应就地追加内容，条数 %d", len(out))
	}
	if got := messagesFingerprint(t, src); got != before {
		t.Fatalf("appendReminderToTail 改动了入参切片：%s → %s", before, got)
	}
	if out[1].Content != "hello\n\n"+reminder {
		t.Fatalf("内容拼接不符：%q", out[1].Content)
	}

	// 末条 user 且内容为空 → 直接放 reminder，不留前导空行
	empty := []llm.ChatMessage{{Role: "user", Content: ""}}
	out2 := appendReminderToTail(empty, reminder, 0)
	if len(out2) != 1 || out2[0].Content != reminder {
		t.Fatalf("空 user 内容应直接放 reminder，got %+v", out2)
	}

	// 末条 assistant → 追加一条 user，不产生 user→user 相邻
	asst := []llm.ChatMessage{llm.NewUserMessage("hi"), llm.NewAssistantMessage("done")}
	out3 := appendReminderToTail(asst, reminder, 7)
	if len(out3) != 3 || out3[2].Role != "user" || out3[2].TurnID != 7 {
		t.Fatalf("assistant 结尾应追加 user(TurnID=7)，got %+v", out3)
	}
	if out3[1].Content != "done" {
		t.Fatalf("assistant content 被污染: %q", out3[1].Content)
	}

	// 空切片 → 原样返回
	if out4 := appendReminderToTail(nil, reminder, 0); len(out4) != 0 {
		t.Fatalf("空切片应原样返回，got %d", len(out4))
	}
}
