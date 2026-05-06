package channel

import (
	"context"
	"fmt"
	"strings"
)

// SettingsCapability is implemented by channels that support user-configurable settings.
type SettingsCapability interface {
	SettingsSchema() []SettingDefinition
	HandleSettingSubmit(ctx context.Context, rawInput string) (map[string]string, error)
}

// SettingDefinition describes a single configurable setting.
type SettingDefinition struct {
	Key          string          `json:"key"`
	Label        string          `json:"label"`
	Description  string          `json:"description"`
	Type         SettingType     `json:"type"`
	Options      []SettingOption `json:"options,omitempty"`
	DefaultValue string          `json:"default_value,omitempty"`
	Category     string          `json:"category"`
	ReadOnly     bool            `json:"read_only,omitempty"` // if true, display-only (not editable by user)
}

// SettingType defines the type of a setting.
type SettingType string

const (
	SettingTypeText     SettingType = "text"
	SettingTypeNumber   SettingType = "number"
	SettingTypeSelect   SettingType = "select"
	SettingTypeToggle   SettingType = "toggle"
	SettingTypeTextarea SettingType = "textarea"
	SettingTypeCombo    SettingType = "combo"    // selectable text with options + free input
	SettingTypePassword SettingType = "password" // password field (masked display)
)

// ProviderDefaultURLs maps provider identifiers to their default API base URLs.
// Used by the settings panel to auto-fill llm_base_url when the user selects a provider.
// Azure and custom are omitted because their URLs are user-specific.
var ProviderDefaultURLs = map[string]string{
	"openai":     "https://api.openai.com/v1",
	"anthropic":  "https://api.anthropic.com",
	"openrouter": "https://openrouter.ai/api/v1",
	"ollama":     "http://localhost:11434/v1",
	"google":     "https://generativelanguage.googleapis.com/v1beta/openai",
}

// IsProviderDefaultURL checks whether a URL matches any known provider default,
// indicating it was auto-filled rather than user-customized.
func IsProviderDefaultURL(url string) bool {
	for _, v := range ProviderDefaultURLs {
		if v == url {
			return true
		}
	}
	return false
}

// SettingOption defines an option for select-type settings.
type SettingOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// UIBuilder is implemented by channels that can render interactive UI.
type UIBuilder interface {
	BuildSettingsUI(ctx context.Context, schema []SettingDefinition, currentValues map[string]string) string
	BuildProgressUI(ctx context.Context, progress any) string
}

// StreamRenderer is implemented by channels that support real-time stream rendering.
// When a channel implements this interface AND enable_stream=true in settings,
// the agent pushes content deltas as IsPartial messages during LLM streaming.
type StreamRenderer interface {
	// SupportsStreamRender returns true if the channel can render stream content in real-time.
	SupportsStreamRender() bool
}

// BuildTextSettingsUI builds a Markdown-formatted text representation of settings.
// Used as fallback for channels that don't implement UIBuilder.
func BuildTextSettingsUI(schema []SettingDefinition, currentValues map[string]string) string {
	if len(schema) == 0 {
		return "没有可配置的设置项。"
	}

	var sb strings.Builder
	sb.WriteString("# ⚙️ 设置\n\n")

	// Group by category
	categories := make(map[string][]SettingDefinition)
	for _, def := range schema {
		cat := def.Category
		if cat == "" {
			cat = "通用"
		}
		categories[cat] = append(categories[cat], def)
	}

	for cat, defs := range categories {
		fmt.Fprintf(&sb, "## %s\n\n", cat)
		for _, def := range defs {
			currentValue := ""
			if currentValues != nil {
				if v, ok := currentValues[def.Key]; ok {
					currentValue = v
				}
			}
			if currentValue == "" {
				currentValue = def.DefaultValue
			}

			fmt.Fprintf(&sb, "**%s**", def.Label)
			if currentValue != "" {
				fmt.Fprintf(&sb, " — `%s`", currentValue)
			}
			sb.WriteString("\n")

			if def.Description != "" {
				fmt.Fprintf(&sb, "%s\n", def.Description)
			}

			if def.Type == SettingTypeSelect && len(def.Options) > 0 {
				sb.WriteString("选项: ")
				for i, opt := range def.Options {
					if i > 0 {
						sb.WriteString(", ")
					}
					fmt.Fprintf(&sb, "`%s`", opt.Value)
				}
				sb.WriteString("\n")
			}

			if def.Type == SettingTypeCombo && len(def.Options) > 0 {
				sb.WriteString("可选: ")
				for i, opt := range def.Options {
					if i > 0 {
						sb.WriteString(", ")
					}
					fmt.Fprintf(&sb, "`%s`", opt.Value)
				}
				sb.WriteString("（也可输入自定义值）\n")
			}

			sb.WriteString("\n")
		}
	}

	sb.WriteString("---\n使用 `/settings set <key> <value>` 修改设置。\n")
	return sb.String()
}
