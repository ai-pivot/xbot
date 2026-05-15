package agent

import (
	"context"
	"encoding/json"
)

// ChannelTransport is the in-process direct-connect Transport for local mode.
// It directly calls RPCTable.Dispatch with no network overhead.
type ChannelTransport struct {
	dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
}

func NewChannelTransport(dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)) *ChannelTransport {
	return &ChannelTransport{dispatch: dispatch}
}

func (t *ChannelTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	ctx := context.Background()
	return t.dispatch(ctx, method, payload)
}

func (t *ChannelTransport) Close() error { return nil }

var _ Transport = (*ChannelTransport)(nil)
