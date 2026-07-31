package sqlite

import (
	"time"
)

// UserLLMConfig 用户 LLM 配置
type UserLLMConfig struct {
	ID              string         // subscription ID (for precise UPDATE targeting)
	SenderID        string         // 用户 ID
	Provider        string         // LLM 提供商: "openai", "deepseek", "anthropic" 等
	BaseURL         string         // API Base URL
	APIKey          string         // API Key
	Model           string         // 默认模型
	MaxContext      int            // 最大上下文 token 数（0 表示不限制）
	MaxOutputTokens int            // 最大输出 token 数（0 表示使用默认值 8192）
	ThinkingMode    string         // 思考模式: "" (自动), "enabled", "disabled"
	APIType         string         // API type: "" (default=chat_completions), "responses"
	OnModelsLoaded  func([]string) // callback after models loaded from API
	CreatedAt       time.Time      // 创建时间
	UpdatedAt       time.Time      // 更新时间
}

// NOTE: The legacy UserLLMConfigService (which read/wrote the user_llm_configs
// table, then user_llm_subscriptions) was removed in the model-first refactor.
// All access now goes through LLMSubscriptionService. This file retains only
// the UserLLMConfig struct, used as a client-construction bag by the factory
// and as a parse bag by the /set-llm handler.
