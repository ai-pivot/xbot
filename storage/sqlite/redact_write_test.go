package sqlite

// redact_write_test.go — 写入点判据（W4 会话库 session_messages / W5 的 iteration_history）。
//
// 全部使用**假 key**（sk-BINGBUFAKE…），不含任何真实凭据。兵部探针只验 session_messages
// 的 content 一列；这里补齐 tool_arguments / detail / reasoning_content、结构不变性，
// 以及 iteration_history 两条独立写入路径（AppendIterationHistory / AppendIterationTool）。

import (
	"encoding/json"
	"strings"
	"testing"

	"xbot/llm"
)

const w4SessionKey = "sk-BINGBUFAKE999888777abcdefghijklmnopqrstuvwxyz0123"

const w4SessionMarker = "PROBEMARKER-W4"

// newRedactTestSession 建一个独立 tenant 的会话库服务。
func newRedactTestSession(t *testing.T) (*SessionService, int64) {
	t.Helper()
	db := openTestDB(t)
	tenantID, err := NewTenantService(db).GetOrCreateTenantID("web", "redact-write")
	if err != nil {
		t.Fatalf("GetOrCreateTenantID: %v", err)
	}
	return NewSessionService(db), tenantID
}

func TestAppendMessageRedactsStringFieldsOnly(t *testing.T) {
	svc, tenantID := newRedactTestSession(t)

	msg := llm.ChatMessage{
		Role:             "tool",
		Content:          w4SessionMarker + " content Bearer " + w4SessionKey,
		ToolName:         "Shell",
		ToolCallID:       "call-1",
		ToolArguments:    `{"cmd":"curl -H 'api_key=` + w4SessionKey + `'"}` + " " + w4SessionMarker,
		Detail:           w4SessionMarker + " detail token=" + w4SessionKey,
		ReasoningContent: w4SessionMarker + " reasoning Bearer " + w4SessionKey,
		TurnID:           7,
		Iteration:        3,
	}
	before := strings.Count(msg.Content, w4SessionMarker)
	if before != 1 {
		t.Fatalf("fixture 自身有问题: marker 出现 %d 次", before)
	}
	if _, err := svc.AppendMessage(tenantID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	conn := svc.db.Conn()
	var role, content, toolName, toolCallID, toolArgs, detail, reasoning string
	var turnID int64
	var iteration int
	var createdAt string
	if err := conn.QueryRow(`
		SELECT role, content, tool_name, tool_call_id, tool_arguments, detail, reasoning_content,
		       turn_id, iteration, created_at
		FROM session_messages ORDER BY id DESC LIMIT 1`).
		Scan(&role, &content, &toolName, &toolCallID, &toolArgs, &detail, &reasoning,
			&turnID, &iteration, &createdAt); err != nil {
		t.Fatalf("select session row: %v", err)
	}

	// 1) 四个字符串列都不许有原值，且内容没被整段吞掉（标记词仍在）
	for label, got := range map[string]string{
		"content": content, "tool_arguments": toolArgs, "detail": detail, "reasoning_content": reasoning,
	} {
		if strings.Contains(got, w4SessionKey) {
			t.Errorf("%s 仍含假 key: %q", label, got)
		}
		if !strings.Contains(got, w4SessionMarker) {
			t.Errorf("%s 丢了标记词（过度脱敏）: %q", label, got)
		}
	}

	// 2) 结构/角色/ID/时间戳一字不动
	if role != msg.Role || toolName != msg.ToolName || toolCallID != msg.ToolCallID {
		t.Errorf("结构字段被改: role=%q tool_name=%q tool_call_id=%q", role, toolName, toolCallID)
	}
	if turnID != 7 || iteration != 3 {
		t.Errorf("turn/iteration 被改: turn=%d iter=%d", turnID, iteration)
	}
	if createdAt == "" {
		t.Errorf("created_at 为空（时间戳不该被动）")
	}
}

func TestAppendIterationHistoryRedactsAndKeepsJSONValid(t *testing.T) {
	svc, tenantID := newRedactTestSession(t)

	rec := IterationRecord{
		Iteration: 1,
		Content:   w4SessionMarker + " 迭代正文 Bearer " + w4SessionKey,
		Reasoning: w4SessionMarker + " 思考 api_key=" + w4SessionKey,
		Tools: `[{"name":"Shell","args":"{\"cmd\":\"curl -H 'Authorization: Bearer ` +
			w4SessionKey + `'\"}","ok":true,"tokens":42}]`,
	}
	if err := svc.AppendIterationHistory(tenantID, 0, 9, rec); err != nil {
		t.Fatalf("AppendIterationHistory: %v", err)
	}

	conn := svc.db.Conn()
	var content, reasoning, tools string
	if err := conn.QueryRow(
		`SELECT content, reasoning, tools FROM iteration_history WHERE tenant_id = ? AND turn_id = 9`,
		tenantID,
	).Scan(&content, &reasoning, &tools); err != nil {
		t.Fatalf("select iteration row: %v", err)
	}
	if strings.Contains(content, w4SessionKey) {
		t.Errorf("iteration content 仍含假 key: %q", content)
	}
	if !strings.Contains(content, w4SessionMarker) {
		t.Errorf("iteration content 丢了标记词: %q", content)
	}
	if strings.Contains(reasoning, w4SessionKey) {
		t.Errorf("iteration reasoning 仍含假 key: %q", reasoning)
	}
	if strings.Contains(tools, w4SessionKey) {
		t.Errorf("iteration tools 仍含假 key: %q", tools)
	}
	if !json.Valid([]byte(tools)) {
		t.Fatalf("iteration tools 脱敏后不再是合法 JSON: %q", tools)
	}
	var arr []map[string]any
	if err := json.Unmarshal([]byte(tools), &arr); err != nil {
		t.Fatalf("iteration tools 解析失败: %v (%q)", err, tools)
	}
	if len(arr) != 1 || arr[0]["name"] != "Shell" {
		t.Errorf("iteration tools 结构被破坏: %#v", arr)
	}
}

func TestAppendIterationToolRedactsAndKeepsJSONValid(t *testing.T) {
	svc, tenantID := newRedactTestSession(t)

	if err := svc.AppendIterationHistory(tenantID, 0, 11, IterationRecord{Iteration: 2, Content: "x"}); err != nil {
		t.Fatalf("seed AppendIterationHistory: %v", err)
	}
	toolJSON := `{"name":"Shell","args":"{\"env\":\"api_key=` + w4SessionKey + `\"}","ok":true}`
	found, err := svc.AppendIterationTool(tenantID, 11, 2, toolJSON)
	if err != nil {
		t.Fatalf("AppendIterationTool: %v", err)
	}
	if !found {
		t.Fatalf("AppendIterationTool 没找到目标行")
	}

	var tools string
	if err := svc.db.Conn().QueryRow(
		`SELECT tools FROM iteration_history WHERE tenant_id = ? AND turn_id = 11`, tenantID,
	).Scan(&tools); err != nil {
		t.Fatalf("select tools: %v", err)
	}
	if strings.Contains(tools, w4SessionKey) {
		t.Errorf("AppendIterationTool 落库仍含假 key: %q", tools)
	}
	if !json.Valid([]byte(tools)) {
		t.Fatalf("AppendIterationTool 落库不是合法 JSON: %q", tools)
	}

	// 前置 json.Valid 不被绕过：非法 JSON 仍然直接报错
	if _, err := svc.AppendIterationTool(tenantID, 11, 2, `{not json`); err == nil {
		t.Errorf("非法 JSON 未被拒绝")
	}
}

// TestAppendMessageKeepsBenignTextByteIdentical 非凭据文本逐字节不变（防误伤）。
func TestAppendMessageKeepsBenignTextByteIdentical(t *testing.T) {
	svc, tenantID := newRedactTestSession(t)

	benign := "中文原样；sk-learn；total_tokens=5；task-frontend-cancel-reason-consume"
	if _, err := svc.AppendMessage(tenantID, llm.ChatMessage{Role: "tool", Content: benign}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	var got string
	if err := svc.db.Conn().QueryRow(
		`SELECT content FROM session_messages ORDER BY id DESC LIMIT 1`).Scan(&got); err != nil {
		t.Fatalf("select content: %v", err)
	}
	if got != benign {
		t.Errorf("非凭据文本被改写:\n want=%q\n got=%q", benign, got)
	}
}

// TestAppendMessageRedactsToolCallsDeepCopy 工部 00:30Z 追加项：tool_calls 列（ToolCall.Arguments
// 是同一批参数的又一份副本）必须洗，且**不能原地改调用方的切片**。
func TestAppendMessageRedactsToolCallsDeepCopy(t *testing.T) {
	svc, tenantID := newRedactTestSession(t)

	original := `{"k":"` + w4SessionKey + `"}`
	msg := llm.ChatMessage{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{
			{ID: "call_x", Name: "Shell", Arguments: original},
			{ID: "call_y", Name: "Read", Arguments: `{"path":"/tmp/a"}`},
		},
		Detail: "shell 输出：" + w4SessionKey + "（假 key）" + w4SessionMarker,
	}
	if _, err := svc.AppendMessage(tenantID, msg); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	var toolCalls, detail string
	if err := svc.db.Conn().QueryRow(
		`SELECT tool_calls, detail FROM session_messages ORDER BY id DESC LIMIT 1`,
	).Scan(&toolCalls, &detail); err != nil {
		t.Fatalf("select tool_calls/detail: %v", err)
	}

	// 1) 两列都 0 命中假 key
	if strings.Contains(toolCalls, w4SessionKey) {
		t.Errorf("tool_calls 列仍含假 key: %q", toolCalls)
	}
	if strings.Contains(detail, w4SessionKey) {
		t.Errorf("detail 列仍含假 key: %q", detail)
	}
	if !strings.Contains(detail, w4SessionMarker) {
		t.Errorf("detail 丢了标记词（过度脱敏）: %q", detail)
	}

	// 2) tool_calls 仍是合法 JSON，且结构字段（id/name）未变
	var got []llm.ToolCall
	if err := json.Unmarshal([]byte(toolCalls), &got); err != nil {
		t.Fatalf("tool_calls 不是合法 JSON: %v (%q)", err, toolCalls)
	}
	if len(got) != 2 || got[0].ID != "call_x" || got[0].Name != "Shell" ||
		got[1].ID != "call_y" || got[1].Name != "Read" {
		t.Errorf("tool_calls 结构被破坏: %#v", got)
	}
	if strings.Contains(got[0].Arguments, w4SessionKey) {
		t.Errorf("tool_calls[0].arguments 仍含假 key: %q", got[0].Arguments)
	}
	if got[1].Arguments != `{"path":"/tmp/a"}` {
		t.Errorf("非凭据 arguments 被改写: %q", got[1].Arguments)
	}

	// 3) 不可变性：调用方持有的切片必须没被原地改
	if msg.ToolCalls[0].Arguments != original {
		t.Errorf("调用方 ToolCalls 被原地修改: %q", msg.ToolCalls[0].Arguments)
	}
}
