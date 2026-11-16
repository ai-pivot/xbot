package agent

// system_reminder_compress_isolation_test —— commit B 的守护判据：
// **压缩摘要（内部一次性 Run）不得注入 system_reminder**。
//
// 为什么需要这条隔离：每轮都注入一旦无条件生效，压缩请求尾部会多出一块
// reminder —— 末条不再是「压缩指令（user）」的裸指令，直接破坏
// 「最后一条消息 = 压缩指令（user）」的契约 —— 实测会让 TestCompactMessages_*
// 六个用例同时变红。（旧形态下多出来的是一对 assistant(tool_call) + tool(result)，
// 2026-10-30 N2 起改成尾部内容拼接，破坏契约的方式变了，隔离要求不变。）
// reminder 是**对话轮**的机制：压缩没有「这一轮用户说了什么」可提醒，
// 塞进去只有害处（<user-msg> 会抓到历史里最后一条 user 消息，误导摘要模型）。
//
// 三层判据：
//  1. 开关本身：RunConfig.DisableSystemReminder 在内部一次性 Run 上必须真的生效；
//  2. 真实链路：compactMessages 发出的请求里不得出现 system_reminder；
//  3. wire：`compressRunConfig(...)` 的返回值必须 DisableSystemReminder == true
//     （纯函数直接断言；不再扫源码字符串）。
//
// 为什么是显式字段而不是 `MaxIterations > 0` 或 `AgentID == "compressor"` 推断：
// 守卫的失效方式必须是**响的**。显式字段漏设 → 压缩请求形状被破坏 →
// TestCompactMessages_* 立刻红、根因可定位；改成隐式推断，将来任何「带轮次上限
// 但非一次性」的 Run 会**静默**失去 reminder，没人会红。
// 注：MaxIterations（轮次上限）与 DisableSystemReminder（reminder 策略）是
// **两个正交显式开关，各守一件事，不是同义反复**。

import (
	"context"
	"testing"

	"xbot/llm"
)

// TestSystemReminderSkippedForInternalOneShotRun 验证内部一次性 Run 不注入 reminder，
// 且请求最后一条仍是 user 指令。
func TestSystemReminderSkippedForInternalOneShotRun(t *testing.T) {
	cfg := RunConfig{
		LLMClient: &mockLLM{responses: []llm.LLMResponse{{Content: "summary"}}},
		Model:     "test-model",
		Tools:     newTestRegistry(),
		Messages: []llm.ChatMessage{
			llm.NewSystemMessage("You are a context compaction expert."),
			llm.NewUserMessage("summarize the history"),
		},
		AgentID:               "compressor",
		Channel:               "web",
		ChatID:                "chat1",
		MaxIterations:         1,    // 与 compress.go 一致（轮次上限，正交开关）
		DisableSystemReminder: true, // reminder 策略开关：本用例验证的就是它
	}
	// 防「开关被删了但测试还绿」：Run 之前先钉住这次 Run 的策略。
	if !cfg.DisableSystemReminder {
		t.Fatal("本用例必须在 DisableSystemReminder=true 下运行，否则它什么也没验证")
	}

	mock := cfg.LLMClient.(*mockLLM)
	out := Run(context.Background(), cfg)
	if out.Error != nil {
		t.Fatalf("run: %v", out.Error)
	}
	if len(mock.calls) != 1 {
		t.Fatalf("请求次数 = %d, want 1", len(mock.calls))
	}
	msgs := mock.calls[0].Messages
	if _, present, _ := findSystemReminderInMessages(msgs); present {
		t.Fatalf("内部一次性 Run（压缩）不得注入 system_reminder，实际注入了 %d 条消息", len(msgs))
	}
	if last := msgs[len(msgs)-1]; last.Role != "user" {
		t.Fatalf("压缩请求最后一条必须是 user 指令，实际 role=%q —— reminder 破坏了请求形状", last.Role)
	}
	t.Logf("internal one-shot run: %d messages, last role=%q, no system_reminder", len(msgs), msgs[len(msgs)-1].Role)
}

// TestCompactRequestHasNoSystemReminder 在**真实压缩链路**上验证同一件事：
// compactMessages 发出的请求 messages 里不得出现 system_reminder。
// 复用 compress_cache_test.go 的 capturingLLM（它已经捕获了每条请求的 messages）。
func TestCompactRequestHasNoSystemReminder(t *testing.T) {
	client := &capturingLLM{response: "### Task Summary\nCompacted."}
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are xbot, an agent working in a repo."),
		llm.NewUserMessage("please fix the login bug"),
		llm.NewAssistantMessage("Looking at auth.go now."),
		llm.NewUserMessage("continue"),
		llm.NewAssistantMessage("on it"),
	}
	if _, err := compactMessages(context.Background(), messages, client, "test-model", 200000, 1000, 0); err != nil {
		t.Fatalf("compactMessages: %v", err)
	}
	if len(client.calls) == 0 {
		t.Fatal("压缩没有发起任何 LLM 请求")
	}
	for i, req := range client.calls {
		if _, present, _ := findSystemReminderInMessages(req); present {
			t.Fatalf("压缩请求 #%d 里出现了 system_reminder（%d 条消息）—— 会破坏「末条必须是指令」契约", i+1, len(req))
		}
		// 新形态下 reminder 不再走伪工具对，但这条守卫保留反向扫描：
		// 一旦有人把 fake pair 路径恢复，压缩请求里会立刻出现 system_reminder 伪工具。
		if hits := findFakeReminderToolCalls(req); len(hits) > 0 {
			t.Fatalf("压缩请求 #%d 里出现 system_reminder 伪工具调用: %v", i+1, hits)
		}
	}
	t.Logf("compactMessages 请求 %d 条，均无 system_reminder", len(client.calls))
}

// TestCompressRunConfigDisablesSystemReminder 钉住压缩配置的 reminder 策略。
//
// 2026-10-28 重构（C1）：原实现扫 compress.go 源码里的字面量文本，
// 重命名/格式化即失效；现在直接断言 `compressRunConfig(...)` 的返回值。
// （判别力由 guard-mutation-m3b.txt 证明：把函数里的 true 改成 false，本用例红。）
func TestCompressRunConfigDisablesSystemReminder(t *testing.T) {
	cfg := compressRunConfig(
		&mockLLM{}, "test-model",
		[]llm.ChatMessage{llm.NewSystemMessage("sys"), llm.NewUserMessage("summarize")},
		1024)
	if !cfg.DisableSystemReminder {
		t.Fatalf("⛔ compressRunConfig 的 DisableSystemReminder = false —— 压缩请求会被注入 system_reminder，破坏「末条 = 指令」契约")
	}
	if cfg.MaxIterations != 1 {
		t.Fatalf("压缩 Run 必须 MaxIterations=1（got %d）", cfg.MaxIterations)
	}
}

// TestDisableSystemReminderShortCircuitsBeforeBuild —— 钉住「开关判定在构建之前」。
//
// 历史（2026-10-28 门下省终审 F1）：commit 44325b6c 的 message 声称本用例已改名
// 并从 TestDisableSystemReminderBeatsIterationPredicate 补上了 ListActiveFn 断言，
// 但代码里实际被删掉了（grep 0 命中）—— 声明强于实际。此处按 message 的原话补齐。
//
// 原用例为什么是假声明：`iteration == 1` 时谓词本来就为真，即使先判谓词、后判开关，
// disable 依然会拦住注入，断言照样绿 —— 它钉不住任何顺序。
//
// 真正与顺序相关的可观测不变量：disable 命中时**根本不该调用 buildSystemReminder**
// （它要拉 todo / goal / 子代理状态，ListActiveFn 是跨进程回调，白跑有真实开销）。
// 这里用 ListActiveFn 计数断言：disable=true 时必须 0 次。
// 判别力由 guard-mutation-b5.txt 证明（把构建挪到 disable 判定之前 → 本用例红）。
func TestDisableSystemReminderShortCircuitsBeforeBuild(t *testing.T) {
	cfg, _ := buildReminderTurnConfig(1)
	cfg.DisableSystemReminder = true
	var buildCalls int
	cfg.InteractiveCallbacks = &InteractiveCallbacks{
		ListActiveFn: func(channel, chatID string) []SubAgentStatus {
			buildCalls++
			return nil
		},
	}
	s := newRunState(cfg)
	s.initDynamicInjector()

	msgs := s.llmMessages(1)
	if _, present, _ := findSystemReminderInMessages(msgs); present {
		t.Fatalf("DisableSystemReminder=true 时 iteration 1 仍注入了 reminder")
	}
	if len(msgs) != len(s.messages) {
		t.Fatalf("条数 = %d, want %d（不应附加任何消息/内容）", len(msgs), len(s.messages))
	}
	if buildCalls != 0 {
		t.Fatalf("DisableSystemReminder=true 时仍调用 buildSystemReminder %d 次 —— "+
			"开关必须在构建之前判定（否则白跑跨进程回调 ListActiveFn）", buildCalls)
	}
	t.Logf("disable=true, iteration=1 → %d messages, no reminder, build 调用 0 次", len(msgs))
}
