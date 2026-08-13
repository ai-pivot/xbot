package llm

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"xbot/logger"
)

func TestObservabilityContext(t *testing.T) {
	ctx := context.Background()
	if _, ok := ObservabilityFromContext(ctx); ok {
		t.Fatal("empty context should not carry observability")
	}

	o := Observability{SessionID: "web:chat-1", UserID: "admin", TurnID: 42}
	ctx = WithObservability(ctx, o)

	got, ok := ObservabilityFromContext(ctx)
	if !ok {
		t.Fatal("expected observability in context")
	}
	if got.SessionID != "web:chat-1" || got.UserID != "admin" || got.TurnID != 42 {
		t.Fatalf("roundtrip mismatch: %+v", got)
	}

	// WithObservability must ALSO mirror ids into the logger context so
	// log.Ctx(ctx) lines carry session_id/turn_id/user_id.
	if logger.SessionID(ctx) != "web:chat-1" {
		t.Errorf("logger.SessionID = %q, want web:chat-1", logger.SessionID(ctx))
	}
	if logger.TurnID(ctx) != 42 {
		t.Errorf("logger.TurnID = %d, want 42", logger.TurnID(ctx))
	}
	if logger.UserID(ctx) != "admin" {
		t.Errorf("logger.UserID = %q, want admin", logger.UserID(ctx))
	}
}

func TestObservabilityWithRequestIDMirrorsLogger(t *testing.T) {
	// RequestID stamped per call (generateResponse) must reach logger too.
	o := Observability{SessionID: "web:chat-1", RequestID: "rq-42", TurnID: 7}
	ctx := WithObservability(context.Background(), o)
	if logger.RequestID(ctx) != "rq-42" {
		t.Errorf("logger.RequestID = %q, want rq-42", logger.RequestID(ctx))
	}
}

func TestObservabilityApplyHeaders(t *testing.T) {
	o := Observability{SessionID: "cli:/tmp/repo", UserID: "admin", TurnID: 7, RequestID: "rq-1", TraceID: "trace-abc"}
	hdr := map[string]string{}
	o.ApplyHeaders(func(name, value string) { hdr[name] = value })

	want := map[string]string{
		"X-Session-Id": "cli:/tmp/repo",
		"X-User-Id":    "admin",
		"X-Turn-Id":    "7",
		"X-Request-Id": "rq-1",
		"X-Trace-Id":   "trace-abc",
	}
	for k, v := range want {
		if hdr[k] != v {
			t.Errorf("header %s = %q, want %q", k, hdr[k], v)
		}
	}
}

func TestObservabilityApplyHeadersSkipsEmpty(t *testing.T) {
	o := Observability{} // all zero
	hdr := map[string]string{}
	o.ApplyHeaders(func(name, value string) { hdr[name] = value })
	if len(hdr) != 0 {
		t.Fatalf("empty observability should set no headers, got %v", hdr)
	}
}

func TestObservabilityNextRequestID(t *testing.T) {
	o := Observability{SessionID: "web:chat-1", TurnID: 9}
	a := o.NextRequestID()
	b := o.NextRequestID()
	if a == b {
		t.Fatalf("request ids must be unique, got %q twice", a)
	}
	if a == "" || b == "" {
		t.Fatal("request id must not be empty")
	}
}

// roundTripFunc adapts a function to http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestTransportAttachesObservabilityHeaders verifies the end-to-end wiring:
// ctx (injected by Run → generateResponse) is read by the OpenAI transport and
// turned into X-Session-Id / X-Request-Id / X-User-Id / X-Turn-Id headers on
// the outgoing LLM HTTP request — mirroring Codex / Claude Code for
// provider-side tracing.
func TestTransportAttachesObservabilityHeaders(t *testing.T) {
	var got map[string]string
	transport := &streamCaptureTransport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			got = map[string]string{}
			for _, name := range []string{"X-Session-Id", "X-Request-Id", "X-User-Id", "X-Turn-Id", "X-Trace-Id"} {
				if v := req.Header.Get(name); v != "" {
					got[name] = v
				}
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"object":"list"}`)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
		}),
	}

	ctx := WithObservability(context.Background(), Observability{
		SessionID: "cli:/tmp/repo",
		RequestID: "rq-42",
		UserID:    "admin",
		TurnID:    7,
		TraceID:   "trace-abc",
	})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/v1/chat/completions", nil)
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}

	want := map[string]string{
		"X-Session-Id": "cli:/tmp/repo",
		"X-Request-Id": "rq-42",
		"X-User-Id":    "admin",
		"X-Turn-Id":    "7",
		"X-Trace-Id":   "trace-abc",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("header %s = %q, want %q", k, got[k], v)
		}
	}
}

// TestTransportSkipsHeadersWithoutContext: no observability in ctx → no custom
// headers attached (must not perturb normal requests).
func TestTransportSkipsHeadersWithoutContext(t *testing.T) {
	transport := &streamCaptureTransport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			for _, name := range []string{"X-Session-Id", "X-Request-Id", "X-User-Id", "X-Turn-Id"} {
				if v := req.Header.Get(name); v != "" {
					t.Errorf("unexpected header %s = %q without observability ctx", name, v)
				}
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: http.Header{"Content-Type": {"application/json"}}}, nil
		}),
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://localhost/v1/chat/completions", nil)
	if _, err := transport.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
}
