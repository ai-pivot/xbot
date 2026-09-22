package channel

import (
	"testing"

	"xbot/llm"
)

// 命令行（`!cmd`）落库行经转换层必须带 **standalone + 时间锚点**：前端据此走
// standalone 渲染路径把它插回"它发生的那一刻"（与实时渲染一致），而不是被当成普通
// turn 行（那会让它绑到错误的 turn，或落到列表顶部 —— 用户报告过"看不到输出"）。
//
// 锚点语义 = 走到该行时已知的最新 turn：命令发生在 turn 1 之后、turn 2 之前 ⇒ 锚点 1。
func TestConvertMessagesToHistory_CommandRowsStandaloneWithAnchor(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "hi", TurnID: 1},
		{Role: "assistant", Content: "hello", TurnID: 1},
		// 命令行（落库形态：CommandRow + turn_id=0）
		{Role: "user", Content: "!pwd", CommandRow: true},
		{Role: "assistant", Content: "/root", CommandRow: true},
		{Role: "user", Content: "next", TurnID: 2},
		{Role: "assistant", Content: "ok", TurnID: 2},
	}

	out := ConvertMessagesToHistory(msgs)

	var cmd []HistoryMessage
	for _, h := range out {
		if h.Content == "!pwd" || h.Content == "/root" {
			cmd = append(cmd, h)
		}
	}
	if len(cmd) != 2 {
		t.Fatalf("命令行两行都必须出现在历史里，got %d: %+v", len(cmd), out)
	}
	for _, h := range cmd {
		if !h.Standalone {
			t.Fatalf("命令行必须是 standalone（前端据此跳过 bindTurnIDs 绑定）: %+v", h)
		}
		if h.AnchorTurnID != 1 {
			t.Fatalf("命令行锚点应为 1（发生在 turn 1 之后、turn 2 之前），got %d: %+v", h.AnchorTurnID, h)
		}
		if h.TurnID != 0 {
			t.Fatalf("命令行 turnID 必须保持 0（虚拟键回落 row.id，绝不与 turn 行撞键）: %+v", h)
		}
	}
	if cmd[0].Role != "user" || cmd[1].Role != "assistant" {
		t.Fatalf("命令行顺序必须保持 输入(user) → 输出(assistant)，got %s → %s", cmd[0].Role, cmd[1].Role)
	}
}
