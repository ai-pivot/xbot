package agent

import (
	"strings"
	"testing"

	"xbot/llm"
)

// TestSanitizeMessagesLoopShape 复现 turn 13 LOOP 事故的消息形态：
// [A: content="", tool_calls=[FileReplace(id=call_N)]][T: id=call_N result]
// 交替 ×N（每次 tool_call_id 不同）。SanitizeMessages 不得剔除任何一对
// ——若剔除，模型 prompt 冻结 → 相同输入 → 相同输出 → 无限循环。
func TestSanitizeMessagesLoopShape(t *testing.T) {
	buildMsgs := func(n int) []llm.ChatMessage {
		msgs := []llm.ChatMessage{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "do the task"},
		}
		// 第一次成功调用
		msgs = append(msgs, llm.ChatMessage{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{
			ID: "call_success_1", Name: "FileReplace", Arguments: `{"path":"/a.ts","old_string":"x","new_string":"y"}`,
		}}})
		msgs = append(msgs, llm.NewToolMessage("FileReplace", "call_success_1", "", "Successfully replaced 1 occurrence(s) in /a.ts"))
		// N 次 LOOP DETECTED 重发（每次 id 不同——真实形态已从 DB 验证）
		for i := 0; i < n; i++ {
			id := "call_loop_" + string(rune('a'+i))
			msgs = append(msgs, llm.ChatMessage{Role: "assistant", Content: "", ToolCalls: []llm.ToolCall{{
				ID: id, Name: "FileReplace", Arguments: `{"path":"/a.ts","old_string":"x","new_string":"y"}`,
			}}})
			msgs = append(msgs, llm.NewToolMessage("FileReplace", id, "", "Error: ⚠️ LOOP DETECTED — this duplicate tool call was SKIPPED (not executed)."))
		}
		return msgs
	}

	for _, n := range []int{1, 3, 19} {
		msgs := buildMsgs(n)
		out := llm.SanitizeMessages(msgs)
		want := len(msgs)
		if len(out) != want {
			t.Fatalf("n=%d: SanitizeMessages 剔除了消息（in=%d out=%d）——LOOP 消息对丢失会冻结模型 prompt", n, want, len(out))
		}
		// 验证最后一次 LOOP 警告仍在（模型必须看到）
		last := out[len(out)-1]
		if !strings.Contains(last.Content, "LOOP DETECTED") {
			t.Fatalf("n=%d: 最后的 LOOP 警告丢失（模型看不到拦截反馈）", n)
		}
	}
}
