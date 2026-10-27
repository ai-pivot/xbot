package sqlite

import (
	"xbot/storage/common"
)

// UserLLMConfig 用户 LLM 配置
type UserLLMConfig = common.UserLLMConfig

// NOTE: The legacy UserLLMConfigService (which read/wrote the user_llm_configs
// table, then user_llm_subscriptions) was removed in the model-first refactor.
// All access now goes through LLMSubscriptionService. This file retains only
// the UserLLMConfig struct, used as a client-construction bag by the factory
// and as a parse bag by the /set-llm handler.
