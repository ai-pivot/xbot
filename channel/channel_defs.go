package channel

// channel_defs.go — the settings schema of the BUILT-IN channels.
//
// Single source of truth: the CLI settings panel and the Web channels panel
// both render from these definitions, so a new built-in channel field only has
// to be added once. Plugin (user-registered) channels carry their own schema in
// ChannelProvider.ConfigSchema() and are NOT listed here.

// BuiltinChannelNames lists the built-in channels in display order.
var BuiltinChannelNames = []string{"web", "feishu", "qq", "napcat"}

// BuiltinChannelSchema returns the settings schema for a built-in channel, or
// nil when the name is not a built-in channel (i.e. a plugin provider).
func BuiltinChannelSchema(name string) []SettingDefinition {
	switch name {
	case "web":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable Web channel", Type: SettingTypeToggle, Category: "Web channel", DefaultValue: "false"},
			{Key: "host", Label: "Host", Description: "Listen host (e.g. 0.0.0.0)", Type: SettingTypeText, Category: "Web channel", DefaultValue: "0.0.0.0"},
			{Key: "port", Label: "Port", Description: "Listen port (e.g. 8080)", Type: SettingTypeText, Category: "Web channel", DefaultValue: "8080"},
		}
	case "feishu":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable Feishu channel", Type: SettingTypeToggle, Category: "Feishu (飞书)", DefaultValue: "false"},
			{Key: "app_id", Label: "App ID", Description: "Feishu app ID", Type: SettingTypeText, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "app_secret", Label: "App Secret", Description: "Feishu app secret", Type: SettingTypePassword, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "encrypt_key", Label: "Encrypt Key", Description: "Feishu event encrypt key", Type: SettingTypePassword, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "verification_token", Label: "Verification Token", Description: "Feishu event verification token", Type: SettingTypeText, Category: "Feishu (飞书)", DefaultValue: ""},
			{Key: "domain", Label: "Domain", Description: "Custom Feishu API domain (optional)", Type: SettingTypeText, Category: "Feishu (飞书)", DefaultValue: ""},
		}
	case "qq":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable QQ channel", Type: SettingTypeToggle, Category: "QQ", DefaultValue: "false"},
			{Key: "app_id", Label: "App ID", Description: "QQ Bot AppID", Type: SettingTypeText, Category: "QQ", DefaultValue: ""},
			{Key: "client_secret", Label: "Client Secret", Description: "QQ Bot client secret", Type: SettingTypePassword, Category: "QQ", DefaultValue: ""},
		}
	case "napcat":
		return []SettingDefinition{
			{Key: "enabled", Label: "Enabled", Description: "Enable NapCat channel", Type: SettingTypeToggle, Category: "NapCat", DefaultValue: "false"},
			{Key: "ws_url", Label: "WebSocket URL", Description: "NapCat WebSocket URL", Type: SettingTypeText, Category: "NapCat", DefaultValue: ""},
			{Key: "token", Label: "Token", Description: "NapCat access token", Type: SettingTypePassword, Category: "NapCat", DefaultValue: ""},
		}
	default:
		return nil
	}
}
