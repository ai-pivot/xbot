package serverapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// RPCHandler is a function that handles a single RPC method.
type RPCHandler func(ctx context.Context, params json.RawMessage) (json.RawMessage, error)

// RPCTable maps method names to their handler functions.
// Built once at server startup, reused for every incoming RPC request.
type RPCTable map[string]RPCHandler

// --- Per-request context values ---

type rpcCtxKeyType struct{}

var rpcCtxKey = rpcCtxKeyType{}

// RPCCtxData holds per-request identity fields, stored in context.
type RPCCtxData struct {
	AuthSenderID string
	BizID        string
	UserID       int64  // canonical user ID (from IdentityResolver)
	Role         string // user role ("admin" | "user")
}

// WithRPCCtx injects per-request identity into context.
func WithRPCCtx(ctx context.Context, authSenderID, bizID string) context.Context {
	return context.WithValue(ctx, rpcCtxKey, &RPCCtxData{AuthSenderID: authSenderID, BizID: bizID, Role: "admin"})
}

// WithRPCCtxResolved injects per-request identity with canonical user_id + role.
func WithRPCCtxResolved(ctx context.Context, authSenderID, bizID string, userID int64, role string) context.Context {
	return context.WithValue(ctx, rpcCtxKey, &RPCCtxData{AuthSenderID: authSenderID, BizID: bizID, UserID: userID, Role: role})
}

func rpcAuthID(ctx context.Context) string {
	if v, ok := ctx.Value(rpcCtxKey).(*RPCCtxData); ok {
		return v.AuthSenderID
	}
	return ""
}

func rpcBizID(ctx context.Context) string {
	if v, ok := ctx.Value(rpcCtxKey).(*RPCCtxData); ok {
		return v.BizID
	}
	return ""
}

// rpcUserID returns the canonical user ID from context (0 if unresolved).
func rpcUserID(ctx context.Context) int64 {
	if v, ok := ctx.Value(rpcCtxKey).(*RPCCtxData); ok {
		return v.UserID
	}
	return 0
}

func rpcRole(ctx context.Context) string {
	if v, ok := ctx.Value(rpcCtxKey).(*RPCCtxData); ok {
		return v.Role
	}
	return ""
}

// --- Generic adapters that eliminate JSON boilerplate ---

func rpc0[R any](fn func(ctx context.Context) R) RPCHandler {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return json.Marshal(fn(ctx))
	}
}

func rpc0err[R any](fn func(ctx context.Context) (R, error)) RPCHandler {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		result, err := fn(ctx)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

func rpc1[P any, R any](fn func(ctx context.Context, p P) (R, error)) RPCHandler {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		result, err := fn(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

func rpc1strict[P any, R any](fn func(ctx context.Context, p P) (R, error)) RPCHandler {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p P
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&p); err != nil {
			return nil, err
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("multiple JSON values")
			}
			return nil, err
		}
		result, err := fn(ctx, p)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
}

func rpc1void[P any](fn func(ctx context.Context, p P) error) RPCHandler {
	return func(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
		var p P
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return nil, fn(ctx, p)
	}
}

func rpc0void(fn func(ctx context.Context) error) RPCHandler {
	return func(ctx context.Context, _ json.RawMessage) (json.RawMessage, error) {
		return nil, fn(ctx)
	}
}

// Dispatch routes a request to the matching handler.
func (t RPCTable) Dispatch(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	h, ok := t[method]
	if !ok {
		return nil, fmt.Errorf("unknown RPC method: %s", method)
	}
	return h(ctx, params)
}

var errSettingsUnavailable = errors.New("settings service not available")
