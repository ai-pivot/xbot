// Package plugin provides xbot's plugin system with VSCode-like extensibility.
//
// The plugin system supports multiple runtimes (native Go, gRPC, future WASM)
// through a unified Plugin API. Plugins are discovered via plugin.json manifests,
// activated lazily based on events, and sandboxed through the PluginContext interface.
package plugin

import (
	"context"

	"time"

	"xbot/llm"
)

// ---------------------------------------------------------------------------
// Plugin Interface — the single entry point for all plugins
// ---------------------------------------------------------------------------

// Plugin is the core interface that all xbot plugins must implement.
// A plugin contributes tools, hooks, and/or context enrichers through
// a unified activation lifecycle.
//
// Implementations are registered via PluginManager.Register() for native plugins,
// or discovered automatically for gRPC plugins based on their manifest.
type Plugin interface {
	// Manifest returns plugin metadata. Called once during discovery phase.
	// Must return a non-nil, valid PluginManifest.
	Manifest() PluginManifest

	// Activate initializes the plugin and registers all capabilities
	// (tools, hooks, context enrichers) through the provided PluginContext.
	// Called when an activation event matches.
	//
	// Implementations should be idempotent — safe to call multiple times.
	Activate(ctx PluginContext) error

	// Deactivate cleans up plugin resources. Called on shutdown or when
	// the plugin is being unloaded. After Deactivate, no further callbacks
	// will be invoked on this plugin.
	Deactivate(ctx PluginContext) error
}

// ---------------------------------------------------------------------------
// Plugin Manifest — declarative plugin metadata
// ---------------------------------------------------------------------------

// PluginManifest describes a plugin's metadata, capabilities, and requirements.
// It is loaded from plugin.json in the plugin directory.
type PluginManifest struct {
	// ID is the unique plugin identifier (reverse DNS recommended).
	// Must be non-empty and unique across all loaded plugins.
	ID string `json:"id"`

	// Name is the human-readable plugin name.
	Name string `json:"name"`

	// Version follows semver (e.g., "1.0.0").
	Version string `json:"version"`

	// Description is a short summary of what the plugin does.
	Description string `json:"description"`

	// Author is the plugin author or organization.
	Author string `json:"author,omitempty"`

	// Homepage is the URL to the plugin's source or documentation.
	Homepage string `json:"homepage,omitempty"`

	// Runtime specifies the plugin execution environment.
	// Valid values: "native", "grpc" (future: "wasm")
	Runtime RuntimeType `json:"runtime"`

	// Entry is the plugin entry point.
	// For native: Go function name (default: "Plugin")
	// For grpc: command to start the plugin process
	Entry string `json:"entry"`

	// Executable is the command to start the plugin process (gRPC runtime).
	// If set, takes precedence over Entry. Use this for security.
	Executable string `json:"executable,omitempty"`

	// Args are command-line arguments passed to Executable.
	Args []string `json:"args,omitempty"`

	// ActivationEvents lists events that trigger plugin activation.
	// Supports: "onStart", "onTool:<name>", "onHook:<event>", "onCommand:<cmd>"
	// Empty means onStart.
	ActivationEvents []string `json:"activationEvents"`

	// Permissions declares required capabilities.
	// The plugin can only access APIs for declared permissions.
	Permissions []string `json:"permissions"`

	// Contributes declares what the plugin provides to xbot.
	Contributes *PluginContributes `json:"contributes,omitempty"`

	// Dependencies lists other plugins this plugin depends on.
	// Currently only format validation is performed; version resolution
	// will be added in a future iteration.
	Dependencies []PluginDependency `json:"dependencies,omitempty"`

	// Timeout is the maximum duration for plugin activation and tool operations.
	// Zero means DefaultPluginTimeout (30s). Not serialized directly via JSON;
	// parsed from manifest's "timeout" string field (e.g., "30s", "1m").
	Timeout time.Duration `json:"-"`
}

// PluginDependency declares a dependency on another plugin.
// Dependencies are validated during manifest loading to ensure
// required plugins are available.
type PluginDependency struct {
	// ID is the unique identifier of the required plugin.
	ID string `json:"id"`

	// Version is the required version constraint (semver range).
	// Currently only format validation is performed; actual version
	// resolution is planned for a future iteration.
	Version string `json:"version"`
}

// PluginContributes declares the capabilities a plugin provides.
type PluginContributes struct {
	Tools            []ToolContribution         `json:"tools,omitempty"`
	Hooks            []HookContribution         `json:"hooks,omitempty"`
	ContextEnrichers []EnricherContribution     `json:"contextEnrichers,omitempty"`
	Commands         []CommandContribution      `json:"commands,omitempty"`
	Configuration    *ConfigurationContribution `json:"configuration,omitempty"`
	UI               []UISlotContribution       `json:"ui,omitempty"`
}

// ToolContribution describes a tool provided by the plugin.
type ToolContribution struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// HookContribution describes a lifecycle hook the plugin subscribes to.
type HookContribution struct {
	Event   string `json:"event"`             // e.g., "PreToolUse", "PostToolUse"
	Matcher string `json:"matcher,omitempty"` // tool name pattern, "" = all
}

// EnricherContribution describes a context enricher that injects dynamic
// content into the system prompt.
type EnricherContribution struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// CommandContribution describes a slash command provided by the plugin.
type CommandContribution struct {
	Name        string `json:"name"` // e.g., "/deploy"
	Description string `json:"description"`
}

// ConfigurationContribution declares user-configurable settings for the plugin.
// Defined in plugin.json under "contributes.configuration".
// Users can override these settings via ~/.xbot/plugins/<id>/config.json.
type ConfigurationContribution struct {
	// Title is a human-readable title for this configuration section.
	Title string `json:"title"`
	// Properties defines the individual configuration properties.
	Properties map[string]ConfigProperty `json:"properties"`
}

// ConfigProperty describes a single configuration property.
type ConfigProperty struct {
	// Type is the JSON schema type: "string", "number", "boolean".
	Type string `json:"type"`
	// Default is the default value when no user configuration exists.
	Default any `json:"default,omitempty"`
	// Description explains the property's purpose.
	Description string `json:"description"`
}

// ---------------------------------------------------------------------------
// UI Contributions — VSCode-like GUI extension points
// ---------------------------------------------------------------------------

// UISlotContribution declares a UI contribution in plugin.json.
// Each entry reserves a widget slot in a predefined zone.
type UISlotContribution struct {
	// ID uniquely identifies this widget within the plugin.
	// Must be unique per plugin. Used as the key for runtime updates.
	ID string `json:"id"`

	// Slot is the target UI zone where the widget renders.
	// Valid values: "titleBarLeft", "titleBarRight", "statusBarLeft",
	// "statusBarRight", "infoBar", "footer".
	Slot string `json:"slot"`

	// Priority controls ordering within a zone (lower = earlier/leftmost).
	// Default 100.
	Priority int `json:"priority,omitempty"`

	// Description is a human-readable explanation of what this widget shows.
	Description string `json:"description,omitempty"`

	// RefreshInterval is the suggested polling interval (e.g. "30s").
	// Only advisory — push-based UpdateWidget is preferred.
	RefreshInterval string `json:"refreshInterval,omitempty"`

	// Triggers is a list of hook matchers that trigger an instant script run.
	// Format: "EventName:Matcher" (e.g. "PostToolUse:Shell*").
	// Only effective for script runtime plugins.
	Triggers []string `json:"triggers,omitempty"`

	// Sync controls whether hook triggers run synchronously (inline in the
	// hook goroutine) or asynchronously (via triggerCh).  Sync triggers
	// block the tool pipeline until the script finishes, so the engine can
	// read output immediately.  Default false (async).
	Sync bool `json:"sync,omitempty"`

	// Interactive indicates whether this widget supports user actions (v2).
	// Default false (read-only).
	Interactive bool `json:"interactive,omitempty"`
}

// StyleClass is a semantic style hint. The TUI maps these to theme colors.
// Plugins must NOT output raw ANSI escape sequences.
type StyleClass string

const (
	StyleNormal  StyleClass = "normal"
	StyleDim     StyleClass = "dim"
	StyleAccent  StyleClass = "accent"
	StyleSuccess StyleClass = "success"
	StyleWarning StyleClass = "warning"
	StyleError   StyleClass = "error"
	StyleInfo    StyleClass = "info"
	StyleMuted   StyleClass = "muted"
	StyleRaw     StyleClass = "raw" // pass-through: no ANSI wrapping, text contains its own escapes
)

// WidgetSpan is a single styled text segment. A widget returns zero or more
// spans, and the TUI applies theme colors based on StyleClass. No raw ANSI
// is ever exposed to the terminal from plugin spans.
type WidgetSpan struct {
	Text  string
	Style StyleClass
}

// UIWidget is the interface plugins implement to provide UI content.
// Render(width) returns styled spans that the TUI composits into the layout.
// The width parameter is the available columns for this widget (0 = unbounded).
// Plugins must NOT include ANSI escape sequences in Text fields.
type UIWidget interface {
	Render(width int) []WidgetSpan
}

// ---------------------------------------------------------------------------
// Runtime Type
// ---------------------------------------------------------------------------

// RuntimeType specifies how a plugin is executed.
type RuntimeType string

const (
	// RuntimeNative runs the plugin in-process as Go code.
	RuntimeNative RuntimeType = "native"

	// RuntimeGRPC runs the plugin as an external process communicating via gRPC.
	RuntimeGRPC RuntimeType = "grpc"

	// RuntimeWASM runs the plugin in a WASM sandbox (Phase 2).
	RuntimeWASM RuntimeType = "wasm"

	// RuntimeScript runs an external script/command periodically as a plugin.
	// Language-agnostic: any executable (bash, python, binary, etc.) works.
	// The script's stdout is used as widget content. No JSON protocol needed.
	RuntimeScript RuntimeType = "script"
)

// ---------------------------------------------------------------------------
// PluginTool — tool definition for plugin-provided tools
// ---------------------------------------------------------------------------

// PluginTool defines a tool that a plugin provides. This is the plugin-side
// interface — the plugin system adapts it to tools.Tool internally.
type PluginTool interface {
	// Definition returns the tool's JSON schema definition for LLM consumption.
	Definition() ToolDef

	// Execute runs the tool with the given input and returns a result.
	// The input is a JSON string matching the tool's input schema.
	Execute(ctx context.Context, input string) (*ToolResult, error)
}

// ---------------------------------------------------------------------------
// ToolCallContext — rich context for plugin tool execution
// ---------------------------------------------------------------------------

// ToolCallContext carries session and identity information for a tool call.
// It is passed to PluginToolV2.ExecuteWithContext, giving plugins access to
// session metadata without requiring a full context.Context.
type ToolCallContext struct {
	// SessionID identifies the current conversation session.
	SessionID string

	// Channel is the message channel (e.g., "cli", "feishu", "web").
	Channel string

	// ChatID is the chat or conversation ID within the channel.
	ChatID string

	// UserID identifies the user who triggered the tool call.
	UserID string

	// Ctx carries cancellation and deadline information.
	Ctx context.Context
}

// ---------------------------------------------------------------------------
// PluginToolV2 — extended tool interface with rich call context
// ---------------------------------------------------------------------------

// PluginToolV2 is an extended version of PluginTool that receives a ToolCallContext
// instead of a bare context.Context. Plugins can implement this interface to get
// access to session metadata (session ID, channel, user ID, etc.).
//
// The PluginToolBridge checks for V2 first and falls back to V1.
type PluginToolV2 interface {
	PluginTool

	// ExecuteWithContext runs the tool with a rich call context.
	ExecuteWithContext(ctx *ToolCallContext, input string) (*ToolResult, error)
}

// ToolDef is the tool definition for LLM function calling.
type ToolDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  []llm.ToolParam `json:"parameters"`
	// Version is the tool version following semver (e.g., "1.0.0").
	// When set, it is included in ToJSONSchema() output for version tracking.
	Version string `json:"version,omitempty"`
	// InputSchema is the auto-generated JSON Schema for tool parameters.
	// When set, this provides a complete parameter schema for LLM function calling.
	// Generated automatically by BuildToolDef; manual ToolDef construction leaves this nil.
	InputSchema map[string]any `json:"inputSchema,omitempty"`
}

// ToJSONSchema returns the tool definition in OpenAI function calling format.
// The returned map has the structure:
//
//	{
//	  "type": "function",
//	  "function": {
//	    "name": "...",
//	    "description": "...",
//	    "parameters": { "type": "object", "properties": {...}, "required": [...] },
//	    "version": "..."  // only if Version is set
//	  }
//	}
//
// If InputSchema is already populated (e.g., from BuildToolDef), it is used directly
// as the parameters value; otherwise, the schema is built from the Parameters slice.
func (td ToolDef) ToJSONSchema() map[string]any {
	fn := map[string]any{
		"name":        td.Name,
		"description": td.Description,
		"parameters":  td.buildParameters(),
	}
	if td.Version != "" {
		fn["version"] = td.Version
	}
	return map[string]any{
		"type":     "function",
		"function": fn,
	}
}

// buildParameters returns the parameters JSON Schema, preferring the pre-built
// InputSchema over reconstructing from Parameters.
// TODO: support ToolParam.Items nested structures in the fallback path.
func (td ToolDef) buildParameters() map[string]any {
	if td.InputSchema != nil {
		return td.InputSchema
	}

	// Fallback: reconstruct from Parameters slice
	properties := make(map[string]any, len(td.Parameters))
	var required []string
	for _, p := range td.Parameters {
		prop := map[string]any{
			"type":        p.Type,
			"description": p.Description,
		}
		if p.Items != nil {
			prop["items"] = p.Items
		}
		properties[p.Name] = prop
		if p.Required {
			required = append(required, p.Name)
		}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// ToolResult is the result of a plugin tool execution.
type ToolResult struct {
	// Content is the primary output to send back to the LLM.
	Content string `json:"content"`

	// IsError indicates the tool execution failed (but the plugin itself ran correctly).
	IsError bool `json:"isError,omitempty"`

	// Metadata carries optional key-value pairs for downstream processing.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// NewToolResult creates a successful tool result.
func NewToolResult(content string) *ToolResult {
	return &ToolResult{Content: content}
}

// NewToolError creates an error tool result.
func NewToolError(content string) *ToolResult {
	return &ToolResult{Content: content, IsError: true}
}

// ---------------------------------------------------------------------------
// ToolResult Builder — fluent API for constructing ToolResult
// ---------------------------------------------------------------------------

// ToolResultBuilder provides a fluent API for building ToolResult instances.
// It allows step-by-step construction of tool results with optional metadata.
//
// Example:
//
//	result := NewResultBuilder().
//	    Content("hello").
//	    Metadata("format", "json").
//	    Build()
type ToolResultBuilder struct {
	result *ToolResult
}

// NewResultBuilder creates a new ToolResultBuilder with default values.
func NewResultBuilder() *ToolResultBuilder {
	return &ToolResultBuilder{result: &ToolResult{}}
}

// Content sets the primary output content.
func (b *ToolResultBuilder) Content(content string) *ToolResultBuilder {
	b.result.Content = content
	return b
}

// Error sets both the content and marks the result as an error.
func (b *ToolResultBuilder) Error(content string) *ToolResultBuilder {
	b.result.Content = content
	b.result.IsError = true
	return b
}

// IsError explicitly sets the error flag.
func (b *ToolResultBuilder) IsError(isError bool) *ToolResultBuilder {
	b.result.IsError = isError
	return b
}

// Metadata adds a key-value pair to the result metadata.
func (b *ToolResultBuilder) Metadata(key, value string) *ToolResultBuilder {
	if b.result.Metadata == nil {
		b.result.Metadata = make(map[string]string)
	}
	b.result.Metadata[key] = value
	return b
}

// Build returns the constructed ToolResult.
func (b *ToolResultBuilder) Build() *ToolResult {
	return b.result
}

// ---------------------------------------------------------------------------
// Hook Types
// ---------------------------------------------------------------------------

// HookEvent identifies a lifecycle event that plugins can subscribe to.
type HookEvent string

const (
	// HookPreToolUse fires before a tool is executed.
	HookPreToolUse HookEvent = "PreToolUse"
	// HookPostToolUse fires after a tool execution succeeds.
	HookPostToolUse HookEvent = "PostToolUse"
	// HookPostToolUseError fires when a tool execution fails.
	HookPostToolUseError HookEvent = "PostToolUseFailure"
	// HookUserPromptSubmit fires when the user submits a prompt.
	HookUserPromptSubmit HookEvent = "UserPromptSubmit"
	// HookAgentStop fires when the agent loop terminates.
	HookAgentStop HookEvent = "AgentStop"
	// HookSessionStart fires at the beginning of a new session.
	HookSessionStart HookEvent = "SessionStart"
	// HookSessionEnd fires when a session concludes.
	HookSessionEnd HookEvent = "SessionEnd"
	// HookSubAgentStart fires before a sub-agent is launched.
	HookSubAgentStart HookEvent = "SubAgentStart"
	// HookSubAgentStop fires after a sub-agent completes.
	HookSubAgentStop HookEvent = "SubAgentStop"
	// HookPreCompact fires before message history compaction.
	HookPreCompact HookEvent = "PreCompact"
	// HookPostCompact fires after message history compaction.
	HookPostCompact HookEvent = "PostCompact"
	// HookCronFired fires when a scheduled cron job triggers.
	HookCronFired HookEvent = "CronFired"
	// HookWebhookReceived fires when an inbound webhook arrives.
	HookWebhookReceived HookEvent = "WebhookReceived"
)

// HookDecision represents a hook handler's decision on whether to allow an operation.
type HookDecision string

const (
	// DecisionAllow permits the operation to proceed.
	DecisionAllow HookDecision = "allow"
	// DecisionDeny blocks the operation and optionally provides a reason.
	DecisionDeny HookDecision = "deny"
	// DecisionAsk prompts the user for confirmation before proceeding.
	DecisionAsk HookDecision = "ask"
	// DecisionDefer defers the decision to the next handler in the chain.
	DecisionDefer HookDecision = "defer"
)

// HookResult is returned by a hook handler after processing an event.
type HookResult struct {
	Decision HookDecision   `json:"decision"`
	Message  string         `json:"message,omitempty"` // Explanation for deny/ask
	Data     map[string]any `json:"data,omitempty"`
}

// HookPayload carries event-specific data to hook handlers.
type HookPayload struct {
	Event         HookEvent      `json:"event"`
	ToolName      string         `json:"toolName,omitempty"`
	ToolInput     string         `json:"toolInput,omitempty"`
	ToolOutput    string         `json:"toolOutput,omitempty"`    // tool execution result (PostToolUse only)
	ToolElapsedMs int64          `json:"toolElapsedMs,omitempty"` // tool execution duration in ms
	SessionID     string         `json:"sessionId,omitempty"`
	Channel       string         `json:"channel,omitempty"`
	ChatID        string         `json:"chatId,omitempty"`
	UserID        string         `json:"userId,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
}

// HookHandler processes a lifecycle event and returns a decision.
type HookHandler func(ctx context.Context, payload *HookPayload) (*HookResult, error)

// ---------------------------------------------------------------------------
// Context Enricher
// ---------------------------------------------------------------------------

// ContextEnricher injects dynamic content into the system prompt.
// This upgrades the current static Skills (SKILL.md) model to executable logic.
type ContextEnricher func(ctx context.Context) (string, error)

// ---------------------------------------------------------------------------
// Plugin State
// ---------------------------------------------------------------------------

// PluginState tracks the lifecycle state of a plugin.
type PluginState string

const (
	// StateDiscovered means the manifest is loaded and the plugin is waiting for activation.
	StateDiscovered PluginState = "discovered"
	// StateActivating means Activate() is in progress.
	StateActivating PluginState = "activating"
	// StateActive means the plugin is running and contributing tools/hooks/enrichers.
	StateActive PluginState = "active"
	// StateDeactivating means Deactivate() is in progress.
	StateDeactivating PluginState = "deactivating"
	// StateInactive means the plugin has been deactivated or unloaded.
	StateInactive PluginState = "inactive"
	// StateError means the plugin failed to activate.
	StateError PluginState = "error"
)

// DefaultPluginTimeout is the default timeout for plugin operations when not
// specified in the manifest.
const DefaultPluginTimeout = 30 * time.Second
