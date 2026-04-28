package llm

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/packages/ssestream"
)

func newTestSSEDecoder(t *testing.T, contentType, input string) ssestream.Decoder {
	t.Helper()

	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{contentType},
		},
		Body: io.NopCloser(strings.NewReader(input)),
	}
	dec := ssestream.NewDecoder(resp)
	if dec == nil {
		t.Fatal("expected decoder, got nil")
	}
	if _, ok := dec.(*sseEventFilterDecoder); !ok {
		t.Fatalf("expected *sseEventFilterDecoder, got %T", dec)
	}
	return dec
}

func TestSSEEventFilterDecoder_SkipsCommentOnlyEvents(t *testing.T) {
	dec := newTestSSEDecoder(t, "text/event-stream",
		": PROCESSING\n\n: STILL PROCESSING\n\ndata: {\"id\":\"chatcmpl-1\"}\n\n")

	if !dec.Next() {
		t.Fatal("expected first data event")
	}
	if got := string(dec.Event().Data); got != "{\"id\":\"chatcmpl-1\"}\n" {
		t.Fatalf("unexpected event data: %q", got)
	}
	if dec.Next() {
		t.Fatal("expected comment-only events to be skipped entirely")
	}
}

func TestSSEEventFilterDecoder_SkipsEmptyDataEvents(t *testing.T) {
	dec := newTestSSEDecoder(t, "text/event-stream",
		"data:\n\ndata: \n\ndata: hello\n\n")

	if !dec.Next() {
		t.Fatal("expected non-empty data event")
	}
	if got := string(dec.Event().Data); got != "hello\n" {
		t.Fatalf("unexpected event data: %q", got)
	}
	if dec.Next() {
		t.Fatal("expected empty-data events to be skipped entirely")
	}
}

func TestSSEEventFilterDecoder_PreservesEventTypeAndDone(t *testing.T) {
	dec := newTestSSEDecoder(t, "text/event-stream",
		"event: thread.message.delta\ndata: {\"id\":\"1\"}\n\ndata: [DONE]\n\n")

	if !dec.Next() {
		t.Fatal("expected thread event")
	}
	if got := dec.Event().Type; got != "thread.message.delta" {
		t.Fatalf("unexpected event type: %q", got)
	}
	if got := string(dec.Event().Data); got != "{\"id\":\"1\"}\n" {
		t.Fatalf("unexpected thread event data: %q", got)
	}

	if !dec.Next() {
		t.Fatal("expected done event")
	}
	if got := string(dec.Event().Data); got != "[DONE]\n" {
		t.Fatalf("unexpected done event data: %q", got)
	}
}

func TestSSEEventFilterDecoder_RegisteredForCharsetVariant(t *testing.T) {
	dec := newTestSSEDecoder(t, "text/event-stream; charset=utf-8", "data: hello\n\n")

	if !dec.Next() {
		t.Fatal("expected event for charset variant")
	}
	if got := string(dec.Event().Data); got != "hello\n" {
		t.Fatalf("unexpected event data: %q", got)
	}
}
