package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"xbot/llm"
)

// TestSanitizeFullMessages926 用 926E2AB6C6B9 会话的全量真实消息跑
// SanitizeMessages，验证 LOOP 拦截对与成功对是否幸存。
// XBOT_FULL_MSG_DUMP=/tmp/full_msgs_1319141.json
// XBOT_TAIL_N=20 可选：只取尾部 N 条做二分定位。
func TestSanitizeFullMessages926(t *testing.T) {
	path := os.Getenv("XBOT_FULL_MSG_DUMP")
	if path == "" {
		t.Skip("XBOT_FULL_MSG_DUMP=... go test")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
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
		t.Fatalf("parse: %v", err)
	}
	// 尾部二分定位（raw 层切片，保留 db_id）
	if tailN := os.Getenv("XBOT_TAIL_N"); tailN != "" {
		var n int
		fmt.Sscanf(tailN, "%d", &n)
		if n > 0 && n < len(raw) {
			raw = raw[len(raw)-n:]
			t.Logf("tail mode: last %d msgs (db_id %d..%d)", n, raw[0].DBID, raw[len(raw)-1].DBID)
		}
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
	t.Logf("input: %d msgs", len(msgs))

	out := llm.SanitizeMessages(msgs)
	t.Logf("sanitized: %d msgs (dropped %d)", len(out), len(msgs)-len(out))

	// 核心断言（HasPrefix——无切片坑）
	hasLoopResult, hasSuccessResult := false, false
	for _, m := range out {
		if m.Role == "tool" && strings.HasPrefix(m.Content, "Error: ⚠️ LOOP DETECTED") {
			hasLoopResult = true
		}
		if m.Role == "tool" && strings.HasPrefix(m.Content, "Successfully replaced 1 ") {
			hasSuccessResult = true
		}
	}
	if !hasSuccessResult {
		t.Errorf("🔴 成功 result 被剔除——模型看不到自己的成功")
	}
	if !hasLoopResult {
		t.Errorf("🔴 LOOP 拦截 result 被剔除——模型看不到拦截反馈")
	}
	if hasLoopResult && hasSuccessResult {
		t.Logf("✅ 成功对与 LOOP 对均幸存")
	}

	// 精确 DROPPED 分析：tool 用唯一 tool_call_id
	if len(out) < len(msgs) {
		outToolIDs := map[string]bool{}
		for _, m := range out {
			if m.ToolCallID != "" {
				outToolIDs[m.ToolCallID] = true
			}
		}
		for _, m := range msgs {
			if m.ToolCallID != "" && !outToolIDs[m.ToolCallID] {
				t.Logf("  DROPPED tool: toolCallID=%s content=%q", m.ToolCallID, truncStr(m.Content, 60))
			}
		}
	}
}

func truncStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
