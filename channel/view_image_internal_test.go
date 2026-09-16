package channel

import (
	"strings"
	"testing"

	"xbot/llm"
)

// 复现（用户报告 2026-09-16）：web 端上传图片后，用户自己的消息被
// view_image 的 follow-up 注入行**顶掉**（正文变成「📷 …」、图片地址从
// `/api/files/download?key=uploads/…` 变成 `/api/files/viewimg/<uuid>`）。
//
// DB 实证（tenant=140480 turn=904，id 升序）：
//
//	1780461 user turn=904 "![IMG_5001.png](/api/files/download?key=uploads%2F4%2F…png&inline=1)激活你的技能"
//	1780467 user turn=904 "📷 … ![ref_dog.jpg](/api/files/viewimg/2497df58….j…"   ← 注入
//	1780477 user turn=904 "📷 … ![dog_chicken.jpg](/api/files/viewimg/9e356d4d….j…" ← 注入
//
// 注入行由 agent/engine_run.go injectViewImages 持久化：user role 是多模态唯一
// 载体（OpenAI tool role 不能带图），且**复用触发它的用户消息的 turn_id**。
// 渲染层每个 turn 只有一个 user 槽位 ⇒ 必须靠 llm.ChatMessage.Internal 把它
// 从渲染历史里剔除（LLM 上下文仍然保留）。
func viewImageHistoryFixture() []llm.ChatMessage {
	const realUser = "![IMG_5001.png](/api/files/download?key=uploads%2F4%2F68b355c3.png&inline=1)激活你的技能"
	inj := func(ref, label string) string {
		return "📷 以下图片已通过 view_image 工具加载，可直接进行视觉分析：\n\n![" + label + "](" + ref + ")"
	}
	return []llm.ChatMessage{
		{Role: "system", Content: "sys"},
		{ID: 1780461, Role: "user", Content: realUser, TurnID: 904},
		{ID: 1780463, Role: "assistant", Content: "技能已激活（image-gen）", TurnID: 904},
		{ID: 1780467, Role: "user", Content: inj("/api/files/viewimg/2497df58.jpeg", "ref_dog.jpg"), TurnID: 904, Internal: true},
		{ID: 1780477, Role: "user", Content: inj("/api/files/viewimg/9e356d4d.jpeg", "dog_chicken.jpg"), TurnID: 904, Internal: true},
		{ID: 1780478, Role: "assistant", Content: "两张参考图都对", TurnID: 904},
	}
}

func assertOnlyRealUserRendered(t *testing.T, history []HistoryMessage) {
	t.Helper()
	var users []HistoryMessage
	for _, h := range history {
		if h.Role == "user" {
			users = append(users, h)
		}
	}
	if len(users) != 1 {
		t.Fatalf("expected exactly 1 user HistoryMessage (the real one), got %d: %+v", len(users), users)
	}
	if !strings.Contains(users[0].Content, "/api/files/download?key=uploads%2F4%2F") {
		t.Fatalf("rendered user message is not the user's own: %q", users[0].Content)
	}
	if strings.Contains(users[0].Content, "view_image 工具加载") {
		t.Fatalf("view_image injection leaked into the rendered user message: %q", users[0].Content)
	}
}

// Internal 行（模型侧载体）不得渲染成用户消息 —— 结构化路径。
func TestConvert_MustNotRenderInternalInjection_WithIterations(t *testing.T) {
	history := ConvertMessagesToHistoryWithIterations(viewImageHistoryFixture(), nil)
	assertOnlyRealUserRendered(t, history)
}

// 同一契约的 legacy 路径（无结构化迭代数据时走 ConvertMessagesToHistory）。
func TestConvert_MustNotRenderInternalInjection_LegacyPath(t *testing.T) {
	history := ConvertMessagesToHistory(viewImageHistoryFixture())
	assertOnlyRealUserRendered(t, history)
}

// 过滤只发生在渲染路径：LLM 上下文（Replay 的输出）必须保留 Internal 行，
// 否则模型在后续 turn 里看不到之前分析过的图片。
func TestFilterInternalMessages_KeepsForLLMContext(t *testing.T) {
	msgs := viewImageHistoryFixture()
	filtered := filterInternalMessages(msgs)
	if len(filtered) != 4 {
		t.Fatalf("expected 4 renderable rows (system+user+2 assistant), got %d", len(filtered))
	}
	// 原始切片（LLM 上下文来源）不受影响
	if len(msgs) != 6 {
		t.Fatalf("caller's slice must not be mutated, got %d", len(msgs))
	}
}
