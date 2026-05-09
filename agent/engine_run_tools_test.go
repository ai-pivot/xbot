package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"xbot/llm"
)

func TestMaybeMaskObservations_NoTokenDataDoesNotMask(t *testing.T) {
	store := NewObservationMaskStore(100)
	messages := []llm.ChatMessage{
		llm.NewSystemMessage("You are a test agent."),
		llm.NewUserMessage("Inspect these files."),
	}
	for i := 0; i < 13; i++ {
		messages = append(messages, buildToolCallResult(
			"Shell",
			fmt.Sprintf(`{"command":"cat file%d.go"}`, i),
			strings.Repeat("large tool result ", 100),
		)...)
	}

	state := &runState{
		cfg: RunConfig{
			MaskStore: store,
		},
		messages: messages,
	}

	state.maybeMaskObservations(context.Background(), 0, 1_000_000)

	if store.Size() != 0 {
		t.Fatalf("expected no masking without API token data, got %d masked entries", store.Size())
	}
	for i, msg := range state.messages {
		if strings.Contains(msg.Content, "📂 [batch:") || strings.Contains(msg.Content, "📂 [masked:") || strings.Contains(msg.Content, "📂 [batch-masked:") {
			t.Fatalf("message %d was masked without API token data: %q", i, msg.Content)
		}
	}
}
