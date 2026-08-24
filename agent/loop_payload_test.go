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

// TestLoopPayloadEvolution 终极实验：mock LLM server 记录每次实际发送的
// 请求体，用 DB 导出的真实消息（turn 13 现场 742 条）模拟 LOOP 迭代循环
// （callLLM 的 Sanitize → GenerateStream → append LOOP 对）。
//
// 验证：每次发送的请求体是否递增（新 LOOP 对进入请求）——若不递增
// （重放老请求体）则当场抓获"usage 恒定 + 模型重复输出"的根因。
func TestLoopPayloadEvolution(t *testing.T) {
	dumpPath := os.Getenv("XBOT_REAL_MSG_DUMP")
	if dumpPath == "" {
		t.Skip("需要真实数据：XBOT_REAL_MSG_DUMP=/tmp/turn13_msgs.json")
	}

	var mu sync.Mutex
	var payloads []string
	callCount := 0

	// mock SSE server：返回一个 tool_call（模型"重发"FileReplace）+ usage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		payloads = append(payloads, string(body))
		callCount++
		n := callCount
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		lines := []string{
			fmt.Sprintf(`{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_%d","type":"function","function":{"name":"FileReplace","arguments":"{\"path\":\"/a.ts\"}"}}]},"finish_reason":null}]}`, n),
			`{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"chatcmpl-x","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":300880,"completion_tokens":86}}`,
		}
		for _, l := range lines {
			fmt.Fprintf(w, "data: %s\n\n", l)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer srv.Close()

	// 复刻真实 callLLM 路径：RetryLLM 包装 + thinkingMode enabled + stream callbacks
	inner := llm.NewOpenAILLM(llm.OpenAIConfig{
		BaseURL: srv.URL, APIKey: "test",
	})
	client := llm.NewRetryLLM(inner, llm.DefaultRetryConfig())

	// 加载真实 742 条消息
	data, err := os.ReadFile(dumpPath)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	var raw []struct {
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
	messages := make([]llm.ChatMessage, 0, len(raw))
	for _, r := range raw {
		m := llm.ChatMessage{Role: r.Role, Content: r.Content, TurnID: r.TurnID}
		for _, tc := range r.ToolCalls {
			m.ToolCalls = append(m.ToolCalls, llm.ToolCall{ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments})
		}
		if r.ToolCallID != "" {
			m.ToolCallID = r.ToolCallID
			m.ToolName = r.ToolName
		}
		messages = append(messages, m)
	}
	t.Logf("base messages=%d", len(messages))

	// 模拟 callLLM 循环 ×3（含 Sanitize + append LOOP 对）
	for round := 1; round <= 3; round++ {
		messages = llm.SanitizeMessages(messages) // callLLM 第一行
		resp, err := collectGen(t, client, messages)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if len(resp.ToolCalls) == 0 {
			t.Fatalf("round %d: mock 响应无 tool_calls", round)
		}
		// recordAssistantMsg：append assistant（含 tool_calls）
		messages = append(messages, llm.ChatMessage{
			Role:      "assistant",
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
			TurnID:    13,
		})
		// executeToolCalls：append tool result（LOOP 警告形态）
		for _, tc := range resp.ToolCalls {
			messages = append(messages, llm.NewToolMessage(tc.Name, tc.ID, tc.Arguments,
				"Error: ⚠️ LOOP DETECTED — this duplicate tool call was SKIPPED (not executed)."))
		}
	}

	// 对比三次请求体
	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 3 {
		t.Fatalf("mock server 收到 %d 次请求（期望 3）", len(payloads))
	}
	for i := 0; i < len(payloads); i++ {
		var req struct {
			Messages []json.RawMessage `json:"messages"`
		}
		json.Unmarshal([]byte(payloads[i]), &req)
		var lastTool string
		for _, m := range req.Messages {
			if strings.Contains(string(m), `"role":"tool"`) || strings.Contains(string(m), `"role": "tool"`) {
				lastTool = string(m)
			}
		}
		t.Logf("payload[%d]: bytes=%d msgs=%d last_tool=%s",
			i+1, len(payloads[i]), len(req.Messages), truncateStr(lastTool, 100))
		// 关键断言：请求体必须递增（每轮 +2 条消息 +~330 tokens）
		if i > 0 && len(payloads[i]) <= len(payloads[i-1]) {
			t.Errorf("payload[%d] (%d bytes) ≤ payload[%d] (%d bytes) —— 请求体没有增长：重放老请求体！",
				i+1, len(payloads[i]), i, len(payloads[i-1]))
		}
		if i > 0 && !strings.Contains(payloads[i], "LOOP DETECTED") {
			t.Errorf("payload[%d] 不含上一轮 append 的 LOOP 警告 —— 模型看不到拦截反馈！", i+1)
		}
	}
}

func collectGen(t *testing.T, client *llm.RetryLLM, messages []llm.ChatMessage) (*llm.LLMResponse, error) {
	t.Helper()
	// 真实 callLLM 路径：GenerateStreamAndCollect + thinkingMode enabled + 4 个 stream callbacks
	return client.GenerateStreamAndCollect(t.Context(), "glm-5.3", messages, nil, "enabled",
		func(string) {}, func(string) {}, func([]llm.ToolCallDelta) {}, func(*llm.TokenUsage) {})
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
