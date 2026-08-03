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
		{HistoryID: 4, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{ID: 4, Role: "assistant", Detail: string(detail), DisplayOnly: true, Timestamp: time.Unix(4, 0)}},
		{HistoryID: 2, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{ID: 2, Role: "assistant", ReasoningContent: "thinking", ToolCalls: []llm.ToolCall{{ID: "call-1", Name: "Read", Arguments: `{"path":"README.md"}`}}, Timestamp: time.Unix(2, 0)}, CompactedBy: 5},
		{HistoryID: 1, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{ID: 1, Role: "user", Content: "raw", Timestamp: time.Unix(1, 0)}, CompactedBy: 5},
		{HistoryID: 5, Type: sqlite.HistoryRecordCompress, Data: snapshot, CreatedAt: time.Unix(5, 0), CompactedBy: 8, Compression: &sqlite.CompressionRange{StartHistoryID: 1, EndHistoryID: 4, SourceHistoryIDs: []int64{1, 2, 3, 4}}},
		{HistoryID: 3, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{ID: 3, Role: "tool", Content: "file", ToolCallID: "call-1", ToolName: "Read", ToolArguments: `{"path":"README.md"}`, Timestamp: time.Unix(3, 0)}, CompactedBy: 5},
		{HistoryID: 7, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{ID: 7, Role: "user", Content: "follow-up", Timestamp: time.Unix(7, 0)}, CompactedBy: 8},
	}
	history := ConvertHistoryRecords(records)
	// Tool-role messages and display_only messages are skipped.
	// 7 records → 5 output rows (tool #3 and display_only #4 excluded).
	if len(history) != 5 {
		t.Fatalf("history=%+v", history)
	}
	wantIDs := []int64{1, 2, 5, 7, 8}
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
	// display_only assistant (ID=4) is skipped — next row is compress marker.
	markers := []HistoryMessage{history[2], history[4]}
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
	// Compression source IDs may include tool/display_only rows that were skipped.
	for _, marker := range markers {
		for _, sourceID := range marker.Compression.SourceHistoryIDs {
			// Source IDs 3 (tool) and 4 (display_only) are skipped from output.
			if sourceID == 3 || sourceID == 4 {
				continue
			}
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
	// display_only and tool messages are skipped — wireRows[2] should be
	// the first compression marker.
	if wireRows[2]["record_type"] != "compress" {
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
		{ID: 10, Role: "assistant", Detail: string(detail)},
		{ID: 11, Role: "assistant", Content: "final"},
	})
	if len(history) != 1 || history[0].HistoryID != 11 || history[0].Content != "final" {
		t.Fatalf("merged assistant=%+v", history)
	}
}
