package agent

import (
	"strconv"
	"strings"

	"xbot/channel"
	"xbot/config"
	log "xbot/logger"
	"xbot/tools"
)

// SettingHandler defines how a setting key updates runtime state.
// Each field is optional — a key that only updates config leaves ApplyAgent nil.
type SettingHandler struct {
	// ApplyConfig updates the in-memory config struct. cfg is always non-nil.
	ApplyConfig func(cfg *config.Config, value string)
	// ApplyAgent applies runtime side effects directly on the Agent.
	// Called after ApplyConfig. ag may be nil (remote CLI mode).
	ApplyAgent func(ag *Agent, senderID, chatID, value string)
	// ApplyFull is called with both cfg and ag. Used when the side effect
	// needs config context (e.g. sandbox reinit needs cfg.Agent.WorkDir).
	// If set, called instead of the ApplyConfig+ApplyAgent pair.
	ApplyFull func(cfg *config.Config, ag *Agent, senderID, value string)
}

// SettingHandlerRegistry is the single source of truth for runtime setting effects.
// Both server (serverapp) and CLI (cmd/xbot-cli) use this same registry.
//
// To add a new runtime setting:
//  1. Add the key to channel.CLIRuntimeSettingKeys
//  2. Add a handler here
//  3. Done — no switch-case to update, no if-chain to extend.
var SettingHandlerRegistry = map[string]SettingHandler{
	// --- LLM tier settings (config-only, agent effects applied by caller via SetModelTiers) ---
	"vanguard_model": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.LLM.VanguardModel = strings.TrimSpace(value)
		},
	},
	"balance_model": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.LLM.BalanceModel = strings.TrimSpace(value)
		},
	},
	"swift_model": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.LLM.SwiftModel = strings.TrimSpace(value)
		},
	},

	// --- Agent settings ---
	"sandbox_mode": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.Sandbox.Mode = value },
		ApplyFull: func(cfg *config.Config, ag *Agent, senderID, value string) {
			workDir := cfg.Agent.WorkDir
			if workDir == "" {
				workDir = "."
			}
			tools.ReinitSandbox(cfg.Sandbox, workDir)
			if ag != nil {
				ag.SetSandbox(tools.GetSandbox(), value)
			}
		},
	},
	"compression_threshold": {
		ApplyConfig: func(cfg *config.Config, value string) {
			if f, err := strconv.ParseFloat(value, 64); err == nil && f > 0 {
				cfg.Agent.CompressionThreshold = f
			}
		},
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			if f, err := strconv.ParseFloat(value, 64); err == nil && f > 0 {
				ag.SetCompressionThreshold(f)
			}
		},
	},
	"memory_provider": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.Agent.MemoryProvider = value },
	},
	"tavily_api_key": {}, // Stored in user_settings; WebSearchTool reads dynamically

	"auto_worktree": {
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			ag.autoWorktree = channel.ParseSettingBool(value)
		},
	},

	// --- Runtime state settings (config + agent side-effects) ---
	"context_mode": {
		ApplyConfig: func(cfg *config.Config, value string) { cfg.Agent.ContextMode = value },
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			_ = ag.SetContextMode(value)
		},
	},
	"max_iterations": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.Agent.MaxIterations = channel.ParseSettingInt(value, cfg.Agent.MaxIterations)
		},
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				ag.SetMaxIterations(n)
			}
		},
	},
	"max_concurrency": {
		ApplyConfig: func(cfg *config.Config, value string) {
			cfg.Agent.MaxConcurrency = channel.ParseSettingInt(value, cfg.Agent.MaxConcurrency)
		},
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			if n, err := strconv.Atoi(value); err == nil && n > 0 {
				ag.SetMaxConcurrency(n)
			}
		},
	},
	"max_context_tokens": {
		// max_context is subscription-scoped, stored in PerModelConfigs.
		// Do NOT write to cfg.Agent.MaxContextTokens (global fallback only).
		ApplyConfig: nil,
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			if n, err := strconv.Atoi(value); err == nil && n >= 0 {
				if chatID != "" {
					ag.SetMaxContextTokens(n, chatID)
				} else {
					ag.SetMaxContextTokens(n)
				}
			}
		},
	},
	"enable_auto_compress": {
		ApplyConfig: func(cfg *config.Config, value string) {
			b := channel.ParseSettingBool(value)
			cfg.Agent.EnableAutoCompress = &b
		},
		ApplyAgent: func(ag *Agent, senderID, chatID, value string) {
			if ag == nil {
				return
			}
			if channel.ParseSettingBool(value) {
				_ = ag.SetContextMode("auto")
			} else {
				_ = ag.SetContextMode("none")
			}
		},
	},

	// --- Layout settings (UI-only, no server-side side-effects) ---
	"layout_mode":      {},
	"sidebar_enabled":  {},
	"sidebar_width":    {},
	"sidebar_position": {},
	"sidebar_sections": {},
	"chat_max_width":   {},
	"chat_center":      {},
}

// ApplyRuntimeSetting applies a single setting change to the in-memory config and agent.
// Used after the setting is persisted to DB.
func ApplyRuntimeSetting(cfg *config.Config, ag *Agent, senderID, key, value string) {
	handler, ok := SettingHandlerRegistry[key]
	if !ok {
		if !channel.IsKnownNonRuntimeKey(key) {
			log.WithField("key", key).WithField("value", value).
				Warn("ApplyRuntimeSetting: unhandled setting key, ignoring")
		}
		return
	}
	applyHandler(cfg, ag, senderID, "", handler, value)
	if ag != nil {
		ag.LLMFactory().SetModelTiers(cfg.LLM)
	}
}

// ApplyRuntimeSettings applies a batch of setting changes.
// context_mode is processed LAST so it correctly overrides enable_auto_compress.
// SetModelTiers is called once after all keys are processed.
// Caller should save config after this returns.
func ApplyRuntimeSettings(cfg *config.Config, ag *Agent, senderID string, values map[string]string) {
	for k, v := range values {
		if k == "context_mode" {
			continue
		}
		handler, ok := SettingHandlerRegistry[k]
		if !ok {
			if !channel.IsKnownNonRuntimeKey(k) {
				log.WithField("key", k).WithField("value", v).
					Warn("ApplyRuntimeSettings: unhandled setting key, ignoring")
			}
			continue
		}
		applyHandler(cfg, ag, senderID, "", handler, v)
	}
	if v, ok := values["context_mode"]; ok && v != "" {
		handler := SettingHandlerRegistry["context_mode"]
		applyHandler(cfg, ag, senderID, "", handler, v)
	}
	if ag != nil {
		ag.LLMFactory().SetModelTiers(cfg.LLM)
	}
}

// MissingSettingHandlerKeys returns keys from channel.CLIRuntimeSettingKeys
// that are missing from SettingHandlerRegistry.
func MissingSettingHandlerKeys() []string {
	return channel.MissingRegistryKeys(SettingHandlerRegistry)
}

func applyHandler(cfg *config.Config, ag *Agent, senderID, chatID string, h SettingHandler, value string) {
	if h.ApplyFull != nil {
		h.ApplyFull(cfg, ag, senderID, value)
	} else {
		if h.ApplyConfig != nil {
			h.ApplyConfig(cfg, value)
		}
		if h.ApplyAgent != nil {
			h.ApplyAgent(ag, senderID, chatID, value)
		}
	}
}
