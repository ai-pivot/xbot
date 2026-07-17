package agent

import (
	"context"
	"encoding/json"

	log "xbot/logger"
	"xbot/protocol"
)

// ChannelTransport is the in-process direct-connect Transport for local mode.
// It directly calls RPCTable.Dispatch with no network overhead.
type ChannelTransport struct {
	dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
	ctxFn    func() context.Context  // creates per-request context (injects auth for local mode)
	eventCh  chan protocol.WSMessage // server pushes events here (nil-safe)
}

// NewChannelTransport creates a ChannelTransport from a dispatch function.
// ctxFn is called for each request to create a context. If nil, context.Background() is used.
// eventCh is the channel for server-pushed events; may be nil for callers that don't need events.
func NewChannelTransport(dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error), ctxFn func() context.Context, eventCh chan protocol.WSMessage) *ChannelTransport {
	return &ChannelTransport{dispatch: dispatch, ctxFn: ctxFn, eventCh: eventCh}
}

// EventCh returns the event channel for server-pushed events.
// Used by Client.eventLoop to read events in local mode.
func (t *ChannelTransport) EventCh() chan protocol.WSMessage {
	return t.eventCh
}

func (t *ChannelTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
	ctx := context.Background()
	if t.ctxFn != nil {
		ctx = t.ctxFn()
	}
	// Inject a per-call requestID so RPC handler logs are traceable.
	ctx = log.WithRequestID(ctx, log.NewRequestID())
	return t.dispatch(ctx, method, payload)
}

func (t *ChannelTransport) Close() error { return nil }

var _ Transport = (*ChannelTransport)(nil)
