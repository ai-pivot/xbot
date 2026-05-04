package agent

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

type setSandboxReq struct {
	Mode string `json:"mode"`
}

// --- User Model / LLM ---

type setUserModelReq struct {
	SenderID string `json:"sender_id"`
	Model    string `json:"model"`
}

type switchModelReq struct {
	SenderID string `json:"sender_id"`
	Model    string `json:"model"`
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

type setProxyLLMReq struct {
	SenderID string `json:"sender_id"`
	Model    string `json:"model"`
}

type setDefaultThinkingModeReq struct {
	Mode string `json:"mode"`
}

type clearProxyLLMReq struct {
	SenderID string `json:"sender_id"`
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

type setSubscriptionModelReq struct {
	ID    string `json:"id"`
	Model string `json:"model"`
}

// channelSubscriptionJSON mirrors channel.Subscription for JSON transport.
type channelSubscriptionJSON struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	BaseURL         string `json:"base_url"`
	APIKey          string `json:"api_key"`
	Model           string `json:"model"`
	Active          bool   `json:"active"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	ThinkingMode    string `json:"thinking_mode"`
}

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

// --- SubAgent Sessions ---

type countInteractiveSessionsReq struct {
	ChannelName string `json:"channel_name"`
	ChatID      string `json:"chat_id"`
}

type listInteractiveSessionsReq struct {
	ChannelName string `json:"channel_name"`
	ChatID      string `json:"chat_id"`
}

type inspectInteractiveSessionReq struct {
	RoleName    string `json:"role_name"`
	ChannelName string `json:"channel_name"`
	ChatID      string `json:"chat_id"`
	Instance    string `json:"instance"`
	TailCount   int    `json:"tail_count"`
}

type getSessionMessagesReq struct {
	ChannelName string `json:"channel_name"`
	ChatID      string `json:"chat_id"`
	RoleName    string `json:"role_name"`
	Instance    string `json:"instance"`
}

type getAgentSessionDumpReq struct {
	ChannelName string `json:"channel_name"`
	ChatID      string `json:"chat_id"`
	RoleName    string `json:"role_name"`
	Instance    string `json:"instance"`
}

type getAgentSessionDumpByFullKeyReq struct {
	FullKey string `json:"full_key"`
}

// --- DirectSend / Channel ---
