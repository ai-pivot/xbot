package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubAgentSessionKeyJSON(t *testing.T) {
	infoJSON, err := json.Marshal(SubAgentInfo{Role: "reviewer", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(infoJSON), "session_key") {
		t.Fatalf("empty SubAgentInfo session key must be omitted: %s", infoJSON)
	}
	infoJSON, err = json.Marshal(SubAgentInfo{
		Role:       "reviewer",
		SessionKey: "cli:chat-1/reviewer:review-1",
		Status:     "running",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(infoJSON), `"session_key":"cli:chat-1/reviewer:review-1"`) {
		t.Fatalf("SubAgentInfo session key missing: %s", infoJSON)
	}

	eventJSON, err := json.Marshal(SessionEvent{
		Channel:         "cli",
		ChatID:          "chat-1",
		Action:          "history_rewound",
		SessionKey:      "cli:chat-1/reviewer:review-1",
		TargetHistoryID: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(eventJSON), `"session_key":"cli:chat-1/reviewer:review-1"`) {
		t.Fatalf("SessionEvent session key missing: %s", eventJSON)
	}
	if !strings.Contains(string(eventJSON), `"target_history_id":42`) {
		t.Fatalf("SessionEvent target history ID missing: %s", eventJSON)
	}
}

func TestAskUserQuestionJSONRoundTrip(t *testing.T) {
	// New multi-select / allow-other fields serialize and round-trip.
	q := AskUserQuestion{
		Question:    "Pick options",
		Options:     []string{"a", "b"},
		MultiSelect: true,
		AllowOther:  true,
	}
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	raw := string(data)
	for _, want := range []string{`"multi_select":true`, `"allow_other":true`} {
		if !strings.Contains(raw, want) {
			t.Fatalf("missing %s in %s", want, raw)
		}
	}
	var back AskUserQuestion
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.MultiSelect || !back.AllowOther || len(back.Options) != 2 || back.Question != "Pick options" {
		t.Fatalf("round-trip mismatch: %+v", back)
	}

	// Plain question omits the new fields (backward compatible).
	plain := AskUserQuestion{Question: "Continue?"}
	data, err = json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "multi_select") || strings.Contains(string(data), "allow_other") {
		t.Fatalf("zero-value new fields must be omitted: %s", data)
	}
}
