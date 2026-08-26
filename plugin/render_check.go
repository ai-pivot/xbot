package plugin

import "sync"

// RenderCheckResult is the result of a frontend render check.
type RenderCheckResult struct {
	Success bool
	Error   string
}

// renderCheckRegistry maps check_id → result channel.
// The ChannelToolBridge registers a channel when it sends code to the frontend
// for compilation validation. The frontend resolves it via the
// render_check_result RPC handler.
var renderCheckRegistry sync.Map // map[string]chan RenderCheckResult

// RegisterRenderCheck creates and registers a result channel for a check_id.
// Returns the channel — the caller blocks on it until ResolveRenderCheck or
// timeout.
func RegisterRenderCheck(checkID string) chan RenderCheckResult {
	ch := make(chan RenderCheckResult, 1)
	renderCheckRegistry.Store(checkID, ch)
	return ch
}

// ResolveRenderCheck delivers a result to the waiting ChannelToolBridge.
// Called by the render_check_result RPC handler (serverapp/rpc_table.go)
// when the frontend reports compilation success/failure.
func ResolveRenderCheck(checkID string, result RenderCheckResult) bool {
	ch, ok := renderCheckRegistry.Load(checkID)
	if !ok {
		return false
	}
	renderCheckRegistry.Delete(checkID)
	select {
	case ch.(chan RenderCheckResult) <- result:
		return true
	default:
		return false
	}
}

// RenderCheckFn is the callback signature: send code to the frontend
// and block until the frontend reports compilation result.
// Returns (success, errorMessage). On timeout, returns (true, "") —
// assume success (don't block the agent indefinitely).
type RenderCheckFn func(checkID string, code string) (bool, string)
