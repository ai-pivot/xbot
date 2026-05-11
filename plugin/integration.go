package plugin

import (
	"context"
	"fmt"
	"sync"
	"time"

	"xbot/llm"
	log "xbot/logger"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// Integration Layer — bridges plugin types to xbot's internal subsystems.
//
// This file lives in the plugin package (not tools/) because the adapter
// needs access to PluginToolAdapter and plugin-internal types. It references
// the tools package for the Tool interface and ToolContext.
//
// Usage: Call WirePluginTools() after plugin activation to register all
// plugin-provided tools with the tools.Registry.
// ---------------------------------------------------------------------------

// ---------------------------------------------------------------------------
// PluginToolBridge — adapts PluginToolAdapter → tools.Tool
// ---------------------------------------------------------------------------

// PluginToolBridge wraps a PluginToolAdapter to implement the full tools.Tool
// interface, bridging the plugin↔host boundary for tool execution.
type PluginToolBridge struct {
	adapter         *PluginToolAdapter
	rateLimiter     *PluginRateLimiter
	quotaManager    *PluginQuotaManager
	pluginID        string
	middlewareChain *MiddlewareChain
	mu              sync.Mutex
	callTracer      *CallTracer
}

// NewPluginToolBridge creates a bridge from a PluginToolAdapter.
func NewPluginToolBridge(adapter *PluginToolAdapter) *PluginToolBridge {
	return &PluginToolBridge{adapter: adapter}
}

// NewPluginToolBridgeWithLimits creates a bridge with rate limiting and quota enforcement.
func NewPluginToolBridgeWithLimits(adapter *PluginToolAdapter, pluginID string, rl *PluginRateLimiter, qm *PluginQuotaManager) *PluginToolBridge {
	return &PluginToolBridge{
		adapter:      adapter,
		rateLimiter:  rl,
		quotaManager: qm,
		pluginID:     pluginID,
	}
}

// SetCallTracer injects a CallTracer for tool call audit trail.
// This is optional — if nil, no tracing is performed.
func (b *PluginToolBridge) SetCallTracer(ct *CallTracer) {
	b.mu.Lock()
	b.callTracer = ct
	b.mu.Unlock()
}

// Name implements llm.ToolDefinition.
func (b *PluginToolBridge) Name() string {
	return b.adapter.Name()
}

// Description implements llm.ToolDefinition.
func (b *PluginToolBridge) Description() string {
	return b.adapter.Description()
}

// Parameters implements llm.ToolDefinition.
func (b *PluginToolBridge) Parameters() []llm.ToolParam {
	return b.adapter.Parameters()
}

// Execute implements tools.Tool.
// Converts tools.ToolContext → ToolCallContext for V2, or context.Context for V1.
// Rate limit and quota checks run before the middleware chain (host-level enforcement).
func (b *PluginToolBridge) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	// Record start time for call tracing
	startTime := time.Now()

	// Capture callTracer under lock for concurrent safety
	b.mu.Lock()
	tracer := b.callTracer
	b.mu.Unlock()

	// Deferred tracer recording — runs after all return paths.
	var traceResult *ToolResult
	var traceErr error
	defer func() {
		if tracer == nil {
			return
		}
		endTime := time.Now()
		trace := CallTrace{
			PluginID:  b.pluginID,
			ToolName:  b.adapter.Name(),
			StartTime: startTime,
			EndTime:   endTime,
			Duration:  endTime.Sub(startTime),
			InputLen:  len(input),
			IsError:   traceErr != nil || (traceResult != nil && traceResult.IsError),
		}
		if traceResult != nil {
			trace.OutputLen = len(traceResult.Content)
		}
		tracer.Record(trace)
	}()

	// Rate limit check (host-level, cannot be bypassed by middleware)
	if b.rateLimiter != nil && b.pluginID != "" {
		if !b.rateLimiter.Allow(b.pluginID) {
			msg := fmt.Sprintf("rate limit exceeded for plugin %s", b.pluginID)
			traceResult = &ToolResult{Content: msg, IsError: true}
			tr := tools.NewResult(msg)
			tr.IsError = true
			return tr, nil
		}
	}

	// Quota check — tool call budget (host-level)
	if b.quotaManager != nil && b.pluginID != "" {
		if allowed, _ := b.quotaManager.CheckToolCall(b.pluginID); !allowed {
			msg := fmt.Sprintf("daily quota exceeded for plugin %s", b.pluginID)
			traceResult = &ToolResult{Content: msg, IsError: true}
			tr := tools.NewResult(msg)
			tr.IsError = true
			return tr, nil
		}
	}

	// Define the final handler that calls the actual tool
	final := func(execCtx context.Context, toolName string, toolInput string) (*ToolResult, error) {
		tcc := &ToolCallContext{
			Ctx:      execCtx,
			Channel:  ctx.Channel,
			ChatID:   ctx.ChatID,
			UserID:   ctx.SenderID,
			TenantID: ctx.TenantID,
		}
		return b.adapter.ExecuteWithContext(tcc, toolInput)
	}

	// Execute through middleware chain if present, otherwise call directly
	var result *ToolResult
	var err error
	if b.middlewareChain != nil && b.middlewareChain.Len() > 0 {
		result, err = b.middlewareChain.Execute(ctx.Ctx, b.adapter.Name(), input, final)
	} else {
		result, err = final(ctx.Ctx, b.adapter.Name(), input)
	}

	traceResult = result
	traceErr = err

	if err != nil {
		return nil, err
	}

	// Convert plugin.ToolResult → tools.ToolResult
	tr := tools.NewResult(result.Content)
	if result.IsError {
		tr.IsError = true
	}
	if result.Metadata != nil {
		tr.Metadata = result.Metadata
	}
	return tr, nil
}

// Compile-time check.
var _ tools.Tool = (*PluginToolBridge)(nil)

// ---------------------------------------------------------------------------
// WirePluginTools — registers all active plugin tools with tools.Registry
// ---------------------------------------------------------------------------

// WirePluginTools scans all active plugins and registers their tools
// with the provided tools.Registry.
//
// Call this after PluginManager.ActivateAll() or ActivateForEvent().
func WirePluginTools(pm *PluginManager, registry *tools.Registry) error {
	if registry == nil {
		return fmt.Errorf("plugin: tools.Registry is nil")
	}

	entries := pm.ListPlugins()
	registered := 0

	rl := pm.RateLimiter()
	qm := pm.QuotaManager()

	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		for _, tool := range entry.Context.GetTools() {
			adapter := NewPluginToolAdapterWithContext(entry.Manifest.ID, tool, entry.Context)
			bridge := NewPluginToolBridgeWithLimits(adapter, entry.Manifest.ID, rl, qm)

			// Inject plugin middleware chain if any middleware was registered
			if middlewares := entry.Context.GetMiddlewares(); len(middlewares) > 0 {
				chain := NewMiddlewareChain(middlewares...)
				bridge.middlewareChain = chain
			}

			registry.Register(bridge)
			registered++
		}
	}

	if registered > 0 {
		log.Infof("plugin: registered %d tools from %d active plugins", registered, pm.ActiveCount())
	}
	return nil
}

// ---------------------------------------------------------------------------
// PluginHookIntegration — bridges plugin hooks to PluginHookBridge
// ---------------------------------------------------------------------------

// WirePluginHooks registers all active plugin hook handlers with the
// PluginHookBridge. The bridge should be registered as a builtin callback
// with xbot's hooks.Manager by the caller (see agent.New() wiring).
func WirePluginHooks(bridge *PluginHookBridge, pm *PluginManager) {
	entries := pm.ListPlugins()
	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		bridge.SetContext(entry.Manifest.ID, entry.Context)
		for _, hook := range entry.Context.GetHooks() {
			bridge.Register(entry.Manifest.ID, hook.Event, hook.Matcher, hook.Handler)
		}
	}
}

// ---------------------------------------------------------------------------
// PluginMiddlewareIntegration — bridges enrichers to MessagePipeline
// ---------------------------------------------------------------------------

// WirePluginEnrichers sets up context enrichers from all active plugins
// into the enricher registry. The registry should be used by a
// PluginMiddleware registered in the MessagePipeline.
func WirePluginEnrichers(registry *EnricherRegistry, pm *PluginManager, priority int) {
	entries := pm.ListPlugins()
	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		for _, enricher := range entry.Context.GetEnrichers() {
			registry.Register(entry.Manifest.ID, enricher.Name, enricher.Enricher, priority)
		}
	}
}

// ---------------------------------------------------------------------------
// Full Wiring — one-call setup for all integration points
// ---------------------------------------------------------------------------

// defaultEnricherPriority is the priority for plugin context enrichers.
// Set after built-in middlewares to allow them to run first.
const defaultEnricherPriority = 150

// WireAll performs complete integration of the plugin system with xbot's
// subsystems. Call this after ActivateAll().
//
// Parameters:
//   - pm: The PluginManager with activated plugins
//   - registry: The tools.Registry for tool registration
//   - bridge: The PluginHookBridge for hook routing
//   - enricherRegistry: The EnricherRegistry for context enrichment
func WireAll(pm *PluginManager, registry *tools.Registry, bridge *PluginHookBridge, enricherRegistry *EnricherRegistry) error {
	if err := WirePluginTools(pm, registry); err != nil {
		return fmt.Errorf("wire tools: %w", err)
	}
	WirePluginHooks(bridge, pm)
	WirePluginEnrichers(enricherRegistry, pm, defaultEnricherPriority)

	return nil
}
