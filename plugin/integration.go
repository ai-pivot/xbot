package plugin

import (
	"fmt"

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
	adapter *PluginToolAdapter
}

// NewPluginToolBridge creates a bridge from a PluginToolAdapter.
func NewPluginToolBridge(adapter *PluginToolAdapter) *PluginToolBridge {
	return &PluginToolBridge{adapter: adapter}
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
// Converts tools.ToolContext → context.Context, executes the plugin tool,
// and converts the result back.
func (b *PluginToolBridge) Execute(ctx *tools.ToolContext, input string) (*tools.ToolResult, error) {
	// Use the cancel-aware context from ToolContext
	result, err := b.adapter.Execute(ctx.Ctx, input)
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

	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		for _, tool := range entry.Context.GetTools() {
			adapter := NewPluginToolAdapter(entry.Manifest.ID, tool)
			bridge := NewPluginToolBridge(adapter)
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
	WirePluginEnrichers(enricherRegistry, pm, 150) // priority 150 = after built-in middlewares

	return nil
}
