package channel

import (
	"encoding/json"
	"testing"
	"time"

	"xbot/llm"
	"xbot/storage/sqlite"
)

func TestConvertHistoryRecordsKeepsCompactedSourcesAndControlMetadata(t *testing.T) {
	snapshot, err := json.Marshal(sqlite.ContextSnapshot{
		Messages:   []llm.ChatMessage{{Role: "user", Content: "[Compacted context]\nsummary"}},
		HistoryIDs: []int64{0},
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []sqlite.HistoryRecord{
		{HistoryID: 1, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 1, Role: "user", Content: "raw", Timestamp: time.Unix(1, 0)}, CompactedBy: 3},
		{HistoryID: 2, Type: sqlite.HistoryRecordMessage, Message: llm.ChatMessage{HistoryID: 2, Role: "assistant", Content: "answer", Timestamp: time.Unix(2, 0)}, CompactedBy: 3},
		{HistoryID: 3, Type: sqlite.HistoryRecordCompress, Data: snapshot, CreatedAt: time.Unix(3, 0), Compression: &sqlite.CompressionRange{StartHistoryID: 1, EndHistoryID: 2, SourceHistoryIDs: []int64{1, 2}}},
	}
	history := ConvertHistoryRecords(records)
	if len(history) != 3 {
		t.Fatalf("history=%+v", history)
	}
	if history[0].HistoryID != 1 || history[0].CompactedBy != 3 || history[0].Content != "raw" {
		t.Fatalf("raw source=%+v", history[0])
	}
	marker := history[2]
	if marker.HistoryID != 3 || marker.RecordType != "compress" || marker.Compression == nil || marker.Compression.StartHistoryID != 1 {
		t.Fatalf("compression marker=%+v", marker)
	}
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
