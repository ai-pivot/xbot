// Code generator for agent/client_rpc_generated.go.
//
// Usage:
//
//	go run cmd/genclient/main.go [-output agent/client_rpc_generated.go]
//
// Typically invoked via go:generate in agent/client.go.
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
)

// ---------------------------------------------------------------------------
// Spec types
// ---------------------------------------------------------------------------

type methodSpec struct {
	Section    string // section header (empty = no header)
	Name       string // Go method name
	Params     string // param list, e.g. "senderID, model string"
	Pattern    string // callVoid | callError | callResult | callDirect | custom
	Method     string // RPC method constant, e.g. "MethodGetSettings"
	ReqExpr    string // request expression, e.g. "getSettingsReq{Namespace: namespace, SenderID: senderID}"
	ResultType string // result type for callResult/callDirect, e.g. "map[string]string"
	Body       string // full function body for custom pattern (includes func signature)
}

// ---------------------------------------------------------------------------
// Method specifications — ordered by section
// ---------------------------------------------------------------------------

var spec = []methodSpec{
	// ---- Settings ----
	{Section: "Settings (via RPC)",
		Name: "GetSettings", Params: "namespace, senderID string",
		Pattern: "callResult", Method: "MethodGetSettings",
		ReqExpr:    "getSettingsReq{Namespace: namespace, SenderID: senderID}",
		ResultType: "map[string]string"},
	{Name: "SetSetting", Params: "namespace, senderID, key, value string",
		Pattern: "callError", Method: "MethodSetSetting",
		ReqExpr: "setSettingReq{Namespace: namespace, SenderID: senderID, Key: key, Value: value}"},

	// ---- Model / LLM ----
	{Section: "Model / LLM (via RPC)",
		Name: "GetDefaultModel", Params: "",
		Pattern: "callDirect", Method: "MethodGetDefaultModel",
		ReqExpr: "struct{}{}", ResultType: "string"},
	{Name: "GetContextMode", Params: "",
		Pattern: "callDirect", Method: "MethodGetContextMode",
		ReqExpr: "struct{}{}", ResultType: "string"},
	{Name: "ListModels", Params: "",
		Pattern: "callDirect", Method: "MethodListModels",
		ReqExpr: "struct{}{}", ResultType: "[]string"},
	{Name: "ListAllModels", Params: "",
		Pattern: "callDirect", Method: "MethodListAllModels",
		ReqExpr: "struct{}{}", ResultType: "[]string"},
	{Name: "SetModelTiers", Params: "cfg config.LLMConfig",
		Pattern: "callError", Method: "MethodSetModelTiers",
		ReqExpr: "cfg"},
	{Name: "SetDefaultThinkingMode", Params: "mode string",
		Pattern: "callError", Method: "MethodSetDefaultThinkingMode",
		ReqExpr: "setDefaultThinkingModeReq{Mode: mode}"},
	{Name: "SetModelContexts", Params: "contexts map[string]int",
		Pattern: "callError", Method: "MethodSetModelContexts",
		ReqExpr: "contexts"},
	{Name: "SetGlobalMaxTokens", Params: "maxTokens int",
		Pattern: "callError", Method: "MethodSetGlobalMaxTokens",
		ReqExpr: "setGlobalMaxTokensReq{MaxTokens: maxTokens}"},
	{Name: "SetRetryConfig", Params: "cfg llm.RetryConfig",
		Pattern: "callError", Method: "MethodSetRetryConfig",
		ReqExpr: "cfg"},
	{Name: "SetChatLLM", Params: "chatID, provider string, llmCfg config.LLMConfig",
		Pattern: "callError", Method: "MethodSetChatLLM",
		ReqExpr: "setChatLLMReq{ChatID: chatID, Provider: provider, Config: llmCfg}"},
	{Name: "ClearProxyLLM", Params: "senderID string",
		Pattern: "callVoid", Method: "MethodClearProxyLLM",
		ReqExpr: "clearProxyLLMReq{SenderID: senderID}"},

	// ---- Per-user settings ----
	{Section: "Per-user settings (via RPC)",
		Name: "GetUserMaxContext", Params: "senderID string",
		Pattern: "callDirect", Method: "MethodGetUserMaxContext",
		ReqExpr: "getUserMaxContextReq{SenderID: senderID}", ResultType: "int"},
	{Name: "SetUserMaxContext", Params: "senderID string, maxContext int",
		Pattern: "callError", Method: "MethodSetUserMaxContext",
		ReqExpr: "setUserMaxContextReq{SenderID: senderID, MaxContext: maxContext}"},
	{Name: "GetUserMaxOutputTokens", Params: "senderID string",
		Pattern: "callDirect", Method: "MethodGetUserMaxOutputTokens",
		ReqExpr: "getUserMaxOutputTokensReq{SenderID: senderID}", ResultType: "int"},
	{Name: "SetUserMaxOutputTokens", Params: "senderID string, maxTokens int",
		Pattern: "callError", Method: "MethodSetUserMaxOutputTokens",
		ReqExpr: "setUserMaxOutputTokensReq{SenderID: senderID, MaxTokens: maxTokens}"},
	{Name: "GetUserThinkingMode", Params: "senderID string",
		Pattern: "callDirect", Method: "MethodGetUserThinkingMode",
		ReqExpr: "getUserThinkingModeReq{SenderID: senderID}", ResultType: "string"},
	{Name: "SetUserThinkingMode", Params: "senderID, mode string",
		Pattern: "callError", Method: "MethodSetUserThinkingMode",
		ReqExpr: "setUserThinkingModeReq{SenderID: senderID, Mode: mode}"},
	{Name: "GetLLMConcurrency", Params: "senderID string",
		Pattern: "callDirect", Method: "MethodGetLLMConcurrency",
		ReqExpr: "getLLMConcurrencyReq{SenderID: senderID}", ResultType: "int"},
	{Name: "SetLLMConcurrency", Params: "senderID string, personal int",
		Pattern: "callError", Method: "MethodSetLLMConcurrency",
		ReqExpr: "setLLMConcurrencyReq{SenderID: senderID, Personal: personal}"},
	{Name: "SetUserModel", Params: "senderID, model string",
		Pattern: "callError", Method: "MethodSetUserModel",
		ReqExpr: "setUserModelReq{SenderID: senderID, Model: model}"},
	{Name: "SwitchModel", Params: "senderID, model, chatID string",
		Pattern: "callError", Method: "MethodSwitchModel",
		ReqExpr: "switchModelReq{SenderID: senderID, Model: model, ChatID: chatID}"},

	// ---- Runtime config ----
	{Section: "Runtime config (via RPC)",
		Name: "SetMaxIterations", Params: "n int",
		Pattern: "callVoid", Method: "MethodSetMaxIterations",
		ReqExpr: "setMaxIterationsReq{N: n}"},
	{Name: "SetMaxConcurrency", Params: "n int",
		Pattern: "callVoid", Method: "MethodSetMaxConcurrency",
		ReqExpr: "setMaxConcurrencyReq{N: n}"},
	{Name: "SetMaxContextTokens", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) SetMaxContextTokens(n int, chatID ...string) {",
			"\tchatIDVal := \"\"",
			"\tif len(chatID) > 0 {",
			"\t\tchatIDVal = chatID[0]",
			"\t}",
			"\tc.callVoid(MethodSetMaxContextTokens, struct {",
			"\t\tMaxContext int    `json:\"max_context\"`",
			"\t\tChatID     string `json:\"chat_id,omitempty\"`",
			"\t}{MaxContext: n, ChatID: chatIDVal})",
			"}",
		)},
	{Name: "SetCompressionThreshold", Params: "f float64",
		Pattern: "callVoid", Method: "MethodSetCompressionThreshold",
		ReqExpr: "setCompressionThresholdReq{Threshold: f}"},
	{Name: "ApplyRuntimeSettings", Params: "values map[string]string",
		Pattern: "callVoid", Method: "MethodApplyRuntimeSettings",
		ReqExpr: "applyRuntimeSettingsReq{Values: values}"},
	{Name: "SetContextMode", Params: "mode string",
		Pattern: "callError", Method: "MethodSetContextMode",
		ReqExpr: "setContextModeReq{Mode: mode}"},
	{Name: "SetCWD", Params: "ch, chatID, dir string",
		Pattern: "callError", Method: "MethodSetCWD",
		ReqExpr: "setCWDReq{Channel: ch, ChatID: chatID, Dir: dir}"},
	{Name: "ResetTokenState", Params: "",
		Pattern: "callVoid", Method: "MethodResetTokenState",
		ReqExpr: "struct{}{}"},
	{Name: "GetEffectiveMaxContext", Params: "senderID, chatID string",
		Pattern: "callDirect", Method: "MethodGetEffectiveMaxContext",
		ReqExpr: "getEffectiveMaxContextReq{SenderID: senderID, ChatID: chatID}", ResultType: "int"},
	{Name: "ClearPerChatMaxContext", Params: "chatID string",
		Pattern: "callVoid", Method: "MethodClearPerChatMaxContext",
		ReqExpr: "clearPerChatMaxContextReq{ChatID: chatID}"},

	// ---- Token usage ----
	{Section: "Token usage (via RPC)",
		Name: "GetUserTokenUsage", Params: "senderID string",
		Pattern: "callResult", Method: "MethodGetUserTokenUsage",
		ReqExpr: "getUserTokenUsageReq{SenderID: senderID}", ResultType: "map[string]any"},
	{Name: "GetDailyTokenUsage", Params: "senderID string, days int",
		Pattern: "callResult", Method: "MethodGetDailyTokenUsage",
		ReqExpr: "getDailyTokenUsageReq{SenderID: senderID, Days: days}", ResultType: "[]map[string]any"},
	{Name: "GetTokenState", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) GetTokenState(ch, chatID string) (int64, int64, error) {",
			"\tvar r struct {",
			"\t\tPrompt     int64 `json:\"prompt_tokens\"`",
			"\t\tCompletion int64 `json:\"completion_tokens\"`",
			"\t}",
			"\tif err := c.call(MethodGetTokenState, getTokenStateReq{Channel: ch, ChatID: chatID}, &r); err != nil {",
			"\t\treturn 0, 0, err",
			"\t}",
			"\treturn r.Prompt, r.Completion, nil",
			"}",
		)},

	// ---- Background tasks ----
	{Section: "Background tasks (via RPC)",
		Name: "GetBgTaskCount", Params: "sessionKey string",
		Pattern: "callDirect", Method: "MethodGetBgTaskCount",
		ReqExpr: "getBgTaskCountReq{SessionKey: sessionKey}", ResultType: "int"},
	{Name: "ListBgTasks", Params: "sessionKey string",
		Pattern: "callResult", Method: "MethodListBgTasks",
		ReqExpr: "listBgTasksReq{SessionKey: sessionKey}", ResultType: "[]BgTaskJSON"},
	{Name: "KillBgTask", Params: "taskID string",
		Pattern: "callError", Method: "MethodKillBgTask",
		ReqExpr: "killBgTaskReq{TaskID: taskID}"},
	{Name: "CleanupCompletedBgTasks", Params: "sessionKey string",
		Pattern: "callVoid", Method: "MethodCleanupCompletedBgTasks",
		ReqExpr: "cleanupCompletedBgTasksReq{SessionKey: sessionKey}"},

	// ---- Tenants ----
	{Section: "Tenants (via RPC)",
		Name: "ListTenants", Params: "",
		Pattern: "callResult", Method: "MethodListTenants",
		ReqExpr: "struct{}{}", ResultType: "[]TenantInfo"},

	// ---- Subscriptions ----
	{Section: "Subscriptions (via RPC)",
		Name: "ListSubscriptions", Params: "senderID string",
		Pattern: "callResult", Method: "MethodListSubscriptions",
		ReqExpr: "listSubscriptionsReq{SenderID: senderID}", ResultType: "[]protocol.Subscription"},
	{Name: "GetDefaultSubscription", Params: "senderID string",
		Pattern: "callResult", Method: "MethodGetDefaultSubscription",
		ReqExpr: "getDefaultSubscriptionReq{SenderID: senderID}", ResultType: "*protocol.Subscription"},
	{Name: "AddSubscription", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) AddSubscription(senderID string, sub protocol.Subscription) error {",
			"\treturn c.call(MethodAddSubscription, addSubscriptionReq{",
			"\t\tSenderID: senderID,",
			"\t\tSub: channelSubscriptionJSON{",
			"\t\t\tID: sub.ID, Name: sub.Name, Provider: sub.Provider,",
			"\t\t\tBaseURL: sub.BaseURL, APIKey: sub.APIKey,",
			"\t\t\tModel: sub.Model, Active: sub.Active,",
			"\t\t\tMaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,",
			"\t\t\tPerModelConfigs: sub.PerModelConfigs,",
			"\t\t},",
			"\t}, nil)",
			"}",
		)},
	{Name: "RemoveSubscription", Params: "id string",
		Pattern: "callError", Method: "MethodRemoveSubscription",
		ReqExpr: "removeSubscriptionReq{ID: id}"},
	{Name: "SetDefaultSubscription", Params: "id, chatID string",
		Pattern: "callError", Method: "MethodSetDefaultSubscription",
		ReqExpr: "setDefaultSubscriptionReq{ID: id, ChatID: chatID}"},
	{Name: "UpdateSubscription", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) UpdateSubscription(id string, sub protocol.Subscription) error {",
			"\treturn c.call(MethodUpdateSubscription, updateSubscriptionReq{",
			"\t\tID: id,",
			"\t\tSub: channelSubscriptionJSON{",
			"\t\t\tID: sub.ID, Name: sub.Name, Provider: sub.Provider,",
			"\t\t\tBaseURL: sub.BaseURL, APIKey: sub.APIKey,",
			"\t\t\tModel: sub.Model, Active: sub.Active,",
			"\t\t\tMaxOutputTokens: sub.MaxOutputTokens, ThinkingMode: sub.ThinkingMode,",
			"\t\t\tPerModelConfigs: sub.PerModelConfigs,",
			"\t\t},",
			"\t}, nil)",
			"}",
		)},
	{Name: "UpdatePerModelConfig", Params: "id, model string, pmc protocol.PerModelConfig",
		Pattern: "callError", Method: "MethodUpdatePerModelConfig",
		ReqExpr: "updatePerModelConfigReq{ID: id, Model: model, Config: pmc}"},
	{Name: "SetSubscriptionModel", Params: "id, model string",
		Pattern: "callError", Method: "MethodSetSubscriptionModel",
		ReqExpr: "setSubscriptionModelReq{ID: id, Model: model}"},
	{Name: "RenameSubscription", Params: "id, name string",
		Pattern: "callError", Method: "MethodRenameSubscription",
		ReqExpr: "renameSubscriptionReq{ID: id, Name: name}"},

	// ---- Memory / History ----
	{Section: "Memory / History (via RPC)",
		Name: "ClearMemory", Params: "ctx context.Context, channelName, chatID, targetType, senderID string",
		Pattern: "callError", Method: "MethodClearMemory",
		ReqExpr: "clearMemoryReq{Channel: channelName, ChatID: chatID, TargetType: targetType, SenderID: senderID}"},
	{Name: "GetMemoryStats", Params: "ctx context.Context, ch, chatID, senderID string",
		Pattern: "callDirect", Method: "MethodGetMemoryStats",
		ReqExpr: "getMemoryStatsReq{Channel: ch, ChatID: chatID, SenderID: senderID}", ResultType: "map[string]string"},
	{Name: "GetHistory", Params: "channelName, chatID string",
		Pattern: "callResult", Method: "MethodGetHistory",
		ReqExpr: "getHistoryReq{Channel: channelName, ChatID: chatID}", ResultType: "[]protocol.HistoryMessage"},
	{Name: "TrimHistory", Params: "ch, chatID string, cutoff time.Time",
		Pattern: "callError", Method: "MethodTrimHistory",
		ReqExpr: "trimHistoryReq{Channel: ch, ChatID: chatID, Cutoff: cutoff.Unix()}"},

	// ---- Interactive SubAgent sessions ----
	{Section: "Interactive SubAgent sessions (via RPC)",
		Name: "CountInteractiveSessions", Params: "channelName, chatID string",
		Pattern: "callDirect", Method: "MethodCountInteractiveSessions",
		ReqExpr: "countInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID}", ResultType: "int"},
	{Name: "ListInteractiveSessions", Params: "channelName, chatID string",
		Pattern: "callDirect", Method: "MethodListInteractiveSessions",
		ReqExpr: "listInteractiveSessionsReq{ChannelName: channelName, ChatID: chatID}", ResultType: "[]InteractiveSessionInfo"},
	{Name: "InspectInteractiveSession", Params: "ctx context.Context, roleName, channelName, chatID, instance string, tailCount int",
		Pattern: "callResult", Method: "MethodInspectInteractiveSession",
		ReqExpr:    "inspectInteractiveSessionReq{RoleName: roleName, ChannelName: channelName, ChatID: chatID, Instance: instance, TailCount: tailCount}",
		ResultType: "string"},
	{Name: "GetSessionMessages", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) GetSessionMessages(channelName, chatID, roleName, instance string) ([]SessionMessage, bool) {",
			"\tvar r struct {",
			"\t\tMessages []SessionMessage `json:\"messages\"`",
			"\t\tOK       bool             `json:\"ok\"`",
			"\t}",
			"\tif err := c.call(MethodGetSessionMessages, getSessionMessagesReq{",
			"\t\tChannelName: channelName, ChatID: chatID, RoleName: roleName, Instance: instance,",
			"\t}, &r); err != nil {",
			"\t\treturn nil, false",
			"\t}",
			"\treturn r.Messages, r.OK",
			"}",
		)},
	{Name: "GetAgentSessionDump", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) GetAgentSessionDump(channelName, chatID, roleName, instance string) (*AgentSessionDump, bool) {",
			"\tvar r struct {",
			"\t\tDump *AgentSessionDump `json:\"dump\"`",
			"\t\tOK   bool              `json:\"ok\"`",
			"\t}",
			"\tif err := c.call(MethodGetAgentSessionDump, getAgentSessionDumpReq{",
			"\t\tChannelName: channelName, ChatID: chatID, RoleName: roleName, Instance: instance,",
			"\t}, &r); err != nil {",
			"\t\treturn nil, false",
			"\t}",
			"\treturn r.Dump, r.OK",
			"}",
		)},
	{Name: "GetAgentSessionDumpByFullKey", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) GetAgentSessionDumpByFullKey(fullKey string) (*AgentSessionDump, bool) {",
			"\tvar r struct {",
			"\t\tDump *AgentSessionDump `json:\"dump\"`",
			"\t\tOK   bool              `json:\"ok\"`",
			"\t}",
			"\tif err := c.call(MethodGetAgentSessionDumpByFullKey, getAgentSessionDumpByFullKeyReq{FullKey: fullKey}, &r); err != nil {",
			"\t\treturn nil, false",
			"\t}",
			"\treturn r.Dump, r.OK",
			"}",
		)},

	// ---- Processing state ----
	{Section: "Processing state (via RPC)",
		Name: "IsProcessing", Params: "ch, chatID string",
		Pattern: "callDirect", Method: "MethodIsProcessing",
		ReqExpr: "isProcessingReq{Channel: ch, ChatID: chatID}", ResultType: "bool"},
	{Name: "GetActiveProgress", Params: "ch, chatID string",
		Pattern: "callDirect", Method: "MethodGetActiveProgress",
		ReqExpr: "getActiveProgressReq{Channel: ch, ChatID: chatID}", ResultType: "*protocol.ProgressEvent"},
	{Name: "GetTodos", Params: "ch, chatID string",
		Pattern: "callDirect", Method: "MethodGetTodos",
		ReqExpr: "getTodosReq{Channel: ch, ChatID: chatID}", ResultType: "[]protocol.TodoItem"},

	// ---- Channel config ----
	{Section: "Channel config (via RPC)",
		Name: "GetChannelConfigs", Params: "",
		Pattern: "callResult", Method: "MethodGetChannelConfig",
		ReqExpr: "struct{}{}", ResultType: "map[string]map[string]string"},
	{Name: "SetChannelConfig", Params: "channel string, values map[string]string",
		Pattern: "callError", Method: "MethodSetChannelConfig",
		ReqExpr: "setChannelConfigReq{Channel: channel, Values: values}"},

	// ---- Web Users ----
	{Section: "Web Users (via RPC)",
		Name: "CreateWebUser", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) CreateWebUser(username string) (string, error) {",
			"\tvar resp struct {",
			"\t\tPassword string `json:\"password\"`",
			"\t}",
			"\terr := c.call(\"create_web_user\", map[string]string{\"username\": username}, &resp)",
			"\treturn resp.Password, err",
			"}",
		)},
	{Name: "ListWebUsers", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) ListWebUsers() ([]map[string]any, error) {",
			"\tvar result []map[string]any",
			"\traw, err := c.CallRPC(\"list_web_users\", nil)",
			"\tif err != nil {",
			"\t\treturn nil, err",
			"\t}",
			"\tif err := json.Unmarshal(raw, &result); err != nil {",
			"\t\treturn nil, err",
			"\t}",
			"\treturn result, nil",
			"}",
		)},
	{Name: "DeleteWebUser", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) DeleteWebUser(username string) error {",
			"\t_, err := c.CallRPC(\"delete_web_user\", map[string]string{\"username\": username})",
			"\treturn err",
			"}",
		)},

	// ---- Chat Management ----
	{Section: "Chat Management (via RPC)",
		Name: "DeleteChat", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) DeleteChat(ch, senderID, chatID string) error {",
			"\t_, err := c.CallRPC(\"delete_chat\", map[string]string{",
			"\t\t\"channel\":  ch,",
			"\t\t\"senderid\": senderID,",
			"\t\t\"chat_id\":  chatID,",
			"\t})",
			"\treturn err",
			"}",
		)},
	{Name: "RenameChat", Pattern: "custom",
		Body: joinLines(
			"func (c *Client) RenameChat(ch, senderID, chatID, newName string) error {",
			"\t_, err := c.CallRPC(\"rename_chat\", map[string]string{",
			"\t\t\"channel\":  ch,",
			"\t\t\"senderid\": senderID,",
			"\t\t\"chat_id\":  chatID,",
			"\t\t\"new_name\": newName,",
			"\t})",
			"\treturn err",
			"}",
		)},
}

// ---------------------------------------------------------------------------
// Code generation
// ---------------------------------------------------------------------------

func main() {
	output := flag.String("output", "client_rpc_generated.go", "output file path")
	flag.Parse()

	var buf strings.Builder

	// Header
	buf.WriteString("// Code generated by cmd/genclient/main.go. DO NOT EDIT.\n\n")
	buf.WriteString("package agent\n\n")

	// Imports
	buf.WriteString("import (\n")
	buf.WriteString("\t\"context\"\n")
	buf.WriteString("\t\"encoding/json\"\n")
	buf.WriteString("\t\"time\"\n\n")
	buf.WriteString("\t\"xbot/config\"\n")
	buf.WriteString("\tllm \"xbot/llm\"\n")
	buf.WriteString("\t\"xbot/protocol\"\n")
	buf.WriteString(")\n\n")

	// Generate methods
	for _, m := range spec {
		if m.Section != "" {
			buf.WriteString("// ---------------------------------------------------------------------------\n")
			buf.WriteString("// " + m.Section + "\n")
			buf.WriteString("// ---------------------------------------------------------------------------\n\n")
		}
		switch m.Pattern {
		case "callVoid":
			fmt.Fprintf(&buf, "func (c *Client) %s(%s) {\n", m.Name, m.Params)
			fmt.Fprintf(&buf, "\tc.callVoid(%s, %s)\n", m.Method, m.ReqExpr)
			buf.WriteString("}\n\n")
		case "callError":
			fmt.Fprintf(&buf, "func (c *Client) %s(%s) error {\n", m.Name, m.Params)
			fmt.Fprintf(&buf, "\treturn c.call(%s, %s, nil)\n", m.Method, m.ReqExpr)
			buf.WriteString("}\n\n")
		case "callResult":
			fmt.Fprintf(&buf, "func (c *Client) %s(%s) (%s, error) {\n", m.Name, m.Params, m.ResultType)
			fmt.Fprintf(&buf, "\tvar r %s\n", m.ResultType)
			fmt.Fprintf(&buf, "\treturn r, c.call(%s, %s, &r)\n", m.Method, m.ReqExpr)
			buf.WriteString("}\n\n")
		case "callDirect":
			fmt.Fprintf(&buf, "func (c *Client) %s(%s) %s {\n", m.Name, m.Params, m.ResultType)
			fmt.Fprintf(&buf, "\tvar r %s\n", m.ResultType)
			fmt.Fprintf(&buf, "\t_ = c.call(%s, %s, &r)\n", m.Method, m.ReqExpr)
			buf.WriteString("\treturn r\n")
			buf.WriteString("}\n\n")
		case "custom":
			buf.WriteString(m.Body)
			buf.WriteString("\n\n")
		default:
			fmt.Fprintf(os.Stderr, "unknown pattern %q for method %s\n", m.Pattern, m.Name)
			os.Exit(1)
		}
	}

	if err := os.WriteFile(*output, []byte(buf.String()), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}

	// Count methods
	total := len(spec)
	fmt.Fprintf(os.Stderr, "generated %s: %d methods, %d bytes\n", *output, total, buf.Len())
}

// joinLines joins lines with newline. Helper for custom method bodies.
func joinLines(lines ...string) string {
	return strings.Join(lines, "\n")
}
