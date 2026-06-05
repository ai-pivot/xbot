package agent

import (
	"xbot/config"
	"xbot/protocol"
)

// RPC method name constants. Used by both Backend (client) and rpc_table (server)
// to ensure method name consistency. Any typo is caught at compile time.
const (
	MethodGetSettings                  = "get_settings"
	MethodSetSetting                   = "set_setting"
	MethodSendInbound                  = "send_inbound"
	MethodSetCWD                       = "set_cwd"
	MethodSetContextMode               = "set_context_mode"
	MethodGetContextMode               = "get_context_mode"
	MethodSetUserModel                 = "set_user_model"
	MethodSwitchModel                  = "switch_model"
	MethodSetUserMaxContext            = "set_user_max_context"
	MethodGetUserMaxContext            = "get_user_max_context"
	MethodSetUserMaxOutputTokens       = "set_user_max_output_tokens"
	MethodGetUserMaxOutputTokens       = "get_user_max_output_tokens"
	MethodSetUserThinkingMode          = "set_user_thinking_mode"
	MethodGetUserThinkingMode          = "get_user_thinking_mode"
	MethodSetLLMConcurrency            = "set_llm_concurrency"
	MethodGetLLMConcurrency            = "get_llm_concurrency"
	MethodClearProxyLLM                = "clear_proxy_llm"
	MethodSetDefaultThinkingMode       = "set_default_thinking_mode"
	MethodGetDefaultModel              = "get_default_model"
	MethodListModels                   = "list_models"
	MethodListAllModels                = "list_all_models"
	MethodSetModelTiers                = "set_model_tiers"
	MethodSetModelContexts             = "set_model_contexts"
	MethodSetGlobalMaxTokens           = "set_global_max_tokens"
	MethodSetRetryConfig               = "set_retry_config"
	MethodSetChatLLM                   = "set_chat_llm"
	MethodClearMemory                  = "clear_memory"
	MethodGetMemoryStats               = "get_memory_stats"
	MethodGetUserTokenUsage            = "get_user_token_usage"
	MethodGetDailyTokenUsage           = "get_daily_token_usage"
	MethodGetBgTaskCount               = "get_bg_task_count"
	MethodListBgTasks                  = "list_bg_tasks"
	MethodKillBgTask                   = "kill_bg_task"
	MethodCleanupCompletedBgTasks      = "cleanup_completed_bg_tasks"
	MethodListSubscriptions            = "list_subscriptions"
	MethodGetDefaultSubscription       = "get_default_subscription"
	MethodAddSubscription              = "add_subscription"
	MethodRemoveSubscription           = "remove_subscription"
	MethodSetDefaultSubscription       = "set_default_subscription"
	MethodRenameSubscription           = "rename_subscription"
	MethodUpdateSubscription           = "update_subscription"
	MethodUpdatePerModelConfig         = "update_per_model_config"
	MethodSetSubscriptionModel         = "set_subscription_model"
	MethodGetSessionSubscription       = "get_session_subscription"
	MethodGetHistory                   = "get_history"
	MethodGetTokenState                = "get_token_state"
	MethodTrimHistory                  = "trim_history"
	MethodResetTokenState              = "reset_token_state"
	MethodGetChannelConfig             = "get_channel_config"
	MethodSetChannelConfig             = "set_channel_config"
	MethodIsProcessing                 = "is_processing"
	MethodGetActiveProgress            = "get_active_progress"
	MethodGetTodos                     = "get_todos"
	MethodCountInteractiveSessions     = "count_interactive_sessions"
	MethodListInteractiveSessions      = "list_interactive_sessions"
	MethodInspectInteractiveSession    = "inspect_interactive_session"
	MethodGetSessionMessages           = "get_session_messages"
	MethodGetAgentSessionDump          = "get_agent_session_dump"
	MethodGetAgentSessionDumpByFullKey = "get_agent_session_dump_by_full_key"
	MethodListTenants                  = "list_tenants"
	MethodGetEffectiveMaxContext       = "get_effective_max_context"
	MethodClearPerChatMaxContext       = "clear_per_chat_max_context"
	MethodSetMaxIterations             = "set_max_iterations"
	MethodSetMaxConcurrency            = "set_max_concurrency"
	MethodSetMaxContextTokens          = "set_max_context_tokens"
	MethodSetCompressionThreshold      = "set_compression_threshold"
	MethodApplyRuntimeSettings         = "apply_runtime_settings"
)

// --- Settings ---

type getSettingsReq struct {
	Namespace string `json:"namespace"`
	SenderID  string `json:"sender_id"`
}

type setSettingReq struct {
	Namespace string `json:"namespace"`
	SenderID  string `json:"sender_id"`
	Key       string `json:"key"`
	Value     string `json:"value"`
}

// --- CWD ---

type setCWDReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	Dir     string `json:"dir"`
}

// --- Context / Runtime ---

type setContextModeReq struct {
	Mode string `json:"mode"`
}

type setMaxIterationsReq struct {
	N int `json:"n"`
}

type setMaxConcurrencyReq struct {
	N int `json:"n"`
}

type setCompressionThresholdReq struct {
	Threshold float64 `json:"threshold"`
}

// --- User Model / LLM ---

type setUserModelReq struct {
	SenderID string `json:"sender_id"`
	Model    string `json:"model"`
}

type switchModelReq struct {
	SenderID string `json:"sender_id"`
	Model    string `json:"model"`
	ChatID   string `json:"chat_id,omitempty"`
}

type setUserMaxContextReq struct {
	SenderID   string `json:"sender_id"`
	MaxContext int    `json:"max_context"`
}

type setUserMaxOutputTokensReq struct {
	SenderID  string `json:"sender_id"`
	MaxTokens int    `json:"max_tokens"`
}

type setUserThinkingModeReq struct {
	SenderID string `json:"sender_id"`
	Mode     string `json:"mode"`
}

type setLLMConcurrencyReq struct {
	SenderID string `json:"sender_id"`
	Personal int    `json:"personal"`
}

type setDefaultThinkingModeReq struct {
	Mode string `json:"mode"`
}

type clearProxyLLMReq struct {
	SenderID string `json:"sender_id"`
}

// --- LLMFactory Settings ---

type setGlobalMaxTokensReq struct {
	MaxTokens int `json:"max_tokens"`
}

type setChatLLMReq struct {
	SenderID string           `json:"sender_id,omitempty"`
	ChatID   string           `json:"chat_id"`
	Provider string           `json:"provider"`
	Config   config.LLMConfig `json:"config"`
}

// --- Settings (RPC) ---

type getUserMaxContextReq struct {
	SenderID string `json:"sender_id"`
}

type getUserMaxOutputTokensReq struct {
	SenderID string `json:"sender_id"`
}

type getUserThinkingModeReq struct {
	SenderID string `json:"sender_id"`
}

type getLLMConcurrencyReq struct {
	SenderID string `json:"sender_id"`
}

// --- Memory ---

type clearMemoryReq struct {
	Channel    string `json:"channel"`
	ChatID     string `json:"chat_id"`
	TargetType string `json:"target_type"`
	SenderID   string `json:"sender_id"`
}

type getMemoryStatsReq struct {
	Channel  string `json:"channel"`
	ChatID   string `json:"chat_id"`
	SenderID string `json:"sender_id"`
}

// --- Token Usage ---

type getUserTokenUsageReq struct {
	SenderID string `json:"sender_id"`
}

type getDailyTokenUsageReq struct {
	SenderID string `json:"sender_id"`
	Days     int    `json:"days"`
}

// --- Background Tasks ---

type getBgTaskCountReq struct {
	SessionKey string `json:"session_key"`
}

type listBgTasksReq struct {
	SessionKey string `json:"session_key"`
}

type killBgTaskReq struct {
	TaskID string `json:"task_id"`
}

type cleanupCompletedBgTasksReq struct {
	SessionKey string `json:"session_key"`
}

// --- Subscriptions ---

type listSubscriptionsReq struct {
	SenderID string `json:"sender_id"`
}

type getDefaultSubscriptionReq struct {
	SenderID string `json:"sender_id"`
}

type addSubscriptionReq struct {
	SenderID string                  `json:"sender_id"`
	Sub      channelSubscriptionJSON `json:"sub"`
}

type removeSubscriptionReq struct {
	ID string `json:"id"`
}

type setDefaultSubscriptionReq struct {
	ID     string `json:"id"`
	ChatID string `json:"chat_id"`
}

type renameSubscriptionReq struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type updateSubscriptionReq struct {
	ID  string                  `json:"id"`
	Sub channelSubscriptionJSON `json:"sub"`
}

type updatePerModelConfigReq struct {
	ID     string                  `json:"id"`
	Model  string                  `json:"model"`
	Config protocol.PerModelConfig `json:"config"`
}

type setSubscriptionModelReq struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

// channelSubscriptionJSON mirrors protocol.Subscription for JSON transport.
type channelSubscriptionJSON struct {
	ID              string                        `json:"id"`
	Name            string                        `json:"name"`
	Provider        string                        `json:"provider"`
	BaseURL         string                        `json:"base_url"`
	APIKey          string                        `json:"api_key"`
	Model           string                        `json:"model"`
	Active          bool                          `json:"active"`
	MaxOutputTokens int                           `json:"max_output_tokens"`
	ThinkingMode    string                        `json:"thinking_mode"`
	PerModelConfigs map[string]perModelConfigJSON `json:"per_model_configs,omitempty"`
}

type perModelConfigJSON = protocol.PerModelConfig

// --- History ---

type getHistoryReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

type getTokenStateReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

type trimHistoryReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
	Cutoff  int64  `json:"cutoff"` // unix timestamp
}

// --- Channel Config ---

type setChannelConfigReq struct {
	Channel string            `json:"channel"`
	Values  map[string]string `json:"values"`
}

// --- Progress ---

type isProcessingReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

type getActiveProgressReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

type getTodosReq struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// --- SubAgent Sessions ---

type countInteractiveSessionsReq struct {
	ChannelName string `json:"channel"`
	ChatID      string `json:"chat_id"`
}

type listInteractiveSessionsReq struct {
	ChannelName string `json:"channel"`
	ChatID      string `json:"chat_id"`
}

type inspectInteractiveSessionReq struct {
	RoleName    string `json:"role"`
	ChannelName string `json:"channel"`
	ChatID      string `json:"chat_id"`
	Instance    string `json:"instance"`
	TailCount   int    `json:"tail_count"`
}

type getSessionMessagesReq struct {
	ChannelName string `json:"channel"`
	ChatID      string `json:"chat_id"`
	RoleName    string `json:"role"`
	Instance    string `json:"instance"`
}

type getAgentSessionDumpReq struct {
	ChannelName string `json:"channel"`
	ChatID      string `json:"chat_id"`
	RoleName    string `json:"role"`
	Instance    string `json:"instance"`
}

type getAgentSessionDumpByFullKeyReq struct {
	FullKey string `json:"full_key"`
}

type getEffectiveMaxContextReq struct {
	SenderID string `json:"sender_id"`
	ChatID   string `json:"chat_id"`
}

type clearPerChatMaxContextReq struct {
	ChatID string `json:"chat_id"`
}

type applyRuntimeSettingsReq struct {
	Values map[string]string `json:"values"`
}

// --- DirectSend / Channel ---
