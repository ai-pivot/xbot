package llm

import (
	"context"
	"strings"
	"testing"
	"time"

	"xbot/internal/mockopenai"
)

func TestProcessStream_EmptyResponseIsError(t *testing.T) {
	srv := mockopenai.NewServer(t, nil)
	client := NewOpenAILLM(OpenAIConfig{
		BaseURL:      srv.URL(),
		APIKey:       "test-key",
		DefaultModel: "mock-model",
		APIType:      APITypeChatCompletions,
	})

	stream, err := client.newStreamingWithRetry(context.Background(), "mock-model", []ChatMessage{
		{Role: "user", Content: "hi"},
	}, nil, "", nil)
	if err != nil {
		t.Fatalf("newStreamingWithRetry: %v", err)
	}

	eventCh := make(chan StreamEvent, 1)
	client.processStream(context.Background(), stream, eventCh, time.Now(), nil, "mock-model", nil, "")

	var events []StreamEvent
	for event := range eventCh {
		events = append(events, event)
	}
	if len(events) != 1 || events[0].Type != EventError {
		t.Fatalf("events = %+v, want one EventError", events)
	}
	if !strings.Contains(events[0].Error, "zero chunks") {
		t.Fatalf("error = %q, want zero-chunk diagnostic", events[0].Error)
	}
}
