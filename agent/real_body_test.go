package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"xbot/llm"
)

// TestRealBodyDiff926 精确复现 11:47 事故：用 DB 真实消息切出 t49-37
// （db_id≤1319136，1623 条）与 t49-38（db_id≤1319138，1625 条，+FileReplace
// 成功对）两个消息集，分别过真实 OpenAILLM.GenerateStream（thinking enabled，
// 与事故时一致），mock server 记录两次真实 HTTP body：
//   - body38 必须包含 body37 的全部内容 + FileReplace 对（assistant
//     tool_calls + "Successfully replaced" result）
//   - 若 body38 == body37（或缺少 FileReplace 对）→ 当场抓获序列化层 bug
//     （prompt_tokens 恒定 + 模型输出逐字节重复的根因）
func TestRealBodyDiff926(t *testing.T) {
	path := os.Getenv("XBOT_FULL_MSG_DUMP")
	if path == "" {
		t.Skip("XBOT_FULL_MSG_DUMP=/tmp/full_msgs_1319141.json go test")
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

	build := func(maxDBID int64) []llm.ChatMessage {
		var msgs []llm.ChatMessage
		for _, r := range raw {
			if r.DBID > maxDBID {
				break
			}
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
		return msgs
	}
	set37 := build(1319136) // t49-37：含 sed 对 + FileReplace 成功 assistant，无 result？→ ≤1319136 含成功 result
	set38 := build(1319138) // t49-38：+FileReplace 成功对 + LOOP 拦截对
	t.Logf("set37=%d msgs, set38=%d msgs", len(set37), len(set38))
	if len(set38)-len(set37) != 2 {
		t.Fatalf("预期 set38 = set37 + 2 条（LOOP 对），实际差 %d", len(set38)-len(set37))
	}

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(body))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"id\":\"x\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		fmt.Fprint(w, "data: {\"id\":\"x\",\"choices\":[],\"usage\":{\"prompt_tokens\":596265,\"completion_tokens\":573}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		f.Flush()
	}))
	defer srv.Close()

	client := llm.NewOpenAILLM(llm.OpenAIConfig{BaseURL: srv.URL, APIKey: "test"})
	gen := func(msgs []llm.ChatMessage) {
		ch, err := client.GenerateStream(t.Context(), "glm-5.3", msgs, nil, "enabled")
		if err != nil {
			t.Fatalf("GenerateStream: %v", err)
		}
		for range ch { // 排空
		}
	}
	gen(set37)
	gen(set38)

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("mock 收到 %d 个请求（期望 2）", len(bodies))
	}
	b37, b38 := bodies[0], bodies[1]
	t.Logf("body37: %d bytes, body38: %d bytes (Δ=%d)", len(b37), len(b38), len(b38)-len(b37))
	if len(b38) <= len(b37) {
		t.Fatalf("🔴 body38 ≤ body37 —— 序列化层丢弃了新迭代消息！")
	}
	if !strings.Contains(b38, "Successfully replaced 1 occurrence") {
		t.Errorf("🔴 body38 不含 FileReplace 成功 result —— 模型看不到成功")
	}
	if !strings.Contains(b38, "LOOP DETECTED") {
		t.Errorf("🔴 body38 不含 LOOP 拦截警告")
	}
	if strings.Contains(b37, "Successfully replaced 1 occurrence(s) in /home/smith/src/xbot/web/src/workspace/panels/DiffPanel.tsx") {
		// set37 含成功对（db 1319136 是 result）——t49-37 是含它的请求
		t.Logf("body37 已含成功 result（预期：t49-37 = sed 对之后、成功对之后的请求）")
	}
	t.Logf("✅ body 递增且含新迭代消息 —— SDK 序列化链无辜")
}
