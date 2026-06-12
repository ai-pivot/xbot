package cli

import (
	"fmt"

	"xbot/channel"
)

// Re-export shared types from channel package for backward compatibility
// within the cli sub-package. This avoids modifying every file that references
// these types.

type (
	InboundMsg          = channel.InboundMsg
	OutboundMsg         = channel.OutboundMsg
	BgTaskStatus        = channel.BgTaskStatus
	BgTask              = channel.BgTask
	UserTokenUsage      = channel.UserTokenUsage
	DailyTokenUsage     = channel.DailyTokenUsage
	AgentPanelEntry     = channel.AgentPanelEntry
	SessionPanelEntry   = channel.SessionPanelEntry
	SessionChatMessage  = channel.SessionChatMessage
	Subscription        = channel.Subscription
	PerModelConfig      = channel.PerModelConfig
	SubscriptionManager = channel.SubscriptionManager
	LLMSubscriber       = channel.LLMSubscriber
	HistoryIteration    = channel.HistoryIteration
	HistoryMessage      = channel.HistoryMessage
	SettingDefinition   = channel.SettingDefinition
	SettingOption       = channel.SettingOption
	SettingType         = channel.SettingType
	Channel             = channel.Channel
	ProgressSender      = channel.ProgressSender
	UserMessageInjector = channel.UserMessageInjector
	SessionStateSender  = channel.SessionStateSender
	ProviderSetupGuide  = channel.ProviderSetupGuide
)

const (
	MetadataReplyPolicy = channel.MetadataReplyPolicy
	ReplyPolicyOptional = channel.ReplyPolicyOptional

	BgTaskRunning = channel.BgTaskRunning
	BgTaskDone    = channel.BgTaskDone
	BgTaskError   = channel.BgTaskError
	BgTaskKilled  = channel.BgTaskKilled

	SettingTypeText     = channel.SettingTypeText
	SettingTypePassword = channel.SettingTypePassword
	SettingTypeNumber   = channel.SettingTypeNumber
	SettingTypeToggle   = channel.SettingTypeToggle
	SettingTypeSelect   = channel.SettingTypeSelect
	SettingTypeCombo    = channel.SettingTypeCombo
	SettingTypeTextarea = channel.SettingTypeTextarea
)

var (
	ProviderSetupGuides       = channel.ProviderSetupGuides
	ProviderDefaultURLs       = channel.ProviderDefaultURLs
	ProviderRecommendedModels = channel.ProviderRecommendedModels
	ConvertMessagesToHistory  = channel.ConvertMessagesToHistory
	ConvertFeishuCard         = channel.ConvertFeishuCard
	BuildTextSettingsUI       = channel.BuildTextSettingsUI
)

// Re-export functions
func IsProviderDefaultURL(url string) bool {
	return channel.IsProviderDefaultURL(url)
}

func IsFieldVisible(def channel.SettingDefinition, values map[string]string) bool {
	return channel.IsFieldVisible(def, values)
}

func IsGlobalScopedSettingKey(key string) bool {
	return channel.IsGlobalScopedSettingKey(key)
}

func IsSubscriptionScopedSettingKey(key string) bool {
	return channel.IsSubscriptionScopedSettingKey(key)
}

func IsUserScopedSettingKey(key string) bool {
	return channel.IsUserScopedSettingKey(key)
}

func SettingScopeOf(key string) string {
	return channel.SettingScopeOf(key)
}

// FormatProviderHint returns the full hint string for a provider, including
// an OSC 8 clickable hyperlink if a URL is available.
// The hint text is looked up from the locale's ProviderHints map.
func FormatProviderHint(provider string, locale *UILocale) string {
	guide, ok := ProviderSetupGuides[provider]
	if !ok {
		return ""
	}
	hint := ""
	if locale != nil && locale.ProviderHints != nil {
		hint = locale.ProviderHints[guide.HintKey]
	}
	if hint == "" {
		return ""
	}
	if guide.URL == "" {
		return hint
	}
	// OSC 8 hyperlink: \x1b]8;;URL\x1b\\TEXT\x1b]8;;\x1b\\
	link := fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", guide.URL, guide.URL)
	return link + "\n" + hint
}

// openBrowser opens a URL in the default browser.
// Copied from web/browser.go since it's unexported.
func openBrowser(url string) error {
	return channel.OpenBrowser(url)
}

// Test-only types: re-exported for white-box tests
type (
	IterSnapshot = channel.IterSnapshot
	IterToolSnap = channel.IterToolSnap
)
