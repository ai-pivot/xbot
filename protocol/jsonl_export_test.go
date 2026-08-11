package protocol

import (
	"encoding/json"
	"testing"

	"xbot/llm"
)

func TestExportSessionJSONL_BasicTurn(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "system", Content: "You are helpful."},
		{Role: "user", Content: "What is 2+2?"},
		{Role: "assistant", Content: "4", ReasoningContent: "simple arithmetic"},
		{Role: "user", Content: "And 3*3?"},
		{Role: "assistant", Content: "9"},
	}
	records := ExportSessionJSONL("/home/test", msgs)
	if len(records) != 2 {
		t.Fatalf("expected 2 records (one per user turn), got %d", len(records))
	}

	r0 := records[0]
	if r0.Question != "What is 2+2?" {
		t.Errorf("record[0].question = %q", r0.Question)
	}
	if r0.Answer != "4" {
		t.Errorf("record[0].answer = %q", r0.Answer)
	}
	if r0.UUID != "-home-test:1" {
		t.Errorf("record[0].uuid = %q, want '-home-test:1'", r0.UUID)
	}
	// system message must be skipped
	if len(r0.Messages) != 2 {
		t.Fatalf("expected 2 messages in record[0] (request+response), got %d", len(r0.Messages))
	}
	if r0.Messages[0].Kind != "request" || r0.Messages[0].Parts[0].PartKind != "user-prompt" {
		t.Errorf("messages[0] = %+v", r0.Messages[0])
	}
	resp := r0.Messages[1]
	if resp.Kind != "response" {
		t.Errorf("messages[1].kind = %q, want response", resp.Kind)
	}
	if len(resp.Parts) != 2 || resp.Parts[0].PartKind != "thinking" || resp.Parts[1].PartKind != "text" {
		t.Errorf("response parts = %+v, want thinking+text", resp.Parts)
	}
	// absent values use "None"
	if resp.Parts[0].ToolName != "None" || resp.Parts[0].ToolCallID != "None" || resp.Parts[0].Args != "None" {
		t.Errorf("thinking part should have None fields, got %+v", resp.Parts[0])
	}
}

func TestExportSessionJSONL_ToolCalls(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "Solve it"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "python_0", Name: "python", Arguments: `{"code":"print(1)"}`}}},
		{Role: "tool", Content: "1\n", ToolCallID: "python_0", ToolName: "python"},
		{Role: "assistant", Content: "Answer is 1", ReasoningContent: "computed"},
	}
	records := ExportSessionJSONL("/home/test", msgs)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	r := records[0]
	// request(user) → response(tool-call) → request(tool-return) → response(thinking+text)
	if len(r.Messages) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(r.Messages), r.Messages)
	}
	tc := r.Messages[1]
	if tc.Kind != "response" || tc.Parts[0].PartKind != "tool-call" {
		t.Fatalf("messages[1] = %+v, want response with tool-call", tc)
	}
	tr := r.Messages[2]
	if tr.Kind != "request" || tr.Parts[0].PartKind != "tool-return" {
		t.Fatalf("messages[2] = %+v, want request with tool-return", tr)
	}
	if tr.Parts[0].ToolName != "python" || tr.Parts[0].ToolCallID != "python_0" {
		t.Errorf("tool-return fields = %+v", tr.Parts[0])
	}
	if r.Answer != "Answer is 1" {
		t.Errorf("answer = %q", r.Answer)
	}
}

func TestImportExportSessionJSONL_RoundTrip(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "Q1"},
		{Role: "assistant", Content: "A1", ReasoningContent: "think1"},
		{Role: "user", Content: "Q2"},
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "t1", Name: "sh", Arguments: `{"cmd":"ls"}`}}},
		{Role: "tool", Content: "out", ToolCallID: "t1", ToolName: "sh"},
		{Role: "assistant", Content: "A2"},
	}
	records := ExportSessionJSONL("/home/x", msgs)
	got := ImportSessionJSONL(records)

	// user + assistant for turn 1; user + assistant + tool + assistant for turn 2
	if len(got) != 6 {
		t.Fatalf("expected 6 messages round-tripped, got %d: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "Q1" {
		t.Errorf("msg[0] = %+v", got[0])
	}
	if got[1].Role != "assistant" || got[1].Content != "A1" || got[1].ReasoningContent != "think1" {
		t.Errorf("msg[1] = %+v", got[1])
	}
	// tool-call part merges into ONE assistant message with ToolCalls
	if got[3].Role != "assistant" || len(got[3].ToolCalls) != 1 {
		t.Fatalf("msg[3] = %+v, want assistant with 1 tool call", got[3])
	}
	if got[3].ToolCalls[0].ID != "t1" || got[3].ToolCalls[0].Name != "sh" {
		t.Errorf("tool call = %+v", got[3].ToolCalls[0])
	}
	if got[4].Role != "tool" || got[4].Content != "out" || got[4].ToolCallID != "t1" {
		t.Errorf("msg[4] = %+v, want tool message", got[4])
	}
	if got[5].Role != "assistant" || got[5].Content != "A2" {
		t.Errorf("msg[5] = %+v", got[5])
	}
}

func TestExportSessionJSONL_DisplayOnlySkipped(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "Q"},
		{Role: "assistant", Content: "A", DisplayOnly: true}, // synthetic — skipped
		{Role: "assistant", Content: "Real answer"},
	}
	records := ExportSessionJSONL("/home/x", msgs)
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].Answer != "Real answer" {
		t.Errorf("answer = %q, DisplayOnly message must be skipped", records[0].Answer)
	}
}

func TestImportSessionJSONL_HandlesNone(t *testing.T) {
	// A record produced by the benchmark has "None" placeholders.
	raw := `{"uuid":"bio:1","question":"Q","answer":"A","domain":"Bio","messages":[
	  {"kind":"request","parts":[{"part_kind":"user-prompt","content":"Q","tool_name":"None","tool_call_id":"None","args":"None"}]},
	  {"kind":"response","parts":[{"part_kind":"tool-call","content":"None","tool_name":"python","tool_call_id":"py_0","args":"{\"code\":\"x\"}"}]},
	  {"kind":"request","parts":[{"part_kind":"tool-return","content":"42","tool_name":"python","tool_call_id":"py_0","args":"None"}]},
	  {"kind":"response","parts":[{"part_kind":"text","content":"A","tool_name":"None","tool_call_id":"None","args":"None"}]}
	],"correct":false,"judge_applied":false}`
	var rec DemoRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	msgs := ImportSessionJSONL([]DemoRecord{rec})
	if len(msgs) != 4 {
		t.Fatalf("expected 4 messages, got %d: %+v", len(msgs), msgs)
	}
	// tool message: "None" placeholders must map to empty strings, real
	// values preserved.
	if msgs[2].Role != "tool" || msgs[2].ToolName != "python" || msgs[2].ToolCallID != "py_0" {
		t.Errorf("tool msg = %+v", msgs[2])
	}
	// tool-call args must be preserved
	if msgs[1].ToolCalls[0].Arguments != `{"code":"x"}` {
		t.Errorf("tool call args = %q", msgs[1].ToolCalls[0].Arguments)
	}
}

func TestExportSessionJSONL_JSONLSerializable(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: "user", Content: "Hi"},
		{Role: "assistant", Content: "Hello"},
	}
	records := ExportSessionJSONL("/tmp/session", msgs)
	var lines []string
	for _, r := range records {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatalf("marshal record: %v", err)
		}
		lines = append(lines, string(b))
	}
	// Each line must be valid standalone JSON (benchmark jsonl format).
	for _, line := range lines {
		var check map[string]any
		if err := json.Unmarshal([]byte(line), &check); err != nil {
			t.Errorf("line is not valid JSON: %v: %s", err, line)
		}
		for _, key := range []string{"uuid", "question", "answer", "domain", "messages", "correct", "judge_applied"} {
			if _, ok := check[key]; !ok {
				t.Errorf("line missing key %q", key)
			}
		}
	}
}
