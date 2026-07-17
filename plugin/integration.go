package plugin

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"xbot/cron"
	"xbot/llm"
	log "xbot/logger"
	"xbot/storage/sqlite"
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
	return wirePluginToolsInternal(pm, registry, 0)
}

// WirePluginToolsForTenant scans all active plugins and registers their tools
// for a specific tenant using RegisterForTenant.
// tenantID=0 registers globally (same as WirePluginTools).
func WirePluginToolsForTenant(pm *PluginManager, registry *tools.Registry, tenantID int64) error {
	return wirePluginToolsInternal(pm, registry, tenantID)
}

// wirePluginToolsInternal is the shared implementation for both global and per-tenant wiring.
func wirePluginToolsInternal(pm *PluginManager, registry *tools.Registry, tenantID int64) error {
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

			registry.RegisterForTenant(tenantID, bridge)
			registered++
		}
	}

	if registered > 0 {
		log.Glob(log.CatPlugin).Infof("plugin: registered %d tools from %d active plugins", registered, pm.ActiveCount())
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
			bridge.Register(entry.Manifest.ID, hook.Event, hook.Matcher, hook.Handler, hook.Global)
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

// ---------------------------------------------------------------------------
// Plugin Command Wiring
// ---------------------------------------------------------------------------

// CommandRegisterFn is the callback signature for registering a plugin command.
// The caller (agent package) wraps this into a Command implementation to avoid
// circular imports between plugin ↔ agent.
type CommandRegisterFn func(name, description string, handler PluginCommandHandler, pctx PluginContext)

// WirePluginCommands iterates active plugins and calls registerFn for each
// registered command handler. The registerFn is responsible for wrapping the
// handler into an agent.Command and registering it with the command registry.
func WirePluginCommands(pm *PluginManager, registerFn CommandRegisterFn) {
	entries := pm.ListPlugins()
	registered := 0

	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		pctx := entry.Context
		for _, cmd := range pctx.GetCommands() {
			registerFn(cmd.name, cmd.description, cmd.handler, pctx)
			registered++
		}
	}

	if registered > 0 {
		log.Glob(log.CatPlugin).Infof("plugin: registered %d commands from %d active plugins", registered, pm.ActiveCount())
	}
}

// ---------------------------------------------------------------------------
// Plugin Cron Wiring
// ---------------------------------------------------------------------------

// WirePluginCrons scans active plugins for scheduled crons and adds them to
// the CronService. It also processes cancellation requests (removes jobs that
// were cancelled via CancelCron). Re-wiring is idempotent: existing plugin jobs
// are cleaned up before re-adding to avoid duplicates.
func WirePluginCrons(pm *PluginManager, cronSvc *sqlite.CronService) {
	if cronSvc == nil {
		return
	}

	added := 0
	removed := 0
	now := time.Now()

	entries := pm.ListPlugins()
	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		pluginID := entry.Manifest.ID

		// Remove stale plugin jobs (idempotent: delete before re-add)
		allJobs, err := cronSvc.ListAllJobs()
		if err != nil {
			log.Glob(log.CatPlugin).WithError(err).Warn("plugin: failed to list existing cron jobs during re-wire")
		}
		for _, job := range allJobs {
			if strings.HasPrefix(job.ID, "plugin:"+pluginID+":") {
				if err := cronSvc.RemoveJob(job.ID); err != nil {
					log.Glob(log.CatPlugin).WithError(err).WithField("job_id", job.ID).Warn("plugin: failed to remove stale cron job")
				} else {
					removed++
				}
			}
		}

		// Process cancellation requests
		for jobID := range entry.Context.GetCronCancellations() {
			if err := cronSvc.RemoveJob(jobID); err != nil {
				log.Glob(log.CatPlugin).WithError(err).WithField("job_id", jobID).Warn("plugin: failed to cancel cron job")
			} else {
				removed++
			}
		}

		// Add current cron contributions
		for i, spec := range entry.Context.GetCrons() {
			job := &sqlite.CronJob{
				ID:           fmt.Sprintf("plugin:%s:%d", pluginID, i),
				Message:      spec.Message,
				CronExpr:     spec.CronExpr,
				EverySeconds: spec.EverySeconds,
				DelaySeconds: spec.DelaySeconds,
				At:           spec.At,
				CreatedAt:    now,
			}

			// Calculate next run and one_shot flag
			job.OneShot = job.At != "" || job.DelaySeconds > 0
			nextRun, err := cron.CalculateNextRun(job, now)
			if err != nil {
				log.Glob(log.CatPlugin).WithError(err).WithField("plugin", pluginID).Warn("plugin: failed to calculate next run for cron")
				continue
			}
			job.NextRun = nextRun

			if err := cronSvc.AddJob(job); err != nil {
				log.Glob(log.CatPlugin).WithError(err).WithField("job_id", job.ID).Warn("plugin: failed to add cron job")
				continue
			}
			added++
		}
	}

	if added > 0 || removed > 0 {
		log.Glob(log.CatPlugin).Infof("plugin: cron wiring complete: %d added, %d removed", added, removed)
	}
}

// ---------------------------------------------------------------------------
// Plugin Theme Wiring
// ---------------------------------------------------------------------------

// WirePluginThemes iterates active plugins and calls themeLoader for each
// contributed theme. themeLoader receives the theme ID and raw JSON data,
// and is responsible for persisting the theme (e.g. to ~/.xbot/themes/).
func WirePluginThemes(pm *PluginManager, themeLoader func(id string, data []byte) error) {
	entries := pm.ListPlugins()
	loaded := 0

	for _, entry := range entries {
		if entry.State != StateActive {
			continue
		}
		for id, data := range entry.Context.GetThemes() {
			if err := themeLoader(id, data); err != nil {
				log.Glob(log.CatPlugin).WithError(err).WithFields(log.Fields{
					"plugin": entry.Manifest.ID,
					"theme":  id,
				}).Warn("plugin: failed to load theme")
				continue
			}
			loaded++
		}
	}

	if loaded > 0 {
		log.Glob(log.CatPlugin).Infof("plugin: loaded %d themes from active plugins", loaded)
	}
}
