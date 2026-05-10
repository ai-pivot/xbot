package channel

import (
	"encoding/json"
	"os"
	"strconv"

	"xbot/config"
	"xbot/tools"
)

// SettingScope defines where a setting's value is stored and persisted.
type SettingScope int

const (
	ScopeGlobal       SettingScope = iota // Shared across all users, persisted in config.json
	ScopeUser                             // Per-user preference, persisted in user_settings DB
	ScopeSubscription                     // Per-subscription LLM field, persisted in user_llm_subscriptions DB
	ScopeAction                           // UI action trigger, not persisted
)

// ConfigPermission defines the AI accessibility level for a setting.
type ConfigPermission string

const (
	PermTransient  ConfigPermission = "transient"  // Layer 0: AI free to modify, no confirmation
	PermPersistent ConfigPermission = "persistent" // Layer 2: AI can modify with approval
	PermManual     ConfigPermission = "manual"     // Layer 3: AI cannot modify, manual only
)

// SettingDef defines a single setting key — its scope, AI permission level, and whether it needs runtime application.
// This is the SINGLE source of truth for all setting keys in the system.
//
// To add a new setting:
//  1. Add a SettingDef entry to AllSettingDefs below
//  2. Add a handler to serverapp/setting_handlers.go (if Runtime=true)
//  3. Add a handler to cmd/xbot-cli/setting_handlers.go (if Runtime=true)
//  4. That's it — all scope maps and key lists are auto-derived.
type SettingDef struct {
	Key        string           // Unique string key used in UI, DB, and RPC
	Scope      SettingScope     // Where this setting's value lives
	Runtime    bool             // If true, requires runtime apply handler (config + backend side-effect)
	Permission ConfigPermission // AI accessibility level (transient/persistent/manual)
	Sensitive  bool             // If true, value is masked in AI context and write is blocked

	// AI-native metadata — used by config tool's "list" action to help AI understand settings.
	// Optional; zero values work for backward compat. New settings should fill these.
	AIDescription string // Human-readable description for AI (e.g. "Controls the TUI color theme")
	ValidValues   string // Allowed values hint (e.g. "ocean|default|pastel", "20-100")
	DefaultValue  string // Default when not configured (for AI context, not enforced)
}

// AllSettingDefs is the single registry of all known setting keys.
// Every other scope map, runtime key list, and known-key check is derived from this.
var AllSettingDefs = []SettingDef{
	// ── LLM Subscription-scoped fields (persisted in user_llm_subscriptions DB) ──
	{Key: "llm_provider", Scope: ScopeSubscription, Permission: PermManual, AIDescription: "LLM provider (only openai and anthropic are supported)", ValidValues: "openai|anthropic", DefaultValue: "openai"},
	{Key: "llm_api_key", Scope: ScopeSubscription, Permission: PermManual, Sensitive: true, AIDescription: "API key for the LLM provider (masked)", ValidValues: "any valid API key starting with sk-"},
	{Key: "llm_base_url", Scope: ScopeSubscription, Permission: PermManual, AIDescription: "Custom API base URL (leave empty for default)", ValidValues: "empty or valid HTTPS URL"},
	{Key: "llm_model", Scope: ScopeSubscription, Permission: PermManual, AIDescription: "Model name to use (provider-specific, e.g. gpt-4o, claude-sonnet-4-20250514)", ValidValues: "provider-specific model ID"},
	{Key: "max_output_tokens", Scope: ScopeSubscription, Permission: PermPersistent, AIDescription: "Maximum tokens per response", ValidValues: "1-131072", DefaultValue: "4096"},
	{Key: "thinking_mode", Scope: ScopeSubscription, Permission: PermPersistent, AIDescription: "Enable thinking/reasoning mode (supported models only)", ValidValues: "true|false", DefaultValue: "true"},

	// ── User-scoped settings (per-user, persisted in user_settings DB) ──
	{Key: "enable_stream", Scope: ScopeUser, Permission: PermTransient, AIDescription: "Show LLM output token-by-token instead of waiting for completion", ValidValues: "true|false", DefaultValue: "true"},
	{Key: "enable_masking", Scope: ScopeUser, Permission: PermPersistent, AIDescription: "Hide old tool results behind 📂 markers to save context", ValidValues: "true|false", DefaultValue: "true"},

	// ── Global-scoped settings (shared, persisted in config.json) ──
	{Key: "sandbox_mode", Scope: ScopeGlobal, Runtime: true, Permission: PermPersistent, AIDescription: "Execution sandbox type for shell commands", ValidValues: "none|docker|remote", DefaultValue: "none"},
	{Key: "compression_threshold", Scope: ScopeUser, Runtime: true, Permission: PermPersistent, AIDescription: "Token count at which context compression triggers", ValidValues: "any positive integer", DefaultValue: "0"},
	{Key: "memory_provider", Scope: ScopeGlobal, Runtime: true, Permission: PermPersistent, AIDescription: "Memory backend for agent state persistence", ValidValues: "flat|letta", DefaultValue: "flat"},
	{Key: "tavily_api_key", Scope: ScopeGlobal, Runtime: true, Permission: PermManual, Sensitive: true, AIDescription: "API key for Tavily web search", ValidValues: "any valid Tavily API key"},
	{Key: "default_user", Scope: ScopeGlobal, Permission: PermPersistent, AIDescription: "Default username for new sessions", ValidValues: "any valid username"},
	{Key: "privileged_user", Scope: ScopeGlobal, Permission: PermManual, AIDescription: "Username with full admin access", ValidValues: "any valid username"},

	// ── User-scoped settings (per-user, persisted in user_settings DB) ──
	{Key: "theme", Scope: ScopeUser, Permission: PermTransient, AIDescription: "TUI color theme (use tui_control set_theme to switch)", ValidValues: "theme name (see sidebar palette or config list)", DefaultValue: "default"},

	// Layout configuration
	{Key: "layout_mode", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Chat layout density", ValidValues: "default|compact|wide", DefaultValue: "default"},
	{Key: "sidebar_enabled", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Show or hide the session sidebar", ValidValues: "true|false", DefaultValue: "true"},
	{Key: "sidebar_width", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Sidebar width in character columns", ValidValues: "15-60", DefaultValue: "20"},
	{Key: "sidebar_position", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Sidebar position relative to chat", ValidValues: "left|right", DefaultValue: "left"},
	{Key: "sidebar_sections", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Which sections to show in the sidebar", ValidValues: "comma-separated: agents,history,worktrees"},
	{Key: "chat_max_width", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Maximum width of chat area in columns (0=unlimited)", ValidValues: "0-200", DefaultValue: "0"},
	{Key: "chat_center", Scope: ScopeUser, Runtime: true, Permission: PermTransient, AIDescription: "Center the chat area horizontally", ValidValues: "true|false", DefaultValue: "false"},

	{Key: "language", Scope: ScopeUser, Permission: PermTransient, AIDescription: "UI language", ValidValues: "zh|en|ja", DefaultValue: "zh"},
	{Key: "context_mode", Scope: ScopeUser, Runtime: true, Permission: PermPersistent, AIDescription: "How agent handles context window: auto (compress) or manual", ValidValues: "auto|manual", DefaultValue: "auto"},
	{Key: "max_iterations", Scope: ScopeUser, Runtime: true, Permission: PermPersistent, AIDescription: "Maximum tool-calling iterations per turn", ValidValues: "1-500", DefaultValue: "30"},
	{Key: "max_concurrency", Scope: ScopeUser, Runtime: true, Permission: PermPersistent, AIDescription: "Max parallel LLM calls per user", ValidValues: "1-100", DefaultValue: "5"},
	{Key: "max_context_tokens", Scope: ScopeUser, Runtime: true, Permission: PermPersistent, AIDescription: "Target context window size for compression", ValidValues: "any positive integer"},
	{Key: "enable_auto_compress", Scope: ScopeUser, Runtime: true, Permission: PermPersistent, AIDescription: "Legacy alias for context_mode=auto (deprecated)", ValidValues: "true|false"},
	{Key: "runner_server", Scope: ScopeUser, Permission: PermPersistent, AIDescription: "Remote sandbox server address", ValidValues: "host:port or URL"},
	{Key: "runner_token", Scope: ScopeUser, Permission: PermManual, Sensitive: true, AIDescription: "Auth token for remote sandbox runner (masked on read)", ValidValues: "any valid token"},
	{Key: "runner_workspace", Scope: ScopeUser, Permission: PermPersistent, AIDescription: "Workspace directory on remote runner", ValidValues: "any valid path"},
	{Key: "vanguard_model", Scope: ScopeUser, Runtime: true, Permission: PermManual, AIDescription: "Model for vanguard tier (strongest, for complex tasks)", ValidValues: "any model name"},
	{Key: "balance_model", Scope: ScopeUser, Runtime: true, Permission: PermManual, AIDescription: "Model for balance tier (default)", ValidValues: "any model name"},
	{Key: "swift_model", Scope: ScopeUser, Runtime: true, Permission: PermManual, AIDescription: "Model for swift tier (fast, for simple tasks)", ValidValues: "any model name"},

	// ── Action keys (UI triggers, not persisted) ──
	{Key: "subscription_manage", Scope: ScopeAction, AIDescription: "Open the subscription management panel"},
	{Key: "runner_panel", Scope: ScopeAction, AIDescription: "Open the remote runner configuration panel"},
	{Key: "danger_zone", Scope: ScopeAction, AIDescription: "Open the danger zone panel (reset/clear data)"},
}

// init-time derived indexes — built once, used everywhere.
var (
	allSettingDefsMap map[string]SettingDef
	scopeIndex        map[SettingScope]map[string]struct{}
)

func init() {
	allSettingDefsMap = make(map[string]SettingDef, len(AllSettingDefs))
	scopeIndex = make(map[SettingScope]map[string]struct{})
	for _, s := range AllSettingDefs {
		allSettingDefsMap[s.Key] = s
		if scopeIndex[s.Scope] == nil {
			scopeIndex[s.Scope] = make(map[string]struct{})
		}
		scopeIndex[s.Scope][s.Key] = struct{}{}
	}
}

// GetSettingDef returns the SettingDef for a key, or (SettingDef{}, false) if unknown.
func GetSettingDef(key string) (SettingDef, bool) {
	d, ok := allSettingDefsMap[key]
	return d, ok
}

// SettingScopeOf returns the scope of a setting key. Returns ("unknown") for unrecognized keys.
func SettingScopeOf(key string) string {
	if d, ok := allSettingDefsMap[key]; ok {
		switch d.Scope {
		case ScopeUser:
			return "user"
		case ScopeGlobal:
			return "global"
		case ScopeSubscription:
			return "subscription"
		case ScopeAction:
			return "action"
		}
	}
	return "unknown"
}

// CLIRuntimeSettingKeys lists all setting keys that require runtime application
// beyond DB persistence. Both serverapp and cmd/xbot-cli use this list to verify
// every runtime-affecting key has a handler registered.
//
// Derived from AllSettingDefs — do not edit manually.
var CLIRuntimeSettingKeys []string

func init() {
	for _, d := range AllSettingDefs {
		if d.Runtime {
			CLIRuntimeSettingKeys = append(CLIRuntimeSettingKeys, d.Key)
		}
	}
}

// IsUserScopedSettingKey returns true if the key has ScopeUser.
func IsUserScopedSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeUser][key]
	return ok
}

// IsGlobalScopedSettingKey returns true if the key has ScopeGlobal.
func IsGlobalScopedSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeGlobal][key]
	return ok
}

// IsSubscriptionScopedSettingKey returns true if the key has ScopeSubscription.
func IsSubscriptionScopedSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeSubscription][key]
	return ok
}

// IsActionSettingKey returns true if the key has ScopeAction.
func IsActionSettingKey(key string) bool {
	_, ok := scopeIndex[ScopeAction][key]
	return ok
}

// AllConfigItemsForAI returns user-facing settings with AI metadata for the config tool's "list" action.
// All scopes are included — subscription keys for LLM config, global keys from config.json,
// and user-scoped keys from user_settings DB. The caller enriches CurrentVal from SettingsSvc.
// Each new SettingDef automatically appears here — zero extra work.
func AllConfigItemsForAI() []tools.ConfigListItem {
	globalVals := readGlobalConfigValues()
	result := make([]tools.ConfigListItem, 0, len(AllSettingDefs))
	for _, d := range AllSettingDefs {
		// Skip action-scoped (UI triggers: subscription_manage, runner_panel, danger_zone)
		if d.Scope == ScopeAction {
			continue
		}
		scope := "user"
		switch d.Scope {
		case ScopeGlobal:
			scope = "global"
		case ScopeSubscription:
			scope = "subscription"
		}
		perm := string(d.Permission)
		if perm == "" {
			perm = string(PermPersistent) // default
		}
		result = append(result, tools.ConfigListItem{
			Key:         d.Key,
			Description: d.AIDescription,
			Permission:  perm,
			Scope:       scope,
			ValidValues: d.ValidValues,
			DefaultVal:  d.DefaultValue,
			Sensitive:   d.Sensitive,
			CurrentVal:  globalVals[d.Key], // global-scoped: from config.json, user-scoped: caller enriches from SettingsSvc
		})
	}
	return result
}

// readGlobalConfigValues reads config.json and returns all top-level string values.
// Used by AllConfigItemsForAI to get current values for global-scoped settings
// (which are not in user_settings DB).
func readGlobalConfigValues() map[string]string {
	raw, err := os.ReadFile(config.ConfigFilePath())
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	out := make(map[string]string)
	for k, v := range m {
		switch val := v.(type) {
		case string:
			out[k] = val
		case float64:
			out[k] = strconv.Itoa(int(val))
		case bool:
			out[k] = strconv.FormatBool(val)
		}
	}
	return out
}
