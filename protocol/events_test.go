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
