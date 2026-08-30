package protocol

import "encoding/json"

// ---------------------------------------------------------------------------
// WebSocket message type constants
// ---------------------------------------------------------------------------

// Server → Client message types
const (
	MsgTypeText           = "text"
	MsgTypeProgress       = "progress_structured"
	MsgTypeStreamContent  = "stream_content"
	MsgTypeRPCResponse    = "rpc_response"
	MsgTypeAskUser        = "ask_user"
	MsgTypeCard           = "card"
	MsgTypeUserEcho       = "user_echo"
	MsgTypeInjectUser     = "inject_user"
	MsgTypePluginWidgets  = "plugin_widgets"
	MsgTypeWebWidgets     = "web_widgets"
	MsgTypeTUIControlReq  = "tui_control_req"
	MsgTypeRunnerStatus   = "runner_status"
	MsgTypeSyncProgress   = "sync_progress"
	MsgTypeSession        = "session"
	MsgTypeGenUI          = "genui"
	MsgTypeResyncRequired = "resync_required"
	MsgTypeBgTaskOutput   = "bg_task_output"
	MsgTypePong           = "__pong__"

	// Channel Plugin → xbot: tool declaration
	MsgTypeChannelTools = "channel_tools"

	// Channel Plugin → xbot: prompt declaration
	MsgTypeChannelPrompt = "channel_prompt"

	// Channel Plugin → xbot: web UI component declarations
	MsgTypeWebUI = "web_ui"

	// Web plugin v2 (frontend ESM plugin runtime) — server → client
	MsgTypeWebPluginInit          = "web_plugin_init"           // 激活/热加载：贡献点 + 模块 URL + 权限
	MsgTypeWebPluginDeactivate    = "web_plugin_deactivate"     // 卸载：前端执行 disposables
	MsgTypeWebPluginEvent         = "web_plugin_event"          // 后端事件 → 前端插件
	MsgTypeWebPluginPush          = "web_plugin_push"           // 后端插件主动推数据
	MsgTypeWebPluginRPC           = "web_plugin_rpc"            // 前端插件 → 后端 RPC 响应
	MsgTypeWebPluginConfigChanged = "web_plugin_config_changed" // 插件配置变更 → 前端插件（热重载）
)

// Client → Server message types
const (
	MsgTypeMessage         = "message"
	MsgTypeCancel          = "cancel"
	MsgTypeRPC             = "rpc"
	MsgTypeSubscribe       = "subscribe"
	MsgTypeSync            = "sync"
	MsgTypeTUIControlResp  = "tui_control_resp"
	MsgTypeAskUserResponse = "ask_user_response"
)

// ---------------------------------------------------------------------------
// TUIControlPayload — TUI control request/response over WS
// ---------------------------------------------------------------------------

// TUIControlPayload carries a TUI control request or response over WS.
type TUIControlPayload struct {
	Action string            `json:"action"`           // "switch" | "close" | "layout" | "theme"
	Params map[string]string `json:"params,omitempty"` // action-specific parameters
	Result map[string]string `json:"result,omitempty"` // response result
	Error  string            `json:"error,omitempty"`  // response error
}

// ---------------------------------------------------------------------------
// AskUser types
// ---------------------------------------------------------------------------

// AskUserResponse is the client response to an ask_user prompt.
type AskUserResponse struct {
	Answers   map[string]string `json:"answers"`   // question index -> answer
	Cancelled bool              `json:"cancelled"` // true = user cancelled
}

// ---------------------------------------------------------------------------
// WS message envelopes — shared between Server (channel/web.go) and Client (agent/transport_remote.go)
// ---------------------------------------------------------------------------

// WSMessage is the unified server→client WebSocket message envelope.
// Replaces the former wsMessage (channel/web.go) and wsIncomingMessage (agent/transport_remote.go).
type WSMessage struct {
	Type            string             `json:"type"`
	ID              string             `json:"id,omitempty"`
	Seq             uint64             `json:"seq,omitempty"`
	Content         string             `json:"content,omitempty"`
	OriginalContent string             `json:"original_content,omitempty"`
	TS              int64              `json:"ts,omitempty"`
	TaskID          string             `json:"task_id,omitempty"` // bg_task_output: which task this delta belongs to
	Progress        *ProgressEvent     `json:"progress,omitempty"`
	ProgressHistory string             `json:"progress_history,omitempty"`
	Channel         string             `json:"channel,omitempty"`
	ChatID          string             `json:"chat_id,omitempty"`
	RouteChannel    string             `json:"route_channel,omitempty"`
	RouteChatID     string             `json:"route_chat_id,omitempty"`
	SenderID        string             `json:"sender_id,omitempty"`
	SenderName      string             `json:"sender_name,omitempty"`
	ChatType        string             `json:"chat_type,omitempty"`
	SessionReset    bool               `json:"session_reset,omitempty"`
	Cancelled       bool               `json:"cancelled,omitempty"` // true = turn was cancelled by user
	TurnID          uint64             `json:"turn_id,omitempty"`   // associates reply with user message by turn
	Metadata        map[string]string  `json:"metadata,omitempty"`
	Result          json.RawMessage    `json:"result,omitempty"`
	Error           string             `json:"error,omitempty"`
	TUIControl      *TUIControlPayload `json:"tui_control,omitempty"`
	Session         *SessionEvent      `json:"session,omitempty"`
}

// WSClientMessage is the unified client→server WebSocket message envelope.
// Replaces the former wsClientMessage (channel/web.go) and wsOutgoingMessage (agent/transport_remote.go).
type WSClientMessage struct {
	Type       string             `json:"type"`
	Content    string             `json:"content,omitempty"`
	FileIDs    []string           `json:"file_ids,omitempty"`
	FileNames  []string           `json:"file_names,omitempty"`
	FileSizes  []int64            `json:"file_sizes,omitempty"`
	UploadKeys []string           `json:"upload_keys,omitempty"`
	FileMimes  []string           `json:"file_mimes,omitempty"`
	Channel    string             `json:"channel,omitempty"`
	ChatID     string             `json:"chat_id,omitempty"`
	SenderID   string             `json:"sender_id,omitempty"`
	SenderName string             `json:"sender_name,omitempty"`
	ChatType   string             `json:"chat_type,omitempty"`
	LastSeq    uint64             `json:"last_seq,omitempty"`
	Resume     bool               `json:"resume,omitempty"`
	ID         string             `json:"id,omitempty"`
	Method     string             `json:"method,omitempty"`
	Params     json.RawMessage    `json:"params,omitempty"`
	TUIControl *TUIControlPayload `json:"tui_control,omitempty"`
}
