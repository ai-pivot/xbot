package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
}

// WithRPCCtx injects per-request identity into context.
func WithRPCCtx(ctx context.Context, authSenderID, bizID string) context.Context {
	return context.WithValue(ctx, rpcCtxKey, &RPCCtxData{AuthSenderID: authSenderID, BizID: bizID})
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
