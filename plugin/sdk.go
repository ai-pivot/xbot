package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
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

// ---------------------------------------------------------------------------
// ToolResult Formatting - structured output helpers for plugin authors
// ---------------------------------------------------------------------------

// FormatToolResult creates a formatted tool result with structured data.
// Sections are rendered as "key: value" lines sorted by key for deterministic output.
// If sections is empty or nil, only the title is returned as content.
//
// Example:
//
//	result := FormatToolResult("Server Info", map[string]string{
//	    "status":  "running",
//	    "version": "2.0.1",
//	})
//	// Content: "Server Info\nstatus: running\nversion: 2.0.1"
func FormatToolResult(title string, sections map[string]string) *ToolResult {
	if len(sections) == 0 {
		return NewToolResult(title)
	}

	keys := make([]string, 0, len(sections))
	for k := range sections {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString(title)
	b.WriteByte('\n')
	for i, k := range keys {
		b.WriteString(k)
		b.WriteString(": ")
		b.WriteString(sections[k])
		if i < len(keys)-1 {
			b.WriteByte('\n')
		}
	}

	return NewToolResult(b.String())
}

// FormatListResult creates a tool result displaying a numbered list.
// Each item is prefixed with "N. " where N starts from 1.
// An empty or nil slice returns "(no items)".
//
// Example:
//
//	result := FormatListResult([]string{"alpha", "beta"})
//	// Content: "1. alpha\n2. beta"
func FormatListResult(items []string) *ToolResult {
	if len(items) == 0 {
		return NewToolResult("(no items)")
	}

	var b strings.Builder
	for i, item := range items {
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%d. %s", i+1, item)
	}

	return NewToolResult(b.String())
}

// FormatErrorResult creates a user-friendly error result.
// If err is nil, the error message is "unknown error".
//
// Example:
//
//	result := FormatErrorResult("deploy", errors.New("timeout"))
//	// Content: "deploy failed: timeout", IsError: true
func FormatErrorResult(operation string, err error) *ToolResult {
	msg := "unknown error"
	if err != nil {
		msg = err.Error()
	}
	return NewToolError(fmt.Sprintf("%s failed: %s", operation, msg))
}
