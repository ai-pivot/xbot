package channel

import "sync"

// LocaleSchemaProvider provides settings schema from the i18n system.
// Registered by the CLI package at init time. Feishu and other channels
// use this to get localized settings schema.
var (
	localeSchemaMu     sync.RWMutex
	localeSchemaGetter func() []SettingDefinition
)

// RegisterLocaleSchemaGetter registers the function that returns the current
// locale's settings schema. Called by CLI at startup.
func RegisterLocaleSchemaGetter(fn func() []SettingDefinition) {
	localeSchemaMu.Lock()
	defer localeSchemaMu.Unlock()
	localeSchemaGetter = fn
}

// GetLocaleSettingsSchema returns the settings schema from the current locale.
// Returns nil if no provider is registered.
func GetLocaleSettingsSchema() []SettingDefinition {
	localeSchemaMu.RLock()
	defer localeSchemaMu.RUnlock()
	if localeSchemaGetter == nil {
		return nil
	}
	return localeSchemaGetter()
}
