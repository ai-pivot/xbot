package serverapp

import (
	"xbot/agent"
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
func applyRuntimeSetting(cfg *config.Config, backend agent.AgentBackend, senderID, key, value string) {
	agent.ApplyRuntimeSetting(cfg, backend, senderID, key, value)
	_ = saveServerConfig(cfg)
}

// applyRuntimeSettings applies a batch of setting changes and saves server config.
func applyRuntimeSettings(cfg *config.Config, backend agent.AgentBackend, senderID string, values map[string]string) {
	agent.ApplyRuntimeSettings(cfg, backend, senderID, values)
	_ = saveServerConfig(cfg)
}

// missingHandlerKeys returns keys from channel.CLIRuntimeSettingKeys
// that are missing from agent.SettingHandlerRegistry.
func missingHandlerKeys() []string {
	return agent.MissingSettingHandlerKeys()
}
