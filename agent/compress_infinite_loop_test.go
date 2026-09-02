package agent

// 无限循环压缩（200k 上下文）复现测试。
//
// 根因链（见 maybeCompress / runCompression / compactMessages）：
//  1. 触发侧用【真实 API prompt_tokens】对比 0.9×(maxContext−maxOutput)；
//  2. 压缩产出（compactMessages）内部预算基于 maxContext 全量（tail cap =
//     15%×maxContext 按每条 200 token 估算 —— 严重低估真实 tool result）；
//  3. 达标检查（runCompression post-compress safety check）用的却是
//     CompressResult.CompressedTokens = 【仅压缩摘要文本】的 chars×2/3 估算
//     （不含 system prompt / tail / 工具定义）→ 摘要永远远小于阈值 →
//     "假达标" → aggressiveTruncate 安全网从不触发；
//  4. 下一次 callLLM 返回真实 prompt_tokens（system + tail + summary 的真实
//     大小，依然超阈值）→ tracker 恢复 api 值 → 5 次迭代冷却后再次压缩 →
//     tail 起点是最后一个 user msg（长 tool loop 中不动）→ 压缩对真实大小
//     几乎无效 → 无限循环，每轮白烧一次压缩 LLM 调用。
//
// 200k 场景特别严重：maxOutput（GLM 类常见 98k）把触发线压到 0.9×102k=91.8k，
// 而 xbot 的不可压缩基座（system prompt + 工具定义）+ tail 轻松超线；
// 1M 上下文模型触发线 825k 几乎碰不到，所以问题集中在 200k。
//
// 三个测试全部用行为断言（mock CM 调用次数 / messages 长度），不引用修复
// 内部字段，保证红灯阶段可编译。

import (
	"context"
	"strings"
	"testing"

	"xbot/agent/hooks"
	"xbot/llm"
)

// bigLLMView builds a compressed output that mocks what compactMessages
// produces: [system] + [summary] + [continuation] + [tail...]. The token
// numbers below are estimates under the chars×2/3 heuristic shared with
// compactMessages.
func bigLLMView(systemChars, tailMsgs, tailCharsEach int) []llm.ChatMessage {
	view := []llm.ChatMessage{
		llm.NewSystemMessage(strings.Repeat("S", systemChars)),
		llm.NewUserMessage("[Compacted context]\n" + strings.Repeat("m", 1478)), // ~1000 tokens
		llm.NewUserMessage("Continue the task with the compacted context above."),
	}
	for i := 0; i < tailMsgs; i++ {
		view = append(view, llm.NewAssistantMessage(strings.Repeat("x", tailCharsEach)))
	}
	return view
}

func newCompressLoopState(t *testing.T, cm ContextManager, msgs []llm.ChatMessage, promptTokens int64) *runState {
	t.Helper()
	tracker := NewTokenTracker(promptTokens, 100)
	tracker.RecordLLMCall(promptTokens, 100)
	return &runState{
		cfg: RunConfig{
			MaxOutputTokens:      98304, // GLM-class max output → budget=101696, threshold=91526
			LLMClient:            &mockLLM{},
			Model:                "glm-test",
			ChatID:               "test-chat",
			Channel:              "test",
			OriginUserID:         "cli_user",
			ContextManager:       cm,
			ContextManagerConfig: &ContextManagerConfig{MaxContextTokens: 200000},
			SaveTokenState:       func(_, _ int64) {},
			SaveContextTokens:    func(_ int64) {},
		},
		messages:           msgs,
		tokenTracker:       tracker,
		persistence:        NewPersistenceBridge(nil, 0),
		structuredProgress: &StructuredProgress{Phase: PhaseThinking},
		autoNotify:         false,
		sessionCtx:         &hooks.SessionContext{},
	}
}

// Test 1: post-compress 达标检查必须用【压缩后全量消息估算】，不能用仅摘要
// 的 CompressedTokens —— 否则"假达标"让 aggressiveTruncate 安全网永不触发，
// 真实 prompt_tokens 压不下去 → 每 5 次迭代无限重压。
//
// 场景：200k 上下文 + 98k maxOutput（触发线 91.8k）。压缩产出 = system(40k
// tokens) + summary + 12 条 tail 消息(每条 6k tokens) ≈ 113k tokens —— 真实
// 超线；但 CompressedTokens(摘要) 只有 ~1k → 现状达标检查 1k < 91.8k →
// "达标" → 不截断 → 无限循环。修复后：全量估算 113k > 91.8k → aggressiveTruncate
// （保 system + notice + 最后 6 条 = 40k+36k ≈ 76k < 91.8k）→ 收敛。
func TestRunCompression_PostCompressCheckUsesFullEstimate_NotSummaryOnly(t *testing.T) {
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			return &CompressResult{
				// LLMView ≈ 40k(system) + 1k(summary) + 0.2k + 12×6k(tail) ≈ 113k tokens
				LLMView:          bigLLMView(60000, 12, 9000),
				CompressedTokens: 1000, // 摘要估算 —— 现状达标检查错误地用它
			}, nil
		},
	}

	state := newCompressLoopState(t, cm, []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("do a long task"),
	}, 190000)

	state.runCompression(context.Background(), cm, 190000, 200000)

	// 压缩后产出 113k > 91.8k（触发线）→ aggressiveTruncate 必须执行：
	// system + notice + 最后 6 条 tail = 8 条消息。
	if got := len(state.messages); got != 8 {
		t.Errorf("BUG REPRODUCED: post-compress output (~113k tokens) exceeds the 91.5k trigger line, but no truncation happened (messages=%d). "+
			"The safety check compared against the SUMMARY-only estimate (CompressedTokens=1000) instead of the full message estimate — "+
			"the truncation net never fires and compression re-triggers every 5 iterations (infinite loop). Want 8 (system+notice+6 tail).", got)
	}
	// 截断后 tracker 必须重置（ResetAfterCompress → no_data），下次真实 API 值校准。
	if pt, source := state.tokenTracker.GetPromptTokens(); pt != 0 || source != "no_data" {
		t.Errorf("after aggressiveTruncate, tracker should be no_data, got (%d, %q)", pt, source)
	}
}

// Test 2: 压缩后全量估算仍超限且【无可收缩部分】（system prompt 本身巨大 /
// 消息太少 aggressiveTruncate 无能为力）→ 必须熔断（本轮 Run 不再自动压缩），
// 绝不能无限重试。场景：200k chars 的 system prompt（≈133k tokens）单独就
// 超过 91.8k 触发线 —— 压缩/截断对它都无效。
func TestRunCompression_GivesUpWhenUnshrinkable_NoInfiniteRetry(t *testing.T) {
	compressCalls := 0
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			compressCalls++
			// system 200k chars ≈ 133k tokens > 91.8k；只有 2 条对话消息 →
			// aggressiveTruncate 无可截断（conversationMsgs ≤ 6）。
			return &CompressResult{
				LLMView:          bigLLMView(200000, 2, 100),
				CompressedTokens: 1000,
			}, nil
		},
	}

	state := newCompressLoopState(t, cm, []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("task"),
	}, 190000)

	// 第一次压缩（模拟 maybeCompress 触发路径）。
	state.runCompression(context.Background(), cm, 190000, 200000)

	// 压缩后真实 prompt_tokens 依然超线（模拟下一次 API 调用的返回值）。
	state.tokenTracker.RecordLLMCall(190000, 100)

	// 模拟后续 12 次迭代（每 5 次冷却到期可再触发）。压缩对不可收缩部分
	// 无效 → 现状会继续白跑压缩（无限循环）；修复后必须熔断。
	for i := 0; i < 12; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress: %v", err)
		}
	}

	if compressCalls != 1 {
		t.Errorf("BUG REPRODUCED: compression re-triggered %d times on an unshrinkable context (system prompt alone exceeds the trigger line). "+
			"Every retry burns a full-context compaction LLM call and can never get below the line — this is the infinite loop. Want exactly 1 call.", compressCalls)
	}
}

// Test 3: 达标检查（估算口径）通过但【真实 API 值不降】→ 连续无效压缩必须
// 熔断。场景：估算低估（中文/密集 token），压缩产出估算 22k < 91.8k（达标
// 检查放行），但下次 API 真实返回 185k（依然超线）→ 又触发 → 又"达标" →
// 循环。熔断条件：连续 2 次触发时的真实值相对上次触发降幅 < 5%。
func TestMaybeCompress_ConsecutiveIneffectiveCompressionGivesUp(t *testing.T) {
	compressCalls := 0
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			compressCalls++
			// 压缩产出很小（估算 ~22k < 91.8k）：达标检查（估算口径）通过。
			// 但真实 API 值（测试模拟）始终 ~185k —— 估算严重低估的场景。
			return &CompressResult{
				LLMView:          bigLLMView(30000, 4, 100),
				CompressedTokens: 500,
			}, nil
		},
	}

	state := newCompressLoopState(t, cm, []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("task"),
	}, 190000)

	// 第 1 次触发（compressAttempts=1）→ 压缩 calls=1。
	if err := state.maybeCompress(context.Background()); err != nil {
		t.Fatalf("maybeCompress #1: %v", err)
	}
	if compressCalls != 1 {
		t.Fatalf("setup: first compression should run, got %d calls", compressCalls)
	}

	// 模拟压缩后真实 API 值依然 185k（估算低估：产出估算 22k，真实 185k）。
	state.tokenTracker.RecordLLMCall(185000, 100)

	// 5 次迭代冷却（attempts 2-6），第 6 次冷却到期再触发 → 第 2 次压缩。
	for i := 0; i < 5; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress cooldown: %v", err)
		}
	}
	if compressCalls != 2 {
		t.Fatalf("setup: second compression should run at cooldown expiry, got %d calls", compressCalls)
	}

	// 再模拟：真实值 184k（相对第一次触发的 190k 降幅 < 5% → 无效 #2）。
	state.tokenTracker.RecordLLMCall(184000, 100)
	for i := 0; i < 5; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress round 3: %v", err)
		}
	}
	// 第 3 次触发点：连续 2 次无效 → 熔断，本轮 Run 不再压缩。
	if compressCalls != 2 {
		t.Errorf("BUG REPRODUCED: compression kept re-triggering (calls=%d) even though real prompt_tokens never dropped ≥5%% across triggers. "+
			"The estimate-based post-compress check passes (under-estimation) while the real value stays above the line — infinite loop. Want 2.", compressCalls)
	}

	// 继续跑很多迭代也必须保持熔断。
	state.tokenTracker.RecordLLMCall(183000, 100)
	for i := 0; i < 10; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress post-abandon: %v", err)
		}
	}
	if compressCalls != 2 {
		t.Errorf("after abandoning, compression must not re-trigger, got %d calls", compressCalls)
	}
}

// Test 4（对照，防回归）：正常有效压缩 —— 真实值显著下降后冷却期触发不
// 计入"无效"，熔断不误伤正常周期性压缩。
func TestMaybeCompress_EffectiveCompressionNotAbandoned(t *testing.T) {
	compressCalls := 0
	cm := &mockContextManager{
		compressFn: func(_ context.Context, _ []llm.ChatMessage, _ llm.LLM, _ string, _ int64) (*CompressResult, error) {
			compressCalls++
			return &CompressResult{
				LLMView:          bigLLMView(30000, 4, 100),
				CompressedTokens: 500,
			}, nil
		},
	}

	state := newCompressLoopState(t, cm, []llm.ChatMessage{
		llm.NewSystemMessage("system"),
		llm.NewUserMessage("hello"),
		llm.NewAssistantMessage("hi"),
		llm.NewUserMessage("task"),
	}, 190000)

	// 触发 #1（190k）→ 压缩 → 真实值降到 150k（降幅 > 5% → 有效）。
	if err := state.maybeCompress(context.Background()); err != nil {
		t.Fatalf("maybeCompress #1: %v", err)
	}
	state.tokenTracker.RecordLLMCall(150000, 100)
	// 会话继续增长：150k → 160k → 冷却到期触发 #2（160k vs 上次触发 190k，
	// 降幅 > 5% → 有效，计数清零）→ 正常压缩。
	for i := 0; i < 5; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress #2: %v", err)
		}
	}
	state.tokenTracker.RecordLLMCall(160000, 100)
	for i := 0; i < 5; i++ {
		if err := state.maybeCompress(context.Background()); err != nil {
			t.Fatalf("maybeCompress #3: %v", err)
		}
	}
	if compressCalls != 3 {
		t.Errorf("effective periodic compression must NOT be abandoned, got %d calls (want 3: every cooldown expiry re-compresses)", compressCalls)
	}
}
