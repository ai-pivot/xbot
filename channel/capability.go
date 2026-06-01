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

	// DependsOnKey makes this field conditionally visible based on another field's value.
	// When set, this field is only shown when the field named by DependsOnKey has a value
	// matching one of the comma-separated values in DependsOnValues.
	// Empty string means "always show" (default).
	DependsOnKey    string `json:"depends_on_key,omitempty"`
	DependsOnValues string `json:"depends_on_values,omitempty"` // comma-separated trigger values
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

// IsFieldVisible returns whether a setting field should be shown given current values.
// Fields without DependsOnKey are always visible.
// Fields with DependsOnKey are visible when the dependent field's value matches one
// of the comma-separated DependsOnValues.
func IsFieldVisible(def SettingDefinition, values map[string]string) bool {
	if def.DependsOnKey == "" {
		return true
	}
	currentVal := values[def.DependsOnKey]
	for _, trigger := range strings.Split(def.DependsOnValues, ",") {
		if strings.TrimSpace(trigger) == currentVal {
			return true
		}
	}
	return false
}

// ProviderDefaultURLs maps provider identifiers to their default API base URLs.
// Used by the settings panel to auto-fill llm_base_url when the user selects a provider.
// Azure and custom are omitted because their URLs are user-specific.
// Coding plan / token plan variants use a "_coding" suffix.
var ProviderDefaultURLs = map[string]string{
	"openai":        "https://api.openai.com/v1",
	"anthropic":     "https://api.anthropic.com",
	"openrouter":    "https://openrouter.ai/api/v1",
	"ollama":        "http://localhost:11434/v1",
	"google":        "https://generativelanguage.googleapis.com/v1beta/openai",
	"deepseek":      "https://api.deepseek.com",
	"moonshot":      "https://api.moonshot.cn/v1",
	"zhipu":         "https://open.bigmodel.cn/api/paas/v4",
	"zhipu_coding":  "https://open.bigmodel.cn/api/coding/paas/v4",
	"siliconflow":   "https://api.siliconflow.cn/v1",
	"xiaomi":        "https://token-plan-cn.xiaomimimo.com/v1",
	"xiaomi_coding": "https://token-plan-cn.xiaomimimo.com/v1", // same URL, tp- prefix key
}

// ProviderRecommendedModels maps provider identifiers to their recommended default model.
// Used by the setup panel to auto-fill llm_model when the user selects a provider.
var ProviderRecommendedModels = map[string]string{
	"openai":        "gpt-4o",
	"anthropic":     "claude-sonnet-4-20250514",
	"openrouter":    "anthropic/claude-sonnet-4-20250514",
	"google":        "gemini-2.5-pro",
	"deepseek":      "deepseek-chat",
	"moonshot":      "kimi-k2.6",
	"zhipu":         "glm-4-flash",
	"zhipu_coding":  "glm-4.5-air",
	"siliconflow":   "deepseek-ai/DeepSeek-V3",
	"ollama":        "qwen3:8b",
	"xiaomi":        "mimo-v2.5-pro",
	"xiaomi_coding": "mimo-v2.5-pro",
}

// ProviderSetupGuide holds instructions for obtaining an API key from a provider.
type ProviderSetupGuide struct {
	// URL is the direct link to the provider's API key management page.
	// Rendered as an OSC 8 clickable hyperlink in the terminal.
	URL string
	// Hint is a short instruction shown below the API Key field.
	// May contain an OSC 8 hyperlink placeholder {{URL}} that gets replaced at render time.
	Hint string
}

// ProviderSetupGuides maps provider identifiers to their API key acquisition guides.
// When the user selects a provider in the setup panel, the API Key field's description
// is dynamically updated to include the guide hint with a clickable link.
var ProviderSetupGuides = map[string]ProviderSetupGuide{
	"openai": {
		URL:  "https://platform.openai.com/api-keys",
		Hint: "👉 打开上面的链接 → 登录 → Create new secret key → 复制密钥",
	},
	"anthropic": {
		URL:  "https://console.anthropic.com/settings/keys",
		Hint: "👉 打开上面的链接 → 登录 → Create Key → 复制密钥",
	},
	"openrouter": {
		URL:  "https://openrouter.ai/settings/keys",
		Hint: "👉 打开上面的链接 → 登录 → Create Key → 复制密钥",
	},
	"google": {
		URL:  "https://aistudio.google.com/apikey",
		Hint: "👉 打开上面的链接 → 登录 → Create API Key → 复制密钥",
	},
	"deepseek": {
		URL:  "https://platform.deepseek.com/api_keys",
		Hint: "👉 打开上面的链接 → 登录 → 创建 API Key → 复制密钥",
	},
	"zhipu": {
		URL:  "https://open.bigmodel.cn/usercenter/apikeys",
		Hint: "👉 打开上面的链接 → 登录 → 添加 API Key → 复制密钥",
	},
	"zhipu_coding": {
		URL:  "https://bigmodel.cn/apikey/platform",
		Hint: "👉 打开上面的链接 → 登录 → 创建 API Key（sk-sp- 开头的是 Coding Plan 专用密钥）",
	},
	"siliconflow": {
		URL:  "https://cloud.siliconflow.cn/account/ak",
		Hint: "👉 打开上面的链接 → 登录 → 添加 API Key → 复制密钥",
	},
	"moonshot": {
		URL:  "https://platform.moonshot.cn/console/api-keys",
		Hint: "👉 打开上面的链接 → 登录 → 创建 API Key → 复制密钥",
	},
	"xiaomi": {
		URL:  "https://mimo.mi.com",
		Hint: "👉 打开上面的链接 → 注册/登录 → 获取 Token Plan 密钥",
	},
	"ollama": {
		URL:  "",
		Hint: "✅ 不需要密钥！只需先安装 Ollama（ollama.com）并运行模型",
	},
}

// FormatProviderHint returns the full hint string for a provider, including
// an OSC 8 clickable hyperlink if a URL is available.
// The link text is the URL itself, followed by the hint instruction.
func FormatProviderHint(provider string) string {
	guide, ok := ProviderSetupGuides[provider]
	if !ok {
		return ""
	}
	if guide.URL == "" {
		return guide.Hint
	}
	// OSC 8 hyperlink: \x1b]8;;URL\x1b\\TEXT\x1b]8;;\x1b\\
	link := fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", guide.URL, guide.URL)
	return link + "\n" + guide.Hint
}

// ProviderIsCodingPlan returns true if the provider value represents a coding plan variant.
func ProviderIsCodingPlan(provider string) bool {
	return strings.HasSuffix(provider, "_coding")
}

// ProviderBaseProvider returns the base provider (without _coding suffix).
// For example, "deepseek_coding" → "deepseek".
func ProviderBaseProvider(provider string) string {
	return strings.TrimSuffix(provider, "_coding")
}

// SettingOption defines an option for select-type settings.
type SettingOption struct {
	Label       string `json:"label"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"` // optional subtitle/hint shown below the label
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
