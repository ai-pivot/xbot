package protocol

import (
	"encoding/json"
	"testing"
)

func TestEventType(t *testing.T) {
	tests := []struct {
		name     string
		event    TransportEvent
		expected string
	}{
		{"ProgressEvent", ProgressEvent{}, "progress"},
		{"OutboundEvent", OutboundEvent{}, "outbound"},
		{"InjectUserEvent", InjectUserEvent{}, "inject_user"},
		{"ConnStateEvent", ConnStateEvent{}, "conn_state"},
		{"ReconnectEvent", ReconnectEvent{}, "reconnect"},
		{"PluginWidgetEvent", PluginWidgetEvent{}, "plugin_widget"},
		{"TUIControlEvent", TUIControlEvent{}, "tui_control"},
		{"AskUserEvent", AskUserEvent{}, "ask_user"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.EventType(); got != tt.expected {
				t.Errorf("EventType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestEventVersion(t *testing.T) {
	tests := []struct {
		name  string
		event TransportEvent
	}{
		{"ProgressEvent", ProgressEvent{}},
		{"OutboundEvent", OutboundEvent{}},
		{"InjectUserEvent", InjectUserEvent{}},
		{"ConnStateEvent", ConnStateEvent{}},
		{"ReconnectEvent", ReconnectEvent{}},
		{"PluginWidgetEvent", PluginWidgetEvent{}},
		{"TUIControlEvent", TUIControlEvent{}},
		{"AskUserEvent", AskUserEvent{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.EventVersion(); got != 1 {
				t.Errorf("EventVersion() = %d, want 1", got)
			}
		})
	}
}

func TestEventEnvelope(t *testing.T) {
	t.Run("round-trip ProgressEvent", func(t *testing.T) {
		original := ProgressEvent{
			Iteration:   1,
			Content:     "test content",
			ElapsedWall: 123,
			Phase:       "thinking",
		}

		envelope := EventEnvelope{
			Type:    original.EventType(),
			Version: original.EventVersion(),
			Payload: mustMarshal(t, original),
		}

		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("Marshal envelope: %v", err)
		}

		var decoded EventEnvelope
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal envelope: %v", err)
		}

		if decoded.Type != "progress" {
			t.Errorf("Type = %q, want %q", decoded.Type, "progress")
		}
		if decoded.Version != 1 {
			t.Errorf("Version = %d, want 1", decoded.Version)
		}

		var payload ProgressEvent
		if err := json.Unmarshal(decoded.Payload, &payload); err != nil {
			t.Fatalf("Unmarshal payload: %v", err)
		}
		if payload.Iteration != 1 || payload.Content != "test content" || payload.ElapsedWall != 123 {
			t.Errorf("payload mismatch: %+v", payload)
		}
	})

	t.Run("round-trip OutboundEvent", func(t *testing.T) {
		original := OutboundEvent{
			Channel:   "cli",
			ChatID:    "chat-1",
			Content:   "hello world",
			IsPartial: true,
		}

		envelope := EventEnvelope{
			Type:    original.EventType(),
			Version: original.EventVersion(),
			Payload: mustMarshal(t, original),
		}

		data, _ := json.Marshal(envelope)
		var decoded EventEnvelope
		json.Unmarshal(data, &decoded)

		if decoded.Type != "outbound" {
			t.Errorf("Type = %q, want %q", decoded.Type, "outbound")
		}

		var payload OutboundEvent
		json.Unmarshal(decoded.Payload, &payload)
		if payload.Channel != "cli" || payload.ChatID != "chat-1" || payload.Content != "hello world" || !payload.IsPartial {
			t.Errorf("payload mismatch: %+v", payload)
		}
	})

	t.Run("unknown event type preserves fields", func(t *testing.T) {
		envelope := EventEnvelope{
			Type:    "custom_event",
			Version: 2,
			Payload: json.RawMessage(`{"key":"value"}`),
		}

		data, _ := json.Marshal(envelope)
		var decoded EventEnvelope
		json.Unmarshal(data, &decoded)

		if decoded.Type != "custom_event" {
			t.Errorf("Type = %q, want %q", decoded.Type, "custom_event")
		}
		if decoded.Version != 2 {
			t.Errorf("Version = %d, want 2", decoded.Version)
		}
		if string(decoded.Payload) != `{"key":"value"}` {
			t.Errorf("Payload = %s, want %s", string(decoded.Payload), `{"key":"value"}`)
		}
	})

	t.Run("nil payload", func(t *testing.T) {
		envelope := EventEnvelope{
			Type:    "progress",
			Version: 1,
			Payload: nil,
		}

		data, _ := json.Marshal(envelope)
		var decoded EventEnvelope
		json.Unmarshal(data, &decoded)

		if decoded.Type != "progress" {
			t.Errorf("Type = %q, want %q", decoded.Type, "progress")
		}
	})

	t.Run("Payload field is json.RawMessage", func(t *testing.T) {
		// Verify that Payload preserves raw JSON without double-encoding
		raw := `{"iteration":1}`
		envelope := EventEnvelope{
			Type:    "progress",
			Version: 1,
			Payload: json.RawMessage(raw),
		}

		data, _ := json.Marshal(envelope)
		var decoded EventEnvelope
		json.Unmarshal(data, &decoded)

		// Should be {"type":"progress","version":1,"payload":{"iteration":1}}
		// NOT {"type":"progress","version":1,"payload":"{\"iteration\":1}"}
		var rawMap map[string]json.RawMessage
		json.Unmarshal(data, &rawMap)
		payloadField := string(rawMap["payload"])
		if payloadField != `{"iteration":1}` {
			t.Errorf("payload should not be double-encoded: %s", payloadField)
		}
	})
}

func mustMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
