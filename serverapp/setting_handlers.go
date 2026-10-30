package serverapp

import (
	"xbot/agent"
	"xbot/channel"
	"xbot/config"
)

// setting_handlers.go — thin wrapper around agent.SettingHandlerRegistry.
// Serverapp only adds server-specific config persistence (saveServerConfig).
//
// To add a new runtime setting:
//  1. Add the key to channel.CLIRuntimeSettingKeys
//  2. Add a handler in agent/setting_runtime.go
//  3. Done.

// applyRuntimeSetting applies a single setting change and saves server config.
func applyRuntimeSetting(cfg *config.Config, ag *agent.Agent, senderID, key, value string) {
	agent.ApplyRuntimeSetting(cfg, ag, senderID, key, value)
	_ = saveServerConfig(cfg)
}

// maskSensitiveSettings replaces configured sensitive values with the masked
// placeholder before a settings snapshot leaves the server. The settings panels
// round-trip every visible value on save, so the placeholder must mean
// "configured but unchanged" — never a credential to persist (set_setting
// rejects masked values for sensitive keys).
func maskSensitiveSettings(settings map[string]string, configFallbackKey, configFallbackValue string) {
	for k, v := range settings {
		if v != "" && channel.IsSensitiveSettingKey(k) {
			settings[k] = channel.MaskedSettingValue
		}
	}
	// Keys with a config.json fallback (tavily_api_key) must surface as
	// "configured" too, without exposing the file value.
	if _, ok := settings[configFallbackKey]; !ok && configFallbackValue != "" {
		settings[configFallbackKey] = channel.MaskedSettingValue
	}
}

// applyRuntimeSettings applies a batch of setting changes and saves server config.
func applyRuntimeSettings(cfg *config.Config, ag *agent.Agent, senderID string, values map[string]string) {
	agent.ApplyRuntimeSettings(cfg, ag, senderID, values)
	_ = saveServerConfig(cfg)
}

// missingHandlerKeys returns keys from channel.CLIRuntimeSettingKeys
// that are missing from agent.SettingHandlerRegistry.
func missingHandlerKeys() []string {
	return agent.MissingSettingHandlerKeys()
}
