package logger

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

type ctxKey string

const (
	requestIDKey ctxKey = "request_id"
	sessionIDKey ctxKey = "session_id"
	turnIDKey    ctxKey = "turn_id"
	userIDKey    ctxKey = "user_id"
)

// WithRequestID injects a request ID into the context.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestID extracts the request ID from the context. Returns "" if not set.
func RequestID(ctx context.Context) string {
	if id, ok := ctx.Value(requestIDKey).(string); ok {
		return id
	}
	return ""
}

// WithSessionID injects the session identifier ("channel:chatID") into ctx.
func WithSessionID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionIDKey, id)
}

// SessionID extracts the session identifier from ctx. Returns "" if not set.
func SessionID(ctx context.Context) string {
	if id, ok := ctx.Value(sessionIDKey).(string); ok {
		return id
	}
	return ""
}

// WithTurnID injects the per-session turn number into ctx.
func WithTurnID(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, turnIDKey, id)
}

// TurnID extracts the per-session turn number from ctx. Returns 0 if not set.
func TurnID(ctx context.Context) int64 {
	if id, ok := ctx.Value(turnIDKey).(int64); ok {
		return id
	}
	return 0
}

// WithUserID injects the user identifier into ctx.
func WithUserID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, userIDKey, id)
}

// UserID extracts the user identifier from ctx. Returns "" if not set.
func UserID(ctx context.Context) string {
	if id, ok := ctx.Value(userIDKey).(string); ok {
		return id
	}
	return ""
}

// NewRequestID generates a request ID (UUID without dashes).
func NewRequestID() string {
	return strings.ReplaceAll(uuid.New().String(), "-", "")
}

// Ctx returns a logrus Entry with request_id / session_id / turn_id / user_id
// fields from context (if present). Use this as the starting point for
// structured logging within a request scope — the agent loop injects these via
// llm.WithObservability / logger.WithRequestID, so LLM-call and agent-loop logs
// are greppable by session/request/turn.
func Ctx(ctx context.Context) *Entry {
	fields := Fields{}
	if id := RequestID(ctx); id != "" {
		fields["request_id"] = id
	}
	if id := SessionID(ctx); id != "" {
		fields["session_id"] = id
	}
	if id := UserID(ctx); id != "" {
		fields["user_id"] = id
	}
	if id := TurnID(ctx); id > 0 {
		fields["turn_id"] = id
	}
	if len(fields) > 0 {
		return WithFields(fields)
	}
	return WithFields(Fields{})
}
