package plugin

import (
	"context"
	"strings"
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Hook Adapter — bridges plugin HookHandlers to the hooks system
// ---------------------------------------------------------------------------

// PluginHookBridge manages hook subscriptions from all plugins.
// It is registered with the hooks.Manager as a builtin callback.
type PluginHookBridge struct {
	mu       sync.RWMutex
	handlers map[string][]pluginHookEntry  // event → entries
	contexts map[string]*pluginContextImpl // pluginID → context (for tracking)
}

type pluginHookEntry struct {
	pluginID string
	matcher  string
	handler  HookHandler
}

// NewPluginHookBridge creates a new hook bridge.
func NewPluginHookBridge() *PluginHookBridge {
	return &PluginHookBridge{
		handlers: make(map[string][]pluginHookEntry),
		contexts: make(map[string]*pluginContextImpl),
	}
}

// Register adds a plugin hook subscription.
func (b *PluginHookBridge) Register(pluginID string, event HookEvent, matcher string, handler HookHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := string(event)
	b.handlers[key] = append(b.handlers[key], pluginHookEntry{
		pluginID: pluginID,
		matcher:  matcher,
		handler:  handler,
	})
	log.WithField("plugin", pluginID).WithField("event", string(event)).
		Debug("Plugin hook registered")
}

// SetContext registers a plugin context for resource tracking.
func (b *PluginHookBridge) SetContext(pluginID string, ctx *pluginContextImpl) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.contexts[pluginID] = ctx
}

// Dispatch sends an event to all matching plugin hooks.
// Returns an aggregated HookDecision.
func (b *PluginHookBridge) Dispatch(ctx context.Context, payload *HookPayload) *HookResult {
	key := string(payload.Event)

	b.mu.RLock()
	entries := make([]pluginHookEntry, len(b.handlers[key]))
	copy(entries, b.handlers[key])
	// Snapshot contexts for tracking (under same RLock)
	contexts := make(map[string]*pluginContextImpl, len(b.contexts))
	for k, v := range b.contexts {
		contexts[k] = v
	}
	b.mu.RUnlock()

	if len(entries) == 0 {
		return &HookResult{Decision: DecisionDefer}
	}

	finalDecision := DecisionAllow
	var denyMessage string

	for _, entry := range entries {
		// Check matcher
		if entry.matcher != "" && payload.ToolName != "" {
			if !matchToolName(entry.matcher, payload.ToolName) {
				continue
			}
		}

		// Track hook call for resource monitoring
		if bCtx, ok := contexts[entry.pluginID]; ok {
			bCtx.incrementHookCallCount()
		}

		result, err := entry.handler(ctx, payload)
		if err != nil {
			log.WithField("plugin", entry.pluginID).WithField("event", string(payload.Event)).
				Warn("Plugin hook handler error: ", err)
			continue
		}

		// Aggregate decision: deny > defer > ask > allow
		if result != nil {
			decision := result.Decision
			if decisionWeight(decision) > decisionWeight(finalDecision) {
				finalDecision = decision
				if decision == DecisionDeny && result.Message != "" {
					denyMessage = result.Message
				}
			}
		}
	}

	return &HookResult{
		Decision: finalDecision,
		Message:  denyMessage,
	}
}

// matchToolName does simple glob-style matching.
func matchToolName(pattern, name string) bool {
	if pattern == "*" || pattern == "" {
		return true
	}
	if pattern == name {
		return true
	}
	// Simple prefix/suffix matching with *
	if len(pattern) > 1 && pattern[0] == '*' && pattern[len(pattern)-1] == '*' {
		return strings.Contains(name, pattern[1:len(pattern)-1])
	}
	if len(pattern) > 1 && pattern[0] == '*' {
		return strings.HasSuffix(name, pattern[1:])
	}
	if len(pattern) > 1 && pattern[len(pattern)-1] == '*' {
		return strings.HasPrefix(name, pattern[:len(pattern)-1])
	}
	return pattern == name
}

// decisionWeight returns priority for decision aggregation.
func decisionWeight(d HookDecision) int {
	switch d {
	case DecisionDeny:
		return 4
	case DecisionAsk:
		return 3
	case DecisionDefer:
		return 2
	case DecisionAllow:
		return 1
	default:
		return 0
	}
}
