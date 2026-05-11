package agent

import (
	"xbot/internal/ctxkeys"
	"xbot/plugin"
)

// ---------------------------------------------------------------------------
// PluginEnricherMiddleware — injects plugin context enrichers into system prompt
// ---------------------------------------------------------------------------

// pluginEnricherMiddleware is a MessageMiddleware that runs all registered
// plugin context enrichers and injects their output into the system prompt
// via SystemParts["plugin_enrichers"].
type pluginEnricherMiddleware struct {
	registry *plugin.EnricherRegistry
}

// newPluginEnricherMiddleware creates a new middleware from the enricher registry.
func newPluginEnricherMiddleware(registry *plugin.EnricherRegistry) *pluginEnricherMiddleware {
	return &pluginEnricherMiddleware{registry: registry}
}

func (m *pluginEnricherMiddleware) Name() string { return "plugin_enrichers" }

// Priority 150 = after built-in middlewares (skills=100, agents=110, memory=120)
// but before post-processing (token trimming=300).
func (m *pluginEnricherMiddleware) Priority() int { return 150 }

func (m *pluginEnricherMiddleware) Process(mc *MessageContext) error {
	if m.registry == nil || m.registry.Count() == 0 {
		return nil
	}

	// Inject session info into context so enrichers can filter by channel/chatID
	ctx := mc.Ctx
	if mc.Channel != "" {
		ctx = ctxkeys.WithChannel(ctx, mc.Channel)
	}
	if mc.ChatID != "" {
		ctx = ctxkeys.WithApprovalTarget(ctx, mc.ChatID, mc.SenderID)
	}

	content := m.registry.RunAll(ctx)
	if content != "" {
		mc.SystemParts["plugin_enrichers"] = content
	}
	return nil
}

// Compile-time check.
var _ MessageMiddleware = (*pluginEnricherMiddleware)(nil)
