package llm

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"
)

// Observability carries per-request tracing identifiers attached to LLM HTTP
// requests via custom headers (X-Session-Id / X-Request-Id / X-User-Id /
// X-Turn-Id / X-Trace-Id), mirroring Codex / Claude Code so provider-side
// dashboards (OpenAI usage, Anthropic console, gateway proxies) can attribute
// a call to a specific session/turn for debugging.
type Observability struct {
	SessionID string // "channel:chatID" — stable per conversation
	RequestID string // unique per LLM call (retries reuse the same id)
	UserID    string // sender / canonical user identifier
	TurnID    int64  // per-session monotonic turn number (0 = unknown)
	TraceID   string // optional distributed-trace id (e.g. OpenTelemetry)
}

type observabilityKey struct{}

// WithObservability attaches tracing identifiers to ctx for the LLM request.
func WithObservability(ctx context.Context, o Observability) context.Context {
	return context.WithValue(ctx, observabilityKey{}, o)
}

// ObservabilityFromContext returns the tracing identifiers carried by ctx.
func ObservabilityFromContext(ctx context.Context) (Observability, bool) {
	o, ok := ctx.Value(observabilityKey{}).(Observability)
	return o, ok
}

// ApplyHeaders sets the observability headers on an outgoing HTTP request.
func (o Observability) ApplyHeaders(header func(name, value string)) {
	if o.SessionID != "" {
		header("X-Session-Id", o.SessionID)
	}
	if o.RequestID != "" {
		header("X-Request-Id", o.RequestID)
	}
	if o.UserID != "" {
		header("X-User-Id", o.UserID)
	}
	if o.TurnID > 0 {
		header("X-Turn-Id", strconv.FormatInt(o.TurnID, 10))
	}
	if o.TraceID != "" {
		header("X-Trace-Id", o.TraceID)
	}
}

var reqCounter atomic.Int64

// NextRequestID generates a unique per-call request id. Retries of the same
// LLM call reuse the id (it is stamped once per generateResponse call, not per
// HTTP attempt) so a provider log shows one logical request across retries.
func (o Observability) NextRequestID() string {
	n := reqCounter.Add(1)
	return fmt.Sprintf("%s-t%d-%d", o.SessionID, o.TurnID, n)
}
