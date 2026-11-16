package agent

// system_reminder_injection_test —— 从「LLM 实际收到的 messages / HTTP body」
// 这一外部可观测面验证 reminder 的**新形态**（2026-10-30 N2）。
//
// 形态变更：reminder 不再是「assistant(tool_calls[system_reminder]) + tool 结果」
// 的假消息对，而是**尾部内容拼接** —— 末条是 user 时拼进它的 content 副本，
// 末条是 tool / assistant 时追加一条独立 user 消息。见 engine_run.go
// 的 llmMessages / appendReminderToTail。
//
// 频率变更：每轮都注入（旧的 1/10/20 稀疏节奏与 shouldInsertSystemReminder
// 一起删除）。
//
// ⛔ 判别串必须带 role 属性（`<system-reminder role="reminder">`）：
// 裸 `<system-reminder>` 在仓库里还有两个合法来源 —— MemoryMiddleware 注入的
// 长期记忆块、自动改名提示（middleware_builtin.go:562/693）。用裸标签计数
// 会假红/假绿。
// ---------------------------------------------------------------------------

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
	"time"

	"xbot/llm"
	"xbot/tools"
)

// reminderMarker 是判据专用判别串（带 role 属性，见文件头）。
const reminderMarker = `<system-reminder role="reminder">`

const reminderCloseTag = `</system-reminder>`

// reminderBlock 从一条消息的 content 里截出完整的 reminder 块。
// 未找到返回 ("", false)。
func reminderBlock(content string) (string, bool) {
	i := strings.Index(content, reminderMarker)
	if i < 0 {
		return "", false
	}
	rest := content[i:]
	if j := strings.Index(rest, reminderCloseTag); j >= 0 {
		return rest[:j+len(reminderCloseTag)], true
	}
	return rest, true
}

// findSystemReminderInMessages 在一条请求的 messages 里查找尾部拼接的 reminder。
// 返回：reminder 全文、是否存在、所在消息下标（不存在 = -1）。
func findSystemReminderInMessages(msgs []llm.ChatMessage) (text string, present bool, index int) {
	for i, m := range msgs {
		if blk, ok := reminderBlock(m.Content); ok {
			return blk, true, i
		}
	}
	return "", false, -1
}

// countReminderMarkers 数一条请求里 `<system-reminder role="reminder">` 的出现次数
// （跨全部消息累加）。新形态下恒为 1，>1 说明累积了。
func countReminderMarkers(msgs []llm.ChatMessage) int {
	n := 0
	for _, m := range msgs {
		n += strings.Count(m.Content, reminderMarker)
	}
	return n
}

// findFakeReminderToolCalls 扫描请求里**残留的伪工具调用**：assistant 的
// tool_calls 里 name == system_reminder，或 tool 消息的 ToolCallID 是那个名字。
// 新形态下必须恒为空 —— 这个函数就是判据 a 的机械守卫。
func findFakeReminderToolCalls(msgs []llm.ChatMessage) []string {
	var hits []string
	for i, m := range msgs {
		for j, tc := range m.ToolCalls {
			if tc.Name == systemReminderToolName {
				hits = append(hits, fmt.Sprintf("messages[%d].tool_calls[%d].name=%s", i, j, tc.Name))
			}
			if tc.ID == systemReminderToolName {
				hits = append(hits, fmt.Sprintf("messages[%d].tool_calls[%d].id=%s", i, j, tc.ID))
			}
		}
		if m.Role == "tool" && (m.ToolName == systemReminderToolName || m.ToolCallID == systemReminderToolName) {
			hits = append(hits, fmt.Sprintf("messages[%d].tool(tool_name=%q tool_call_id=%q)", i, m.ToolName, m.ToolCallID))
		}
	}
	return hits
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
	Count     int    `json:"count"`    // marker 在整个请求里的出现次数（应恒为 1）
	AtIndex   int    `json:"at_index"` // reminder 所在消息下标（-1 = 无）
	LastIndex int    `json:"last_index"`
	LastRole  string `json:"last_role"`
	AtTail    bool   `json:"at_tail"` // reminder 是否落在**最后一条**消息里
	TextLen   int    `json:"text_len"`
	Summary   string `json:"summary"`
	// Text 是 reminder 全文（不截断）。不进矩阵表（矩阵只看形状），
	// 由 TestSystemReminderInjectionMatrix 单独落盘成 reminder-text-first-iteration.txt。
	Text string `json:"-"`
	// msgs 是这次请求的完整消息切片（不参与 JSON），供断言扫伪工具调用用。
	msgs []llm.ChatMessage
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
		text, present, at := findSystemReminderInMessages(call.Messages)
		last := len(call.Messages) - 1
		row := reminderMatrixRow{
			Iteration: iter,
			Present:   present,
			Count:     countReminderMarkers(call.Messages),
			AtIndex:   at,
			LastIndex: last,
			LastRole:  call.Messages[last].Role,
			AtTail:    present && at == last,
			TextLen:   len(text),
			Text:      text,
			msgs:      call.Messages,
		}
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
	fmt.Fprintf(&sb, "%-10s %-9s %-6s %-9s %-10s %-8s %-9s %s\n",
		"iteration", "reminder", "count", "at_index", "last_role", "at_tail", "text_len", "summary")
	for _, r := range rows {
		fmt.Fprintf(&sb, "%-10d %-9v %-6d %-9d %-10s %-8v %-9d %s\n",
			r.Iteration, r.Present, r.Count, r.AtIndex, r.LastRole, r.AtTail, r.TextLen, r.Summary)
	}
	t.Logf("\n%s", sb.String())
	writeDump(t, filename, sb.String())
}

// assertInsertionRound 是**插入轮**的判据（N2a 新形态）：
//   - 恰好 1 处带 role 属性的 marker（不累积）；
//   - 落在**最后一条**消息里；
//   - 末条消息 role 必须是 user（末条是 user 时拼进它，否则追加一条 user）；
//   - 文本非空；
//   - 请求里 0 处 system_reminder 伪工具调用。
func assertInsertionRound(t *testing.T, label string, row reminderMatrixRow, msgs []llm.ChatMessage) {
	t.Helper()
	if !row.Present {
		t.Errorf("%s: 缺少 reminder（本轮是插入轮，见 shouldInsertSystemReminder）", label)
		return
	}
	if row.Count != 1 {
		t.Errorf("%s: marker 出现 %d 次, want 1（累积或多处注入）", label, row.Count)
	}
	if !row.AtTail {
		t.Errorf("%s: reminder 在 messages[%d]，末条是 messages[%d] —— 必须落尾部",
			label, row.AtIndex, row.LastIndex)
	}
	if row.LastRole != "user" {
		t.Errorf("%s: 末条消息 role=%q, want user（拼进末条 user 或追加一条 user）", label, row.LastRole)
	}
	if row.TextLen == 0 {
		t.Errorf("%s: reminder 文本为空 —— 伪注入，模型看不到任何内容", label)
	}
	if hits := findFakeReminderToolCalls(msgs); len(hits) > 0 {
		t.Errorf("%s: 请求里仍有 system_reminder 伪工具调用: %v", label, hits)
	}
}

// assertNonInsertionRound 是**非插入轮**的判据：不该有 reminder（频率仍为 1/10/20），
// 也不该有伪工具调用 —— 这正是 N2a 与旧实现的唯一差别（旧实现在这里的请求里
// 有 fake tool pair，新实现在这里什么都没有）。
func assertNonInsertionRound(t *testing.T, label string, row reminderMatrixRow, msgs []llm.ChatMessage) {
	t.Helper()
	if row.Present {
		t.Errorf("%s: 非插入轮出现了 reminder（频率被改动？N2a 要求 1/10/20 原样）", label)
	}
	if row.Count != 0 {
		t.Errorf("%s: 非插入轮 marker 出现 %d 次, want 0", label, row.Count)
	}
	if hits := findFakeReminderToolCalls(msgs); len(hits) > 0 {
		t.Errorf("%s: 请求里仍有 system_reminder 伪工具调用: %v", label, hits)
	}
}

// TestSystemReminderInjectionMatrix 验证**稀疏节奏保持不变 + 落点改为尾部**：
//   - 3 轮 turn：仅第 1 轮含 reminder，且在末条消息里；2/3 轮无；
//   - 12 轮 turn：仅第 1、10 轮含；其余轮次无。
//
// 频率口径由御前 N2a 令锁定：shouldInsertSystemReminder / systemReminderInsertEvery=10
// 原样不动（N2b 才讨论改每轮，且需真 token 量化）。
func TestSystemReminderInjectionMatrix(t *testing.T) {
	t.Run("3turn", func(t *testing.T) {
		rows := runReminderMatrix(t, 3)
		dumpMatrix(t, "per-iteration-matrix-3turn.txt", "3-turn tail-injection matrix", rows)
		for _, r := range rows {
			label := fmt.Sprintf("iteration %d", r.Iteration)
			if r.Iteration == 1 {
				assertInsertionRound(t, label, r, r.msgs)
			} else {
				assertNonInsertionRound(t, label, r, r.msgs)
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
		dumpMatrix(t, "per-iteration-matrix-12turn.txt", "12-turn tail-injection matrix", rows)
		wantSet := map[int]bool{1: true, 10: true}
		for _, r := range rows {
			label := fmt.Sprintf("iteration %d", r.Iteration)
			if wantSet[r.Iteration] {
				assertInsertionRound(t, label, r, r.msgs)
			} else {
				assertNonInsertionRound(t, label, r, r.msgs)
			}
		}
	})
}

// TestSystemReminderLazyBuild 确认构建是**真懒**：非插入轮不得调用
// ListActiveFn（reminder 只在 1/10/20 构建）。这是「先判 iteration 再构建」顺序的
// 行为级证据 —— 把顺序写反了这个测试会红。
//
// N2a：频率未动，所以这条断言原样有效（12 轮 ≤ 2 次：iter 1 与 10）。
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
	mu.Lock()
	calls := len(listActiveCalls)
	mu.Unlock()
	// 12 轮里只有 iteration 1 和 10 是插入轮 → 最多 2 次（每次插入轮构建一次）。
	if calls > 2 {
		t.Fatalf("ListActiveFn 被调用 %d 次，期望 ≤2（仅插入轮 1/10 构建）—— 构建不懒，每轮都在拉状态", calls)
	}
	if calls == 0 {
		t.Fatal("ListActiveFn 从未被调用 —— 说明插入轮根本没构建 reminder")
	}
	t.Logf("ListActiveFn calls = %d（插入轮 1/10，非插入轮不构建）", calls)
}

// TestSystemReminderInjection25Iterations 是稀疏节奏 + 尾部落点的最强判据：
// 25 轮 turn 里注入点必须**恰好**是 1 / 10 / 20，其余 22 轮一律没有；
// 且三个注入点都落在末条消息尾部。
//
// 口径（首辅 2026-10-28 裁定，N2a 不改）：注入点是 1/10/20，**不是** 1/11/21 ——
// 后者只能由「第 N 轮末尾生成、第 N+1 轮消费」的旧相位结构导出，那套结构已作废。
func TestSystemReminderInjection25Iterations(t *testing.T) {
	start := time.Now()
	rows := runReminderMatrix(t, 25)
	elapsed := time.Since(start)
	dumpMatrix(t, "per-iteration-matrix-25turn.txt", "25-turn tail-injection matrix", rows)

	wantHit := map[int]bool{1: true, 10: true, 20: true}
	var hits []int
	for _, r := range rows {
		label := fmt.Sprintf("iteration %d", r.Iteration)
		if wantHit[r.Iteration] {
			hits = append(hits, r.Iteration)
			assertInsertionRound(t, label, r, r.msgs)
		} else {
			assertNonInsertionRound(t, label, r, r.msgs)
		}
	}
	if fmt.Sprint(hits) != fmt.Sprint([]int{1, 10, 20}) {
		t.Fatalf("注入点 = %v, want [1 10 20]", hits)
	}
	t.Logf("injection points over 25 iterations: %v；25 轮 mock turn（零网络）整轮耗时 = %v（每轮 %v）",
		hits, elapsed, elapsed/25)
}

// TestSystemReminderNoAdjacentIterations 是 2026-10-20 事故的回归（N2a 原样保留）：
// 当时 reminder 每轮都注入，模型每轮都看到一组「凭空冒出的工具调用」，
// 误以为要反复调用 system_reminder。
//
// 判据：24 轮 turn 里**任意相邻两轮不得同时为 true**；反向判据：注入次数必须
// 恰好是 3（1/10/20）—— 太少说明谓词被改瘫，太多说明频率被改成每轮（N2b 的事，
// 不许在 N2a 里顺手做）。
//
// 判别力（变异检验 D5）：把 shouldInsertSystemReminder 改成恒 return true，
// 这个用例必须变红（相邻两轮全为 true）—— 因此它不能被写成「只要出现就算过」。
//
// 另附「不累积」：注入轮的 marker 必须恰好 1 处（旧形态一个 fake pair、新形态
// 一段尾部文本，两者都不得随轮次叠加）。
func TestSystemReminderNoAdjacentIterations(t *testing.T) {
	const iterations = 24
	rows := runReminderMatrix(t, iterations)
	dumpMatrix(t, "per-iteration-matrix-24turn-adjacency.txt", "24-turn adjacency matrix", rows)

	prevHit := false
	prevIter := 0
	var total int
	for _, r := range rows {
		if r.Present && prevHit {
			t.Fatalf("iteration %d 与 %d 连续两轮都注入了 system_reminder —— 2026-10-20 事故形态（逐轮注入）", prevIter, r.Iteration)
		}
		if r.Present {
			total++
			if r.Count != 1 {
				t.Fatalf("iteration %d: 注入轮 marker 出现 %d 次, want 1（累积）", r.Iteration, r.Count)
			}
			if !r.AtTail {
				t.Fatalf("iteration %d: reminder 不在末条消息（at_index=%d, last=%d）", r.Iteration, r.AtIndex, r.LastIndex)
			}
		}
		prevHit = r.Present
		prevIter = r.Iteration
	}
	// 反向判据：不能因为「从不注入」而让上面的检查空过。
	if total != 3 { // 24 轮：1, 10, 20
		t.Fatalf("24 轮注入次数 = %d, want 3（1/10/20）—— 过少说明谓词被改瘫，过多说明逐轮注入", total)
	}
	t.Logf("24-turn: %d injections, no adjacent pairs, each carries exactly 1 tail marker", total)
}

// TestSystemReminderLLMMessagesIdempotent 验证同一 runState 上重复调用
// llmMessages 的结果逐字节一致（幂等）：reminder 内容里不得混入时间戳、随机量
// 或自增计数；且源切片 s.messages 不得被污染。
func TestSystemReminderLLMMessagesIdempotent(t *testing.T) {
	cfg, _ := buildReminderTurnConfig(1)
	s := newRunState(cfg)
	s.initDynamicInjector()

	beforeMsgs := len(s.messages)
	beforeBytes := messagesFingerprint(t, s.messages)
	for i := 0; i < 3; i++ {
		_ = s.llmMessages(1)
	}
	if len(s.messages) != beforeMsgs {
		t.Fatalf("llmMessages 改动了 s.messages 长度：%d → %d（源切片被污染）", beforeMsgs, len(s.messages))
	}
	if got := messagesFingerprint(t, s.messages); got != beforeBytes {
		t.Fatalf("llmMessages 改动了 s.messages 内容：sha256 %s → %s", beforeBytes, got)
	}
	t.Logf("s.messages stable (%d messages, sha256=%s) across 3 llmMessages calls", beforeMsgs, beforeBytes)

	first := s.llmMessages(1)
	second := s.llmMessages(1)
	if len(first) != len(second) {
		t.Fatalf("两次调用条数不同 %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].Role != second[i].Role || first[i].Content != second[i].Content || first[i].ToolCallID != second[i].ToolCallID {
			t.Fatalf("第 %d 条消息两次调用不一致\nfirst=%+v\nsecond=%+v", i, first[i], second[i])
		}
		if len(first[i].ToolCalls) != len(second[i].ToolCalls) {
			t.Fatalf("第 %d 条消息 tool_calls 数量不一致", i)
		}
	}
	t.Logf("%d messages byte-identical across two calls", len(first))

	// s.messages 必须零污染：不得出现 marker 或伪工具调用。
	if n := countReminderMarkers(s.messages); n != 0 {
		t.Fatalf("s.messages 里出现 %d 处 reminder marker —— llmMessages 污染了源切片", n)
	}
	if hits := findFakeReminderToolCalls(s.messages); len(hits) > 0 {
		t.Fatalf("s.messages 里出现伪工具调用: %v", hits)
	}
}

// ---------------------------------------------------------------------------
// G1（2026-10-28 御前定稿）：resume turn 的稀疏注入点 + 六行参数化矩阵
//
// N2a：这两条用例从基线**原样恢复**（此前被误删）。唯一改动是「配对」断言
// 换成「落尾」断言 —— 旧断言 `r.Paired`（fake pair 成对）随形态改造失去意义；
// 等价覆盖：旧的不变量是「注入内容完整出现在请求里、且不会让上游 400」，
// 新形态下对应的不变量是「注入内容完整落在最后一条消息尾部」。

// TestSystemReminderInjectionMatrixParameterized 覆盖六种 iterStart。
//
// 规则（原样）：首轮 = iterStart+1 必插；其后每 10 轮一次；首轮与下一个 10k
// **相邻**时跳过该 10k（否则会连续两轮注入，破「任意相邻两轮不同时注入」的
// 硬不变量）。每行统一跑 20 轮（从 iterStart+1 起），因此 30/40 不会进入窗口。
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
				if !r.AtTail || r.TextLen == 0 {
					t.Errorf("iterStart=%d iteration %d: reminder 不完整（at_tail=%v len=%d）",
						c.iterStart, r.Iteration, r.AtTail, r.TextLen)
				}
				if hits2 := findFakeReminderToolCalls(r.msgs); len(hits2) > 0 {
					t.Errorf("iterStart=%d iteration %d: 请求里仍有伪工具调用: %v", c.iterStart, r.Iteration, hits2)
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
// N2a 原样恢复（只把 Paired 换成 AtTail，理由同上）。
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

// ---------------------------------------------------------------------------
// 真实 HTTP 链路：断言实际请求 body 的形状
// ---------------------------------------------------------------------------

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

// contentString 把 wire content 归一成字符串（null → ""）。
func contentString(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case nil:
		return ""
	default:
		b, _ := json.Marshal(c)
		return string(b)
	}
}

// parseWireBody 解析真实请求体。
func parseWireBody(t *testing.T, label, raw string) wireBody {
	t.Helper()
	var body wireBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("%s: 请求 body 不是合法 JSON: %v\nbody=%s", label, err, raw)
	}
	if len(body.Messages) == 0 {
		t.Fatalf("%s: 请求没有 messages", label)
	}
	return body
}

// assertWireNoFakeCalls 是判据 a 的第 ① 条（对**任何**请求都成立）：
// 全量扫描 0 处 system_reminder 伪工具调用（tool_calls.name / tool.tool_call_id / 伪 tool 结果）。
func assertWireNoFakeCalls(t *testing.T, label string, body wireBody) {
	t.Helper()
	for i, m := range body.Messages {
		for j, tc := range m.ToolCalls {
			if tc.Function.Name == systemReminderToolName || tc.ID == systemReminderToolName {
				t.Fatalf("%s: body 里仍出现伪工具调用 messages[%d].tool_calls[%d] name=%q id=%q —— 上游会看到一个不存在的工具",
					label, i, j, tc.Function.Name, tc.ID)
			}
		}
		if m.Role == "tool" && m.ToolCallID == systemReminderToolName {
			t.Fatalf("%s: body 里仍出现伪 tool 结果 messages[%d]", label, i)
		}
	}
}

// assertWireTailForm 是**注入轮**的判据：
//
//	① 0 处 system_reminder 伪工具调用；
//	② 末条消息 role == "user"；
//	③ `<system-reminder role="reminder">` 恰好 1 处，且落在末条消息内容里，
//	   位置在原有内容**之后**（尾部拼接，不是前置）。
func assertWireTailForm(t *testing.T, label, raw string) {
	t.Helper()
	var body wireBody
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("%s: 请求 body 不是合法 JSON: %v\nbody=%s", label, err, raw)
	}
	if len(body.Messages) == 0 {
		t.Fatalf("%s: 请求没有 messages", label)
	}
	// ① 伪工具调用 0 处
	assertWireNoFakeCalls(t, label, body)
	// ② 末条必须是 user
	last := body.Messages[len(body.Messages)-1]
	lastContent := contentString(last.Content)
	if last.Role != "user" {
		t.Fatalf("%s: 末条消息 role=%q, want user", label, last.Role)
	}
	// ③ marker 恰好 1 处，落在末条
	total := 0
	for _, m := range body.Messages {
		total += strings.Count(contentString(m.Content), reminderMarker)
	}
	if total != 1 {
		t.Fatalf("%s: 全请求 marker 出现 %d 次, want 1（累积或没注入）", label, total)
	}
	idx := strings.Index(lastContent, reminderMarker)
	if idx < 0 {
		t.Fatalf("%s: reminder 不在末条消息里；末条内容前 200 字符: %.200s", label, lastContent)
	}
	if !strings.HasSuffix(strings.TrimRight(lastContent, "\n"), reminderCloseTag) {
		t.Errorf("%s: reminder 不在末条内容结尾（尾部拼接判据）", label)
	}
	// 拼在已有内容之后：marker 前必须有分隔（末条是原 user 消息时）
	if idx > 0 && !strings.HasSuffix(lastContent[:idx], "\n\n") {
		t.Errorf("%s: marker 前缺少 \"\\n\\n\" 分隔", label)
	}
	blk, _ := reminderBlock(lastContent)
	if strings.Count(blk, reminderMarker) != 1 {
		t.Errorf("%s: reminder 块内嵌套了多层标签（不得再包一层）", label)
	}
}

// TestSystemReminderHTTPBody 用真实 llm.NewOpenAILLM（Stream:false）+ httptest
// 假上游，捕获**实际 HTTP body JSON**：三轮请求**每一轮**都必须满足
// 「末条 user + 唯一 reminder + 0 伪工具调用」。
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

	// 稀疏节奏（N2a 未改频率）：只有 iteration 1 是插入轮，2/3 轮不得有 reminder，
	// 但**任何一轮**都不得出现伪工具调用。
	for _, d := range dumps {
		label := fmt.Sprintf("iteration %d", d.Index+1)
		if d.Index == 0 {
			assertWireTailForm(t, label, d.Body)
			continue
		}
		body := parseWireBody(t, label, d.Body)
		assertWireNoFakeCalls(t, label, body)
		for _, m := range body.Messages {
			if strings.Contains(contentString(m.Content), reminderMarker) {
				t.Fatalf("%s: 非插入轮出现 reminder（频率被改动？N2a 要求 1/10/20 原样）", label)
			}
		}
	}

	// 首轮 body 必须真的带着工具定义（否则「模型该用 TodoWrite」无从谈起）
	if !strings.Contains(dumps[0].Body, `"tools"`) {
		t.Error("首轮 body 不含 tools 字段 —— 请求构造异常")
	}
	// 第二轮起，历史里应能看到上一轮的 Echo tool 结果 —— 证明对话本身是干净的
	if !strings.Contains(dumps[1].Body, `"role":"tool"`) {
		t.Error("第二轮 body 里没有 tool 结果消息 —— 正常工具循环被破坏")
	}
}

// TestSystemReminderHTTPBodyResumeTurn 用真实 llm.NewOpenAILLM + httptest 捕获
// iterStart=1 的 resume turn 请求体：首轮 = iterStart+1 = 2，所以**只有第一条**
// 请求（iteration 2）带尾部 reminder；3、4 轮没有（N2a 频率原样）。
func TestSystemReminderHTTPBodyResumeTurn(t *testing.T) {
	dumpDir := os.Getenv("XBOT_REQ_DUMP_DIR")
	if dumpDir == "" {
		t.Skip("XBOT_REQ_DUMP_DIR 未设置 —— 跳过 resume HTTP body 证据测试")
	}

	srv, dumpsFn := fakeChatCompletionServer(t, 2)
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

	// resume turn 的首轮 = iterStart+1 = 2 ⇒ 只有第一条请求是插入轮。
	for _, d := range dumps {
		label := fmt.Sprintf("resume iteration %d", d.Index+2)
		if d.Index == 0 {
			assertWireTailForm(t, label, d.Body)
			continue
		}
		body := parseWireBody(t, label, d.Body)
		assertWireNoFakeCalls(t, label, body)
		for _, m := range body.Messages {
			if strings.Contains(contentString(m.Content), reminderMarker) {
				t.Fatalf("%s: 非插入轮出现 reminder", label)
			}
		}
	}
}
