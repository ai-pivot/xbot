package channel

import (
	"encoding/json"
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestConvertHistoryRecordsReturnsOneRowPerMessageAndCompression(t *testing.T) {
	snapshot, err := json.Marshal(sqlite.ContextSnapshot{
		Messages:   []llm.ChatMessage{{Role: "user", Content: "[Compacted context]\nsummary"}},
		HistoryIDs: []int64{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := json.Marshal([]map[string]any{{
		"iteration": 1,
		"reasoning": "checking",
		"tools":     []map[string]any{{"name": "Read", "status": "done"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	records := []sqlite.HistoryRecord{
		// Deliberately unordered: the public projection is ordered by history_id.
		{HistoryID: 8, Type: sqlite.HistoryRecordCompress, Data: snapshot, CreatedAt: time.Unix(8, 0), Compression: &sqlite.CompressionRange{StartHistoryID: 5, EndHistoryID: 7, SourceHistoryIDs: []int64{5, 7}}},
		{HistoryID: 6, Type: sqlite.HistoryRecordMask, CreatedAt: time.Unix(6, 0)},
		{HistoryID: 4, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 4, Role: "assistant", Detail: string(detail), DisplayOnly: true, Timestamp: time.Unix(4, 0)}},
		{HistoryID: 2, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 2, Role: "assistant", ReasoningContent: "thinking", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`}}, Timestamp: time.Unix(2, 0)}, CompactedBy: 5},
		{HistoryID: 1, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 1, Role: "user", Content: "raw", Timestamp: time.Unix(1, 0)}, CompactedBy: 5},
		{HistoryID: 5, Type: sqlite.HistoryRecordCompress, Data: snapshot, CreatedAt: time.Unix(5, 0), CompactedBy: 8, Compression: &sqlite.CompressionRange{StartHistoryID: 1, EndHistoryID: 4, SourceHistoryIDs: []int64{1, 2, 3, 4}}},
		{HistoryID: 3, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 3, Role: "tool", Content: "file", ToolCallID: "call-1", ToolName: "Read", ToolArguments: `{"path":"README.md"}`, Timestamp: time.Unix(3, 0)}, CompactedBy: 5},
		{HistoryID: 7, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 7, Role: "user", Content: "follow-up", Timestamp: time.Unix(7, 0)}, CompactedBy: 8},
	}
	history := ConvertHistoryRecords(records)
	if len(history) != 7 {
		t.Fatalf("history=%+v", history)
	}
	wantIDs := []int64{1, 2, 3, 4, 5, 7, 8}
	for i, wantID := range wantIDs {
		if history[i].HistoryID != wantID {
			t.Fatalf("history IDs=%v, want %v", historyIDs(history), wantIDs)
		}
	}
	if history[0].CompactedBy != 5 || history[0].Content != "raw" {
		t.Fatalf("raw source=%+v", history[0])
	}
	assistant := history[1]
	if assistant.Role != "assistant" || assistant.ReasoningContent != "thinking" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call-1" || len(assistant.Iterations) != 1 {
		t.Fatalf("assistant tool call=%+v", assistant)
	}
	tool := history[2]
	if tool.Role != "tool" || tool.ToolCallID != "call-1" || tool.ToolName != "Read" || tool.ToolArguments == "" {
		t.Fatalf("tool row=%+v", tool)
	}
	emptyAssistant := history[3]
	if emptyAssistant.Role != "assistant" || emptyAssistant.Content != "" || !emptyAssistant.DisplayOnly || len(emptyAssistant.Iterations) != 1 {
		t.Fatalf("display-only assistant=%+v", emptyAssistant)
	}
	markers := []HistoryMessage{history[4], history[6]}
	if markers[0].RecordType != "compress" || markers[0].CompactedBy != 8 || markers[0].Compression == nil || markers[0].Compression.StartHistoryID != 1 {
		t.Fatalf("first compression marker=%+v", markers[0])
	}
	if markers[1].RecordType != "compress" || markers[1].Compression == nil || markers[1].Compression.StartHistoryID != 5 {
		t.Fatalf("second compression marker=%+v", markers[1])
	}
	returned := make(map[int64]bool, len(history))
	for _, row := range history {
		returned[row.HistoryID] = true
	}
	for _, marker := range markers {
		for _, sourceID := range marker.Compression.SourceHistoryIDs {
			if !returned[sourceID] {
				t.Fatalf("compression source %d has no returned row", sourceID)
			}
		}
	}
	encoded, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	var wireRows []map[string]any
	if err := json.Unmarshal(encoded, &wireRows); err != nil {
		t.Fatal(err)
	}
	if wireRows[1]["reasoning_content"] != "thinking" || len(wireRows[1]["tool_calls"].([]any)) != 1 {
		t.Fatalf("assistant wire row=%v", wireRows[1])
	}
	if wireRows[2]["role"] != "tool" || wireRows[2]["tool_call_id"] != "call-1" || wireRows[3]["display_only"] != true {
		t.Fatalf("raw message wire rows=%v", wireRows[2:4])
	}
}

func historyIDs(history []HistoryMessage) []int64 {
	ids := make([]int64, len(history))
	for i, row := range history {
		ids[i] = row.HistoryID
	}
	return ids
}

func TestConvertMessagesToHistoryUsesFinalAssistantHistoryIDAfterMerge(t *testing.T) {
	detail, err := json.Marshal([]map[string]any{{"iteration": 1, "content": "working"}})
	if err != nil {
		t.Fatal(err)
	}
	history := ConvertMessagesToHistory([]llm.ChatMessage{
		{HistoryID: 10, Role: "assistant", Detail: string(detail)},
		{HistoryID: 11, Role: "assistant", Content: "final"},
	})
	if len(history) != 1 || history[0].HistoryID != 11 || history[0].Content != "final" {
		t.Fatalf("merged assistant=%+v", history)
	}
}
