package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

// ---------------------------------------------------------------------------
// SDK Helpers — convenience functions for plugin authors
// ---------------------------------------------------------------------------

// MustActivate wraps Activate and panics on error.
// Use in init() for plugins that must be available at startup.
func MustActivate(p Plugin, ctx PluginContext) {
	if err := p.Activate(ctx); err != nil {
		panic(fmt.Sprintf("plugin %s activation failed: %v", p.Manifest().ID, err))
	}
}

// ToolFromFunc creates a PluginTool from a simple function signature.
// The fn receives a plain string input and returns a plain string result.
func ToolFromFunc(name, desc string, fn func(ctx context.Context, input string) (string, error)) PluginTool {
	return &SimplePluginTool{
		Def: ToolDef{Name: name, Description: desc},
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			result, err := fn(ctx, input)
			if err != nil {
				return nil, err
			}
			return NewToolResult(result), nil
		},
	}
}

// ToolFromJSONFunc creates a tool that accepts JSON input and returns
// structured output which is automatically marshaled to JSON.
func ToolFromJSONFunc(name, desc string, params []ToolParamDef, fn func(ctx context.Context, input json.RawMessage) (any, error)) PluginTool {
	def := BuildToolDef(name, desc, params...)
	return &SimplePluginTool{
		Def: def,
		ExecFn: func(ctx context.Context, input string) (*ToolResult, error) {
			result, err := fn(ctx, json.RawMessage(input))
			if err != nil {
				return nil, err
			}
			jsonBytes, err := json.Marshal(result)
			if err != nil {
				return nil, fmt.Errorf("marshal result: %w", err)
			}
			return NewToolResult(string(jsonBytes)), nil
		},
	}
}

// DenyHook creates a HookHandler that always denies with the given message.
func DenyHook(msg string) HookHandler {
	return func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionDeny, Message: msg}, nil
	}
}

// AllowHook creates a HookHandler that always allows.
func AllowHook() HookHandler {
	return func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		return &HookResult{Decision: DecisionAllow}, nil
	}
}

// LogHook creates a HookHandler that logs the event and allows.
func LogHook(logger Logger, msg string) HookHandler {
	return func(ctx context.Context, payload *HookPayload) (*HookResult, error) {
		logger.Info(msg, Field{Key: "event", Value: string(payload.Event)})
		return &HookResult{Decision: DecisionAllow}, nil
	}
}

// StaticEnricher creates a ContextEnricher that always returns the given content.
func StaticEnricher(content string) ContextEnricher {
	return func(ctx context.Context) (string, error) {
		return content, nil
	}
}

// FileEnricher creates a ContextEnricher that reads content from a file.
func FileEnricher(path string) ContextEnricher {
	return func(ctx context.Context) (string, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read enricher file %s: %w", path, err)
		}
		return string(data), nil
	}
}

// ---------------------------------------------------------------------------
// Manifest Builder — fluent helpers for constructing PluginManifest
// ---------------------------------------------------------------------------

// QuickManifest creates a minimal valid PluginManifest with sensible defaults.
// Use ManifestOption functions to customize.
func QuickManifest(id, name, version, description string, opts ...ManifestOption) PluginManifest {
	m := PluginManifest{
		ID:               id,
		Name:             name,
		Version:          version,
		Description:      description,
		Runtime:          RuntimeNative,
		ActivationEvents: []string{"onStart"},
	}
	for _, opt := range opts {
		opt(&m)
	}
	return m
}

// ManifestOption customizes a PluginManifest created by QuickManifest.
type ManifestOption func(*PluginManifest)

// WithPermissions adds permission strings to the manifest.
func WithPermissions(perms ...string) ManifestOption {
	return func(m *PluginManifest) {
		m.Permissions = append(m.Permissions, perms...)
	}
}

// WithActivationEvents sets the activation events (replaces default "onStart").
func WithActivationEvents(events ...string) ManifestOption {
	return func(m *PluginManifest) {
		m.ActivationEvents = events
	}
}

// WithRuntime sets the plugin runtime type.
func WithRuntime(rt RuntimeType) ManifestOption {
	return func(m *PluginManifest) {
		m.Runtime = rt
	}
}

// WithTools adds tool contributions to the manifest.
func WithTools(tools ...ToolContribution) ManifestOption {
	return func(m *PluginManifest) {
		if m.Contributes == nil {
			m.Contributes = &PluginContributes{}
		}
		m.Contributes.Tools = append(m.Contributes.Tools, tools...)
	}
}

// WithHooks adds hook contributions to the manifest.
func WithHooks(hooks ...HookContribution) ManifestOption {
	return func(m *PluginManifest) {
		if m.Contributes == nil {
			m.Contributes = &PluginContributes{}
		}
		m.Contributes.Hooks = append(m.Contributes.Hooks, hooks...)
	}
}

// WithEnrichers adds context enricher contributions to the manifest.
func WithEnrichers(enrichers ...EnricherContribution) ManifestOption {
	return func(m *PluginManifest) {
		if m.Contributes == nil {
			m.Contributes = &PluginContributes{}
		}
		m.Contributes.ContextEnrichers = append(m.Contributes.ContextEnrichers, enrichers...)
	}
}
