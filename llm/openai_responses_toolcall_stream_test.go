package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// Captured from mint alpha / gpt-6.1-sol via /v1/responses on 2026-11-13.
// Four calls, output_index 1..4 (reasoning occupied 0), NO arguments.delta:
// added(empty args) -> arguments.done(full args) -> output_item.done(full args).
// Only synthetic weather requests are retained; IDs are anonymized.
func TestResponsesStream_MultiToolDoneOnlyCapture(t *testing.T) {
	raw, err := os.ReadFile("testdata/responses_multitool_done_only.sse")
	if err != nil {
		t.Fatal(err)
	}
	var events []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "data: ") {
			events = append(events, strings.TrimPrefix(line, "data: "))
		}
	}
	srv := newResponsesSSEServer(t, events...)
	defer srv.Close()
	resp := collectResponsesStream(t, srv.URL)
	if len(resp.ToolCalls) != 4 {
		t.Fatalf("got %d calls, want 4", len(resp.ToolCalls))
	}
	seen := map[string]bool{}
	for i, city := range []string{"Beijing", "Shanghai", "Tokyo", "London"} {
		call := resp.ToolCalls[i]
		if call.Name != "get_weather" || call.ID == "" || seen[call.ID] {
			t.Errorf("bad call identity: %+v", call)
		}
		seen[call.ID] = true
		var args struct {
			City string `json:"city"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
			t.Errorf("call %d parse args: %v (raw=%q)", i, err, call.Arguments)
			continue
		}
		if args.City != city {
			t.Errorf("call %d args=%+v, want city %s", i, args, city)
		}
	}
	if resp.FinishReason != FinishReasonToolCalls {
		t.Errorf("finish reason=%s, want tool_calls", resp.FinishReason)
	}
}

func TestResponsesStream_ToolArgumentsFinalization(t *testing.T) {
	const final = `{"value":"complete"}`
	added := `{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_a","call_id":"call_a","type":"function_call","name":"alpha","arguments":""}}`
	delta := func(args string) string {
		b, _ := json.Marshal(args)
		return fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"fc_a","output_index":1,"delta":%s}`, b)
	}
	done := func(args string) string {
		b, _ := json.Marshal(args)
		return fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":"fc_a","output_index":1,"arguments":%s}`, b)
	}
	item := `{"id":"fc_a","call_id":"call_a","type":"function_call","name":"alpha","arguments":"{\"value\":\"complete\"}"}`
	itemDone := `{"type":"response.output_item.done","output_index":1,"item":` + item + `}`
	completed := `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[` + item + `]}}`
	for _, tc := range []struct {
		name   string
		events []string
	}{
		{"done_only", []string{added, done(final), itemDone, completed}},
		{"normal_deltas_no_duplicate", []string{added, delta(`{"value":`), delta(`"complete"}`), done(final), itemDone, completed}},
		{"partial_delta_completed_by_done", []string{added, delta(`{"value":"comp`), done(final), itemDone, completed}},
		{"authoritative_done_corrects_delta", []string{added, delta(`{"value":"wrong"}`), done(final), itemDone, completed}},
		{"output_item_done_only", []string{added, itemDone, completed}},
		{"output_item_done_corrects_partial", []string{added, delta(`{"value":"comp`), itemDone, completed}},
		{"arguments_on_added", []string{`{"type":"response.output_item.added","output_index":1,"item":` + item + `}`, responsesCompletedEvent}},
		{"completed_only", []string{completed}},
		{"completed_corrects_partial", []string{added, delta(`{"value":"comp`), completed}},
		{"output_item_without_added", []string{itemDone, completed}},
		{"duplicate_added_and_done", []string{added, added, done(final), done(final), itemDone, completed}},
		{"done_missing_call_id_preserves_identity", []string{added, strings.ReplaceAll(itemDone, `"call_id":"call_a",`, ""), strings.ReplaceAll(completed, `"call_id":"call_a",`, "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newResponsesSSEServer(t, tc.events...)
			defer srv.Close()
			resp := collectResponsesStream(t, srv.URL)
			if len(resp.ToolCalls) != 1 {
				t.Fatalf("got %d calls, want 1", len(resp.ToolCalls))
			}
			call := resp.ToolCalls[0]
			if call.ID != "call_a" || call.Name != "alpha" || call.Arguments != final {
				t.Errorf("call=%+v, want call_a/alpha/%s", call, final)
			}
		})
	}
}

// Calls may be interleaved, have sparse output indices, and mix ordinary deltas
// with done-only snapshots. IDs/names/arguments must stay per-call throughout.
func TestResponsesStream_InterleavedToolArguments(t *testing.T) {
	srv := newResponsesSSEServer(t,
		`{"type":"response.output_item.added","output_index":1,"item":{"id":"fc_a","call_id":"call_a","type":"function_call","name":"alpha","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_a","output_index":1,"delta":"{\"a\":"}`,
		`{"type":"response.output_item.added","output_index":3,"item":{"id":"fc_b","call_id":"call_b","type":"function_call","name":"beta","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_b","output_index":3,"arguments":"{\"b\":2}"}`,
		`{"type":"response.function_call_arguments.delta","item_id":"fc_a","output_index":1,"delta":"1}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"fc_a","output_index":1,"arguments":"{\"a\":1}"}`,
		responsesCompletedEvent,
	)
	defer srv.Close()
	resp := collectResponsesStream(t, srv.URL)
	if len(resp.ToolCalls) != 2 {
		t.Fatalf("calls=%+v, want 2", resp.ToolCalls)
	}
	for i, want := range []ToolCall{{ID: "call_a", Name: "alpha", Arguments: `{"a":1}`}, {ID: "call_b", Name: "beta", Arguments: `{"b":2}`}} {
		if resp.ToolCalls[i] != want {
			t.Errorf("call[%d]=%+v, want %+v", i, resp.ToolCalls[i], want)
		}
	}
}
