package hooks

import "context"

// SessionContext carries session-level metadata that should be available
// to all hook events (model name, token usage, context window limits).
// It is injected into the context by the engine and extracted by
// PluginBridgeCallback to populate HookPayload.Extra for plugins.
type SessionContext struct {
	Model        string // Current LLM model name (e.g. "claude-sonnet-4-20250514")
	MaxContext   int64  // Maximum context window in tokens
	PromptTokens int64  // Cumulative prompt tokens used in this Run()
	CompTokens   int64  // Cumulative completion tokens used in this Run()
}

type sessionCtxKey struct{}

// WithSessionContext injects SessionContext into the context.
func WithSessionContext(ctx context.Context, sc *SessionContext) context.Context {
	return context.WithValue(ctx, sessionCtxKey{}, sc)
}

// SessionContextFromContext extracts SessionContext from the context.
// Returns nil if not present.
func SessionContextFromContext(ctx context.Context) *SessionContext {
	sc, _ := ctx.Value(sessionCtxKey{}).(*SessionContext)
	return sc
}
