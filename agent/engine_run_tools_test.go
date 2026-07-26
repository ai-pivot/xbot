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

func TestBeginIterationClearsSubAgentNodes(t *testing.T) {
	state := &runState{
		structuredProgress: &StructuredProgress{
			Iteration: 0,
			Phase:     PhaseDone,
		},
		subAgentNodes: []SubAgentNode{
			{Role: "explore", Instance: "oneshot-1", Status: "running"},
		},
	}

	// SubAgent completed in iteration 0. beginIteration(1) must clear it —
	// carrying it forward causes the "explore card that never disappears" bug.
	state.beginIteration(1)

	if len(state.subAgentNodes) != 0 {
		t.Fatalf("expected subAgentNodes cleared at iteration boundary, got %d nodes", len(state.subAgentNodes))
	}
	if len(state.structuredProgress.SubAgents) != 0 {
		t.Fatalf("expected structuredProgress.SubAgents cleared, got %d", len(state.structuredProgress.SubAgents))
	}
}
