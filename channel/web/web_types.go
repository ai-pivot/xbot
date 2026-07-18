package web

import (
	"database/sql"
	"encoding/json"
	"os"
	"time"

	ch "xbot/channel"
	"xbot/protocol"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// WebConfig (channel-level)
// ---------------------------------------------------------------------------

// WebChannelConfig Web 渠道配置（channel 包内部使用）
type WebChannelConfig struct {
	Host       string
	Port       int
	DB         *sql.DB // SQLite DB handle for user management and history
	AdminToken string  // global admin token for privileged auth
	InviteOnly bool    // 禁止自主注册，新账号只能由 admin 创建
	PublicURL  string  // 对外访问地址，用于生成 Runner 连接命令
}

// WebCallbacks holds callback functions for Web channel API endpoints.
// Injected from main to decouple channel from agent/tools packages.
type WebCallbacks struct {
	// RunnerTokenGet returns the runner connect command for the user ("" if none).
	RunnerTokenGet func(senderID string) string
	// RunnerTokenGenerate generates a new per-user token and returns the connect command.
	RunnerTokenGenerate func(senderID, mode, dockerImage, workspace string) (string, error)
	// RunnerTokenRevoke revokes the user's current token.
	RunnerTokenRevoke func(senderID string) error
	// RunnerList lists all runners for a user with online status.
	RunnerList func(senderID string) ([]tools.RunnerInfo, error)
	// RunnerCreate creates a new named runner and returns the connect command.
	RunnerCreate func(senderID, name, mode, dockerImage, workspace string, llm tools.RunnerLLMSettings) (string, error)
	// RunnerDelete deletes a named runner.
	RunnerDelete func(senderID, name string) error
	// RunnerGetActive returns the active runner name for the user.
	RunnerGetActive func(senderID string) (string, error)
	// RunnerSetActive sets the active runner for the user.
	RunnerSetActive func(senderID, name string) error
	// RegistryBrowse lists available agents/skills in the marketplace.
	RegistryBrowse func(entryType string, limit, offset int) ([]sqlite.SharedEntry, error)
	// RegistryInstall installs a shared entry for the user.
	RegistryInstall func(entryType string, id int64, senderID string) error
	// RegistryListMy lists the user's installed entries.
	RegistryListMy func(senderID, entryType string) ([]sqlite.SharedEntry, []string, error)
	// RegistryUnpublish removes a user's published entry.
	RegistryUnpublish func(entryType, name, senderID string) error
	// RegistryUninstall removes a user-installed entry.
	RegistryUninstall func(entryType, name, senderID string) error
	// LLMList returns available model entries and current entry.
	LLMList func(senderID string) ([]protocol.ModelEntry, protocol.ModelEntry)
	// LLMSet switches the user's model via explicit (subID, model).
	LLMSet func(senderID, subID, model string) error
	// LLMGetConfig returns user's LLM config (provider, baseURL, model, ok).
	LLMGetConfig func(senderID string) (provider, baseURL, model string, ok bool)
	// IsProcessing returns true if the backend is actively processing a request for the user.
	IsProcessing func(senderID string) bool
	// GetActiveProgress returns the latest progress snapshot for an active turn.
	// Used by Web history API to restore progress state on page refresh.
	GetActiveProgress func(channel, chatID string) *protocol.ProgressEvent
	// GetPendingAskUser returns the pending AskUser prompt for a chat, or nil.
	// Used by Web WS reconnect to resend ask_user so page refresh doesn't lose it.
	GetPendingAskUser func(channel, chatID string) *protocol.ProgressEvent
	// WithPendingAskUser invokes fn while the matching prompt cannot be cleared.
	// Web transports use it as a bounded delivery-admission gate; network writes
	// happen after it returns. fn must not mutate pending AskUser state.
	WithPendingAskUser func(channel, chatID string, fn func(*protocol.ProgressEvent) bool) bool
	// HistorySnapshot returns a Web-only history snapshot with runtime state.
	HistorySnapshot func(senderID string, sel SessionSelector) (HistorySnapshot, error)
	// RewindHistory rewinds a Web-accessible session to a selected user message.
	RewindHistory func(senderID string, sel SessionSelector, cutoff time.Time) (RewindHistoryResult, error)
	// GetCWD returns the current directory for a Web-accessible session.
	GetCWD func(senderID string, sel SessionSelector) (string, error)
	// SetCWD sets the current directory for a Web-accessible session.
	SetCWD func(senderID string, sel SessionSelector, dir string) error
	// BackgroundTasks returns background shell tasks for a Web-accessible session.
	BackgroundTasks func(senderID string, sel SessionSelector) (any, error)
	// CronTasks returns scheduled tasks for a Web-accessible session.
	CronTasks func(senderID string, sel SessionSelector) (any, error)
	// CommandList returns slash-command completion metadata for the Web UI.
	CommandList func(senderID string) ([]CommandInfo, error)
	// SessionSubscription returns the model/subscription selected for a Web-accessible session.
	SessionSubscription func(senderID string, sel SessionSelector) (map[string]string, error)
	// LLMSetConfig sets user's personal LLM config.
	LLMSetConfig func(senderID, provider, baseURL, apiKey, model string, maxOutputTokens int, thinkingMode string) error
	// LLMDelete reverts user to global LLM config.
	LLMDelete func(senderID string) error
	// LLMGetMaxContext returns the per-(subID, model) max context tokens
	// setting. When subID/model are empty, falls back to session resolution.
	LLMGetMaxContext func(senderID, subID, model string) int
	// LLMSetMaxContext sets the per-(subID, model) max context tokens setting.
	// When subID/model are empty, falls back to session resolution.
	LLMSetMaxContext func(senderID, subID, model string, maxContext int) error

	// RegistryPublish publishes a user's agent/skill to the marketplace.
	RegistryPublish func(entryType, name, senderID string) error
	// SandboxWriteFile writes file data to the user's sandbox at the given path.
	// Returns (sandboxInternalPath, error). sandboxInternalPath is the path inside
	// the sandbox (e.g., /workspace/uploads/file.txt). Returns ("", nil) if no sandbox available.
	SandboxWriteFile func(senderID string, sandboxRelPath string, data []byte, perm os.FileMode) (sandboxPath string, err error)
	// RunnerStatusNotify is called when a runner connects/disconnects.
	// Used by main to wire up real-time status push to WebChannel.
	RunnerStatusNotify func(senderID, runnerName string, online bool)
	// SyncProgressNotify is called when runner sync progress is reported.
	SyncProgressNotify func(senderID, phase, message string)
	// RPCHandler handles RPC requests from authenticated REST and remote CLI clients.
	// The method string identifies the operation; params is the JSON-encoded request body.
	// identity carries the channel identity and canonical authorization fields resolved
	// at the HTTP or WebSocket authentication boundary.
	// Returns JSON-encoded result or an error.
	RPCHandler func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error)
	// SessionsList returns interactive SubAgent sessions for a user (channel="web", chatID=senderID).
	// Returns JSON-serializable session info objects.
	SessionsList func(senderID string) []SessionInfo
	// SessionMessages returns the conversation messages for a specific SubAgent session.
	// Returns (messages, true) if found, (nil, false) otherwise.
	SessionMessages func(senderID, roleName, instance string) ([]ch.SessionChatMessage, bool)

	// ChatList returns all chatrooms for a user (main + user-created).
	// channel parameter selects which channel's sessions to list ("web", "cli", etc.).
	ChatList func(senderID, currentChatID, channel string) ([]UserChatWithPreview, error)
	// SubAgentList returns Web-only SubAgent rows for the sidebar tree.
	SubAgentList func(senderID string, admin bool) ([]UserChatWithPreview, error)
	// SessionTree returns Web-only main sessions with SubAgent children already attached.
	SessionTree func(senderID string, current SessionSelector, admin bool) (SessionTreeResult, error)
	// ChatCreate creates a new chatroom for a user. Returns new chatID.
	ChatCreate func(senderID, label string) (string, error)
	// ChatDelete deletes a chatroom (except the default one).
	ChatDelete func(senderID, channel, chatID string) error
	// ChatRename renames a chatroom.
	ChatRename func(senderID, channel, chatID, label string) error
	// LocalSessionExists checks non-DB session metadata surfaced in the tree.
	LocalSessionExists func(channel, chatID string) bool

	// IdentityResolver provides canonical user identity resolution, link code
	// generation, merge preview/execution, and admin user management.
	IdentityResolver IdentityResolverAPI
}

type RPCIdentity struct {
	SenderID        string
	CanonicalUserID int64
	CanonicalRole   string
}

// IdentityResolverAPI is the interface WebChannel uses for account linking.
// Implemented by *agent.IdentityResolver.
type IdentityResolverAPI interface {
	Resolve(channel, channelUserID string) (int64, string, error)
	IsAdmin(userID int64) bool
	SetRole(userID int64, role string) error
	ListIdentities(userID int64) (any, error)
	ListAllUsers() (any, error)
	GenerateLinkCode(userID int64) (string, error)
	ConsumeLinkCode(code string) (int64, error)
	ValidateLinkCode(code string) (int64, error)
	LinkIdentity(targetUserID int64, channel, channelUserID string) (bool, error)
	PreviewMerge(sourceUserID, targetUserID int64) (any, error)
	MergeUsers(sourceUserID, targetUserID int64) error
	UnlinkIdentity(userID, identityID int64) error
}

// IdentityEntry is a channel identity linked to a canonical user.
type IdentityEntry struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"user_id"`
	Channel       string `json:"channel"`
	ChannelUserID string `json:"channel_user_id"`
	LinkedAt      string `json:"linked_at"`
}

// UserInfo represents a canonical user's metadata.
type UserInfo struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	CreatedAt   string `json:"created_at"`
}

// MergePreview shows what would happen if sourceUser is merged into targetUser.
type MergePreview struct {
	SourceUserID  int64    `json:"source_user_id"`
	TargetUserID  int64    `json:"target_user_id"`
	Identities    int      `json:"identities"`
	Subscriptions int      `json:"subscriptions"`
	Runners       int      `json:"runners"`
	Settings      int      `json:"settings"`
	DefaultModel  int      `json:"default_model"`
	UserChats     int      `json:"user_chats"`
	Tenants       int      `json:"tenants"`
	CronJobs      int      `json:"cron_jobs"`
	EventTriggers int      `json:"event_triggers"`
	Conflicts     []string `json:"conflicts"`
}

// UserChatWithPreview is a chatroom with metadata for API responses.
// This mirrors storage/sqlite.UserChatWithPreview to avoid channel→storage dependency.
type UserChatWithPreview struct {
	ChatID        string                `json:"chat_id"`
	Channel       string                `json:"channel,omitempty"` // channel name (e.g. "web", "cli", "feishu")
	Label         string                `json:"label"`
	LastActive    string                `json:"last_active"` // RFC3339
	Preview       string                `json:"preview"`
	IsCurrent     bool                  `json:"is_current"`
	Type          string                `json:"type,omitempty"` // "agent" for historical SubAgent tenant rows
	FullKey       string                `json:"full_key,omitempty"`
	Role          string                `json:"role,omitempty"`
	Instance      string                `json:"instance,omitempty"`
	ParentChatID  string                `json:"parent_chat_id,omitempty"`
	ParentChannel string                `json:"parent_channel,omitempty"`
	Historical    bool                  `json:"historical,omitempty"`
	Running       bool                  `json:"running,omitempty"`
	Status        string                `json:"status,omitempty"`
	Synthetic     bool                  `json:"synthetic,omitempty"`
	Children      []UserChatWithPreview `json:"children,omitempty"`
}

// SessionTreeNode is a Web-only sidebar row. Children are SubAgent rows
// matched by the backend using the same parent identity rules as the TUI.
type SessionTreeNode struct {
	UserChatWithPreview
}

// SessionTreeResult is the Web-only sidebar tree response. OrphanSubAgents are
// returned separately instead of being silently dropped when a historical row's
// parent is not in the current main-session list.
type SessionTreeResult struct {
	Sessions        []SessionTreeNode     `json:"sessions"`
	OrphanSubAgents []UserChatWithPreview `json:"orphan_subagents,omitempty"`
}

// HistorySnapshot is the Web-only /api/history response payload.
type HistorySnapshot struct {
	Messages       []protocol.HistoryMessage `json:"messages,omitempty"`
	Processing     bool                      `json:"processing,omitempty"`
	ActiveProgress *protocol.ProgressEvent   `json:"active_progress,omitempty"`
	LastSeq        uint64                    `json:"last_seq,omitempty"`
	ChatID         string                    `json:"chat_id,omitempty"`
	Channel        string                    `json:"channel,omitempty"`
}

// RewindHistoryResult is the Web-only /api/history/rewind response payload.
type RewindHistoryResult struct {
	Draft        string                 `json:"draft"`
	RewindResult *protocol.RewindResult `json:"rewind_result,omitempty"`
}

// CommandInfo is a JSON-friendly slash-command descriptor for Web completion.
type CommandInfo struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description,omitempty"`
}

// ChatRoom represents a conversation between the user and/or agents.
// Both human↔agent and agent↔agent conversations are ChatRooms.
type ChatRoom struct {
	ID       string `json:"id"`       // "main" for primary chat, "role/instance" for SubAgent
	Type     string `json:"type"`     // "main" (human↔agent) or "subagent" (agent↔agent)
	Label    string `json:"label"`    // Display name: "主会话" or "brainstorm/rt-1"
	Role     string `json:"role"`     // SubAgent role name (empty for main)
	Instance string `json:"instance"` // SubAgent instance ID (empty for main)
	Running  bool   `json:"running"`  // Is the SubAgent currently running?
	Preview  string `json:"preview"`  // Latest message/progress preview
	Members  string `json:"members"`  // "You ↔ Agent" or "reviewer ↔ tester"
}

// SessionInfo represents a snapshot of an interactive SubAgent session (for API responses).
// Deprecated: Use ChatRoom instead.
type SessionInfo = ChatRoom

// SessionSelector holds the active channel + chatID for cross-channel browsing.
type SessionSelector struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}
