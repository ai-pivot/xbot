package agent

import (
	"encoding/json"
	"os"
	"testing"

	"xbot/llm"
)

// TestSanitizeRealTurn13Messages 用 DB 导出的真实消息（turn 13 事故现场，
// 06:32:28 请求的 742 条）跑 SanitizeMessages。核心验证：
//  1. 成功的 FileReplace 对（db_id 1315873/1315874）必须保留——若被剔除，
//     模型看不到"已成功"，prompt 内容与上一迭代完全一致 → 网关收到
//     相同请求 → 相同响应（usage 恒定）→ 模型无限重复（用户观察到的现象）
//  2. 总体剔除条数及被剔条目的位置分布
func TestSanitizeRealTurn13Messages(t *testing.T) {
	if os.Getenv("XBOT_REAL_MSG_DUMP") == "" {
		t.Skip("需要真实数据 dump：XBOT_REAL_MSG_DUMP=/tmp/turn13_msgs.json go test")
	}
	data, err := os.ReadFile(os.Getenv("XBOT_REAL_MSG_DUMP"))
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var raw []struct {
		DBID      int64  `json:"db_id"`
		Role      string `json:"role"`
		Content   string `json:"content"`
		TurnID    uint64 `json:"turn_id"`
		ToolCalls []struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"tool_calls"`
		ToolCallID string `json:"tool_call_id"`
		ToolName   string `json:"tool_name"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parse dump: %v", err)
	}

	var msgs []llm.ChatMessage
	for _, r := range raw {
		m := llm.ChatMessage{Role: r.Role, Content: r.Content, TurnID: r.TurnID}
		for _, tc := range r.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		if r.ToolCallID != "" {
			m.ToolCallID = r.ToolCallID
			m.ToolName = r.ToolName
		}
		msgs = append(msgs, m)
	}
	t.Logf("input msgs=%d", len(msgs))

	out := llm.SanitizeMessages(msgs)
	t.Logf("sanitized msgs=%d (dropped %d)", len(out), len(msgs)-len(out))

	// 被剔除条目的位置分布（用内容前缀 + role 定位）
	inSet := map[string]bool{}
	for _, m := range msgs {
		inSet[m.Role+"|"+truncate(m.Content, 50)+"|"+m.ToolCallID] = true
	}
	dropped := 0
	for _, m := range out {
		k := m.Role + "|" + truncate(m.Content, 50) + "|" + m.ToolCallID
		if !inSet[k] {
			dropped++
		}
	}
	_ = dropped

	// 核心断言：成功对（1315873 assistant FileReplace + 1315874 result）必须在
	var hasSuccessAssistant, hasSuccessResult bool
	for _, m := range out {
		if m.Role == "assistant" && len(m.ToolCalls) == 1 && m.ToolCalls[0].Name == "FileReplace" &&
			m.Content == "" {
			hasSuccessAssistant = true
		}
		if m.Role == "tool" && m.ToolName == "FileReplace" &&
			len(m.Content) > 20 && m.Content[:20] == "Successfully replace" {
			hasSuccessResult = true
		}
	}
	if !hasSuccessAssistant {
		t.Fatalf("成功调用的 assistant 消息被剔除——模型看不到自己已调用（prompt 冻结根因）")
	}
	if !hasSuccessResult {
		t.Fatalf("成功 result（Successfully replaced）被剔除——模型看不到成功")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
