package agent

import (
	"context"
	"encoding/json"
)

// ChannelTransport is the in-process direct-connect Transport for local mode.
// It directly calls RPCTable.Dispatch with no network overhead.
type ChannelTransport struct {
	dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
	ctxFn    func() context.Context // creates per-request context (injects auth for local mode)
}

// NewChannelTransport creates a ChannelTransport from a dispatch function.
// ctxFn is called for each request to create a context. If nil, context.Background() is used.
func NewChannelTransport(dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error), ctxFn func() context.Context) *ChannelTransport {
	return &ChannelTransport{dispatch: dispatch, ctxFn: ctxFn}
}

func (t *ChannelTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	ctx := context.Background()
	if t.ctxFn != nil {
		ctx = t.ctxFn()
	}
	return t.dispatch(ctx, method, payload)
}

func (t *ChannelTransport) Close() error { return nil }

var _ Transport = (*ChannelTransport)(nil)
