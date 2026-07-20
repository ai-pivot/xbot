// xbot Web Channel implementation

package web

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"xbot/bus"
	ch "xbot/channel"
	log "xbot/logger"
	"xbot/protocol"
	"xbot/tools"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// Frontend static files are served from an external directory (set via SetStaticDir).
// No go:embed — frontend is deployed independently.

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const (
	webSendChBufSize     = 64
	webOfflineMsgBufSize = 50
	webSessionCookieName = "xbot_session"
	webSessionMaxAge     = 30 * 24 * time.Hour // 30 days
	maxBodySize          = 1 << 20             // 1MB maximum request body size
)

// limitBodySize wraps a handler to limit request body size.
func limitBodySize(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBodySize)
		next(w, r)
	}
}

// ---------------------------------------------------------------------------
// WebConfig (channel-level)
// ---------------------------------------------------------------------------

// RPCIdentity identifies the authenticated user for RPC dispatch.
type RPCIdentity struct {
	SenderID        string
	CanonicalUserID int64
	CanonicalRole   string
}

// WebChannelConfig Web 渠道配置（channel 包内部使用）
type WebChannelConfig struct {
	Host       string
	Port       int
	DB         *sql.DB // SQLite DB handle for user management and history
	AdminToken string  // global admin token for privileged auth
	InviteOnly bool    // 禁止自主注册，新账号只能由 admin 创建
	PublicURL  string  // 对外访问地址，用于生成 Runner 连接命令
	SingleUser bool    // 单用户模式：所有用户共享同一身份，视为 admin
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
	// LocalSessionExists reports whether a local session exists outside the database.
	LocalSessionExists func(channel, chatID string) bool

	// IdentityResolver provides canonical user identity resolution, link code
	// generation, merge preview/execution, and admin user management.
	IdentityResolver IdentityResolverAPI
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
	WorkDir       string                `json:"work_dir,omitempty"`
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

// ---------------------------------------------------------------------------
// Hub: manages all WebSocket clients
// ---------------------------------------------------------------------------

// Hub 管理所有 WebSocket 连接。
// ---------------------------------------------------------------------------
// WebChannel: implements Channel interface
// ---------------------------------------------------------------------------

// WebChannel Web 渠道实现
type WebChannel struct {
	config   WebChannelConfig
	msgBus   *bus.MessageBus
	hub      *Hub
	server   *http.Server
	listener net.Listener

	// Callbacks from main
	callbacks WebCallbacks

	// Auth
	sessions   map[string]sessionInfo // token → sessionInfo
	sessionsMu sync.RWMutex

	// Inbound request dedup
	inboundRequests   map[inboundRequestKey]*inboundRequestState
	inboundRequestsMu sync.Mutex

	// DB
	db *sql.DB

	// Lifecycle
	stopCh chan struct{}
	wg     sync.WaitGroup

	// Static files (external directory)
	staticDir string

	// Working directory (workspace) — used to copy uploaded files into sandbox-accessible path
	workDir string

	// OSS provider for file storage (local or qiniu)
	ossProvider OSSProvider

	// Event stream buffer — per chatID monotonic seq + ring buffer for replay
	evtBuf   map[string]*eventStream
	evtBufMu sync.Mutex

	// Per-user current session (multi-chatroom + cross-channel support).
	// Key: senderID, Value: SessionSelector (channel + chatID).
	// Defaults to {Channel:"web", ChatID:senderID} if not set.
	userCurrentSession   map[string]SessionSelector
	userCurrentSessionMu sync.RWMutex

	// singleUser mirrors config.Experimental.SingleUser — when true, all
	// web users are treated as admin (no identity isolation).
	singleUser bool
}

type sessionInfo struct {
	userID       int
	username     string
	feishuUserID string // non-empty when logged in via Feishu identity
	expires      time.Time
}

// Compile-time interface assertion.
var _ ch.SessionStateSender = (*WebChannel)(nil)

// SendSessionState implements ch.SessionStateSender.
// WebSocket clients retain the legacy broadcast behavior; SSE clients receive
// only events for their authorized chat subscription.
func (wc *WebChannel) SendSessionState(ev protocol.SessionEvent) {
	wc.hub.broadcastSessionState(ev.Channel, ev.ChatID, protocol.WSMessage{
		Type:    protocol.MsgTypeSession,
		TS:      time.Now().Unix(),
		Session: &ev,
	})
}

// SessionSelector holds the active channel + chatID for cross-channel browsing.
type SessionSelector struct {
	Channel string `json:"channel"`
	ChatID  string `json:"chat_id"`
}

// NewWebChannel 创建 Web 渠道
func NewWebChannel(cfg WebChannelConfig, msgBus *bus.MessageBus) *WebChannel {
	wc := &WebChannel{
		config:             cfg,
		msgBus:             msgBus,
		hub:                newHub(),
		sessions:           make(map[string]sessionInfo),
		db:                 cfg.DB,
		stopCh:             make(chan struct{}),
		inboundRequests:    make(map[inboundRequestKey]*inboundRequestState),
		userCurrentSession: make(map[string]SessionSelector),
		singleUser:         cfg.SingleUser,
	}
	wc.hub.seqFn = wc.stampAndBuffer
	return wc
}

// Hub returns the web channel's hub for sharing with other channels.
func (wc *WebChannel) Hub() *Hub {
	return wc.hub
}

// SetStaticDir sets the directory for serving frontend static files.
func (wc *WebChannel) SetStaticDir(dir string) {
	if dir != "" {
		wc.staticDir = filepath.Clean(dir)
	}
}

// SetWorkDir sets the working directory for sandbox file access.
func (wc *WebChannel) SetWorkDir(dir string) {
	if dir != "" {
		wc.workDir = filepath.Clean(dir)
	}
}

// SetOSSProvider sets the OSS provider for file storage.
func (wc *WebChannel) SetOSSProvider(p OSSProvider) {
	wc.ossProvider = p
}

// SetCallbacks injects callback functions from main for API endpoints.
func (wc *WebChannel) SetCallbacks(cb WebCallbacks) {
	wc.callbacks = cb
}

// SetRPCHandler sets or replaces the RPC handler. Used to wire the handler
// after the dispatcher and message bus are available.
func (wc *WebChannel) SetRPCHandler(fn func(method string, params json.RawMessage, identity RPCIdentity) (json.RawMessage, error)) {
	wc.callbacks.RPCHandler = fn
}

// rpcCall invokes a server-side RPC method via the injected RPCHandler.
// It marshals params to JSON, calls the handler, and unmarshals the result.
func (wc *WebChannel) rpcCall(method string, params any, result any) error {
	if wc.callbacks.RPCHandler == nil {
		return fmt.Errorf("RPC handler not configured")
	}
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	resp, err := wc.callbacks.RPCHandler(method, paramsJSON, RPCIdentity{SenderID: "web_admin"})
	if err != nil {
		return err
	}
	if result != nil && len(resp) > 0 {
		if err := json.Unmarshal(resp, result); err != nil {
			return fmt.Errorf("unmarshal result: %w", err)
		}
	}
	return nil
}

func (wc *WebChannel) Name() string { return "web" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start 启动 Web 渠道 HTTP server
func (wc *WebChannel) Start() error {
	mux := wc.newServeMux()

	addr := fmt.Sprintf("%s:%d", wc.config.Host, wc.config.Port)
	// Use custom listener with SO_REUSEADDR to avoid "address already in use"
	// after unclean shutdown (e.g., SIGKILL, crash).
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		return c.Control(func(fd uintptr) {
			setReuseAddr(fd)
		})
	}}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", addr, err)
	}
	wc.listener = ln

	wc.server = &http.Server{
		Addr:         addr,
		Handler:      wc.securityHeadersMiddleware(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	log.WithFields(log.Fields{
		"host": wc.config.Host,
		"port": wc.config.Port,
	}).Info("Web channel starting...")

	// Start cleanup goroutine for expired sessions
	wc.wg.Add(1)
	go wc.sessionCleanup()

	err = wc.server.Serve(wc.listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (wc *WebChannel) newServeMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", wc.handleWS)
	mux.HandleFunc("/api/sse", wc.authMiddleware(wc.handleSSE))

	mux.HandleFunc("/api/auth/register", limitBodySize(postOnly(wc.handleRegister)))
	mux.HandleFunc("/api/auth/login", limitBodySize(postOnly(wc.handleLogin)))
	mux.HandleFunc("/api/auth/logout", postOnly(wc.handleLogout))
	mux.HandleFunc("/api/auth/feishu-link", limitBodySize(postOnly(wc.handleFeishuLink)))
	mux.HandleFunc("/api/auth/feishu-login", limitBodySize(postOnly(wc.handleFeishuLogin)))
	mux.HandleFunc("/api/auth/config", postOnly(wc.handleAuthConfig))

	mux.HandleFunc("/api/message", wc.authenticatedPOST(wc.handleMessage))
	mux.HandleFunc("/api/cancel", wc.authenticatedPOST(wc.handleCancel))
	mux.HandleFunc("/api/ask_user/respond", wc.authenticatedPOST(wc.handleAskUserRespond))
	mux.HandleFunc("/api/rpc", wc.authenticatedPOST(wc.handleRPC))
	mux.HandleFunc("/api/history", wc.authenticatedPOST(wc.handleHistoryPOST))
	mux.HandleFunc("/api/history/rewind", wc.authenticatedPOST(wc.handleHistoryRewind))
	mux.HandleFunc("/api/search", wc.authenticatedPOST(wc.handleSearchPOST))

	mux.HandleFunc("/api/settings", wc.authenticatedPOST(wc.handleSettingsPOST))
	mux.HandleFunc("/api/llm-config", wc.authenticatedPOST(wc.handleLLMConfigPOST))
	mux.HandleFunc("/api/session/status", wc.authenticatedPOST(wc.handleSessionStatus))

	mux.HandleFunc("/api/runners/list", wc.authenticatedPOST(wc.handleRunnersListPOST))
	mux.HandleFunc("/api/runners/create", wc.authenticatedPOST(wc.handleRunnersCreatePOST))
	mux.HandleFunc("/api/runners/active", wc.authenticatedPOST(wc.handleRunnerActivePOST))
	mux.HandleFunc("/api/runners/{name}/delete", wc.authenticatedPOST(wc.handleRunnerDeletePOST))

	mux.HandleFunc("/api/files/upload", wc.authenticatedPOST(wc.handleFileUpload))
	mux.HandleFunc("/api/fs/list", wc.authenticatedPOST(wc.handleFsListPOST))
	mux.HandleFunc("/api/fs/read", wc.authenticatedPOST(wc.handleFsReadPOST))
	mux.HandleFunc("/api/fs/search", wc.authenticatedPOST(wc.handleFsSearchPOST))

	mux.HandleFunc("/api/chats/list", wc.authenticatedPOST(wc.handleChatsListPOST))
	mux.HandleFunc("/api/chats/create", wc.authenticatedPOST(wc.handleChatsCreatePOST))
	mux.HandleFunc("/api/chats/{chatID}/switch", wc.authenticatedPOST(wc.handleChatSwitchPOST))
	mux.HandleFunc("/api/chats/{chatID}/rename", wc.authenticatedPOST(wc.handleChatRename))
	mux.HandleFunc("/api/chats/{chatID}/delete", wc.authenticatedPOST(wc.handleChatDeletePOST))
	mux.HandleFunc("/api/session-tree", wc.authenticatedPOST(wc.handleSessionTreePOST))

	mux.HandleFunc("/api/account/link-code", wc.authenticatedPOST(wc.handleLinkCode))
	mux.HandleFunc("/api/account/link", wc.authenticatedPOST(wc.handleLink))
	mux.HandleFunc("/api/account/identities/list", wc.authenticatedPOST(wc.handleIdentitiesListPOST))
	mux.HandleFunc("/api/account/identities/{id}/delete", wc.authenticatedPOST(wc.handleUnlinkIdentityPOST))

	mux.HandleFunc("/api/admin/users/list", wc.authenticatedPOST(wc.handleAdminUsersListPOST))
	mux.HandleFunc("/api/admin/users/{id}/set-role", wc.authenticatedPOST(wc.handleAdminSetRole))

	// App bundle API
	mux.HandleFunc("/api/app/pack", wc.authenticatedPOST(wc.handleMarketPack))
	mux.HandleFunc("/api/app/install-file", wc.authenticatedPOST(wc.handleMarketInstallFile))
	mux.HandleFunc("/api/app/uninstall", wc.authenticatedPOST(wc.handleMarketUninstall))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		jsonErrorResponse(w, http.StatusNotFound, "endpoint not found")
	})
	if wc.staticDir != "" {
		mux.HandleFunc("/", wc.handleStatic)
	}
	return mux
}

// Stop 停止 Web 渠道
func (wc *WebChannel) Stop() {
	log.Info("Web channel stopping...")
	close(wc.stopCh)

	wc.hub.stopAll()

	if wc.server != nil {
		ctx, cancel := func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 5*time.Second)
		}()
		_ = wc.server.Shutdown(ctx)
		cancel()
	}

	wc.wg.Wait()
	log.Info("Web channel stopped")
}

// ---------------------------------------------------------------------------
// Send: non-blocking write to WebSocket client
// ---------------------------------------------------------------------------

// Send 发送消息到 Web 客户端（非阻塞）

func (wc *WebChannel) Send(msg ch.OutboundMsg) (string, error) {
	msgID := strings.ReplaceAll(uuid.New().String(), "-", "")

	content := msg.Content
	msgType := "text"

	// __FEISHU_CARD__ protocol adaptation
	if strings.HasPrefix(content, "__FEISHU_CARD__") {
		msgType = "card"
		content = ch.ConvertFeishuCard(content)
	}

	wsMsg := protocol.WSMessage{
		Type:            msgType,
		ID:              msgID,
		Content:         content,
		TS:              time.Now().Unix(),
		ProgressHistory: msg.Metadata["progress_history"],
		Channel:         msg.Channel,
		ChatID:          msg.ChatID,
		SessionReset:    msg.Metadata != nil && msg.Metadata["session_reset"] == "true",
		// Only forward frontend-relevant metadata keys — avoid leaking internal
		// keys like feishu_user_id, request_id, cancelled, etc.
	}

	targetClientID := msg.ChatID
	channelName := msg.Channel
	if channelName == "" {
		channelName = "web"
	}

	// /new resets the conversation boundary. Drop buffered pre-reset events before
	// buffering the reset message itself; otherwise reconnect replay can resurrect
	// a stale in-flight progress event from the previous session.
	if wsMsg.SessionReset {
		wc.getEventStream(sessionRouteKey(channelName, targetClientID)).clear()
	}

	// The Hub stamps and buffers Web events before fan-out while preserving the
	// remote CLI WebSocket envelope unchanged.
	if !wc.hub.sendToSession(channelName, targetClientID, wsMsg) {
		log.WithFields(log.Fields{"chat_id": msg.ChatID, "target_client_id": targetClientID}).Debug("Web client offline, message buffered")
	}

	// AskUser: agent needs user input
	if msg.WaitingUser {
		askPayload := &protocol.ProgressEvent{}
		if msg.Metadata != nil {
			askPayload.RequestID = msg.Metadata["request_id"]
			if qJSON := msg.Metadata["ask_questions"]; qJSON != "" {
				var qs []protocol.AskUserQuestion
				if json.Unmarshal([]byte(qJSON), &qs) == nil {
					askPayload.Questions = qs
				}
			}
		}
		askMsg := protocol.WSMessage{
			Type:     protocol.MsgTypeAskUser,
			ID:       msgID,
			TS:       time.Now().Unix(),
			Channel:  msg.Channel,
			ChatID:   msg.ChatID,
			Progress: askPayload,
		}
		if wc.callbacks.WithPendingAskUser != nil {
			wc.callbacks.WithPendingAskUser(msg.Channel, msg.ChatID, func(pending *protocol.ProgressEvent) bool {
				if pending.RequestID != askPayload.RequestID {
					return false
				}
				askMsg.Progress = pending
				wc.hub.sendToSession(channelName, targetClientID, askMsg)
				return true
			})
		}
	}

	return msgID, nil
}

// stampAndBuffer assigns a monotonic seq to the message and appends it to the
// per-chatID event stream buffer. Returns the stamped message (ready to send).
func (wc *WebChannel) stampAndBuffer(chatID string, msg protocol.WSMessage) protocol.WSMessage {
	es := wc.getEventStream(chatID)
	msg.Seq = es.nextSeq()
	es.push(msg)
	return msg
}

// SendProgress 发送结构化进度事件到 Web 客户端（非阻塞）。
// 内部通过 hub 的缓冲通道发送，保持调用路径轻量。
func (wc *WebChannel) SendProgress(chatID string, payload *protocol.ProgressEvent) {
	if payload == nil {
		return
	}

	wsMsg := protocol.WSMessage{
		Type:     protocol.MsgTypeProgress,
		TS:       time.Now().Unix(),
		Progress: payload,
	}

	if !wc.hub.sendToSession("web", chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, progress event buffered")
	}
}

// SendStreamContent sends streaming LLM content to a specific client.
// Used by CLI RemoteBackend connections to push token-by-token streaming.
func (wc *WebChannel) SendStreamContent(chatID, content, reasoning string) {
	if content == "" && reasoning == "" {
		return
	}
	wsMsg := protocol.WSMessage{
		Type: protocol.MsgTypeStreamContent,
		TS:   time.Now().Unix(),
		Progress: &protocol.ProgressEvent{
			ChatID:                 "web:" + chatID,
			StreamContent:          content,
			ReasoningStreamContent: reasoning,
		},
	}
	_ = wc.hub.sendToSession("web", chatID, wsMsg) // stream events are ephemeral, safe to drop
}

// PushRunnerStatus pushes a runner online/offline status change to the Web client.
func (wc *WebChannel) PushRunnerStatus(chatID, runnerName string, online bool) {
	wsMsg := protocol.WSMessage{
		Type: protocol.MsgTypeRunnerStatus,
		TS:   time.Now().Unix(),
		Content: func() string {
			b, _ := json.Marshal(map[string]any{"runner_name": runnerName, "online": online})
			return string(b)
		}(),
	}
	if !wc.hub.sendToSession("web", chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, runner status buffered")
	}
}

// PushSyncProgress pushes a sync progress notification to the Web client.
func (wc *WebChannel) PushSyncProgress(chatID, phase, message string) {
	wsMsg := protocol.WSMessage{
		Type: "sync_progress",
		TS:   time.Now().Unix(),
		Content: func() string {
			b, _ := json.Marshal(map[string]any{"phase": phase, "message": message})
			return string(b)
		}(),
	}
	if !wc.hub.sendToSession("web", chatID, wsMsg) {
		log.WithField("chat_id", chatID).Debug("Web client offline, sync progress buffered")
	}
}

// ---------------------------------------------------------------------------
// WebSocket handler
// ---------------------------------------------------------------------------

// wsUpgrader returns a WebSocket upgrader with origin checking.
func (wc *WebChannel) wsUpgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			// Allow same-origin or configured public URL
			if wc.config.PublicURL != "" {
				if pu, err := url.Parse(wc.config.PublicURL); err == nil && u.Host == pu.Host {
					return true
				}
			}
			// Always allow requests from the backend's own host (e.g. Vite proxy
			// sets Origin to the backend host, or direct browser access).
			if u.Host == r.Host {
				return true
			}
			// Allow localhost origins in development (Vite dev server on
			// a different port proxies to the backend).
			if u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" {
				return true
			}
			return false
		},
	}
}

func (wc *WebChannel) handleWS(w http.ResponseWriter, r *http.Request) {
	var senderID, username string
	var si *sessionInfo

	// Support token-based auth for CLI clients (RemoteBackend).
	// Query params: ?token=<runner_token>&client_type=cli
	if token := r.URL.Query().Get("token"); token != "" && r.URL.Query().Get("client_type") == "cli" {
		var err error
		senderID, err = wc.validateCLIToken(token)
		if err != nil {
			log.WithError(err).Warn("CLI token auth failed")
			jsonErrorResponse(w, http.StatusUnauthorized, "invalid token")
			return
		}
		username = "cli:" + senderID
	} else {
		// Authenticate via cookie (web browser clients)
		si = wc.validateSession(r)
		if si == nil {
			jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		senderID = "web-" + strconv.Itoa(si.userID)
		// If linked to Feishu account, use Feishu identity directly.
		// This makes the web user share the same session/persona/workspace/skills/agents
		// as their Feishu account — effectively the same user.
		if si.feishuUserID != "" {
			senderID = si.feishuUserID
		}
		username = si.username
	}

	// Resolve canonical user identity (injects user_id + role for agent layer).
	var wsUserID int64
	var wsRole string
	if wc.callbacks.IdentityResolver != nil {
		resolveChannel := "web"
		if si != nil && si.feishuUserID != "" {
			resolveChannel = "feishu"
		} else if senderID == "admin" || senderID == "cli_user" {
			resolveChannel = "cli"
		}
		wsUserID, wsRole, _ = wc.callbacks.IdentityResolver.Resolve(resolveChannel, senderID)
	}

	// Upgrade to WebSocket
	conn, err := wc.wsUpgrader().Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Warn("WebSocket upgrade failed")
		return
	}

	isCLI := r.URL.Query().Get("client_type") == "cli"
	client := &Client{
		connType:        clientConnTypeWS,
		wsConn:          conn,
		sendCh:          make(chan protocol.WSMessage, webSendChBufSize),
		done:            make(chan struct{}),
		hub:             wc.hub,
		userID:          senderID,
		id:              strings.ReplaceAll(uuid.New().String(), "-", ""),
		isCLI:           isCLI,
		canonicalUserID: wsUserID,
		canonicalRole:   wsRole,
		statelessSig:    make(chan struct{}, 1),
	}

	if !wc.hub.addClient(client.id, client) {
		_ = conn.Close()
		return
	}

	// Immediately subscribe to senderID for server-pushed events (progress, stream, etc.)
	// CLI clients skip this — they subscribe to their business chatID (absolute path)
	// via an explicit "subscribe" message after connection. Subscribing CLI clients to
	// senderID ("admin") causes cross-session widget pushes to overwrite other windows.
	if !isCLI {
		chatID := senderID // p2p mode: chatID == senderID
		wc.hub.subscribe(client.id, sessionRouteKey("web", chatID))
	}

	log.WithFields(log.Fields{
		"sender_id": senderID,
		"client_id": client.id,
		"username":  username,
	}).Info("Web client connected")

	// Reconnect sync: wait for client's sync message with last_seq,
	// then replay missed events from the event stream buffer.
	// This runs in a goroutine to not block the read pump startup.
	go wc.replayMissedEvents(client, senderID)

	// Write pump
	wc.wg.Add(1)
	go func() {
		defer wc.wg.Done()
		wc.writePump(client)
	}()

	// Read pump (blocks until disconnect)
	// si is nil for CLI token auth; readPump uses it only for username lookup
	wc.readPump(client, si)
}

// validateCLIToken validates a CLI auth token and returns the associated senderID.
// Two auth methods:
//  1. Admin token (WebChannelConfig.AdminToken) — senderID = "admin", full access
//  2. Runner token — per-user token from runner_tokens table
func (wc *WebChannel) validateCLIToken(token string) (string, error) {
	if token == "" {
		return "", fmt.Errorf("empty token")
	}
	// Check admin token first
	if wc.config.AdminToken != "" && subtle.ConstantTimeCompare([]byte(token), []byte(wc.config.AdminToken)) == 1 {
		return "admin", nil
	}
	// Fall back to runner token lookup
	db := tools.GetRunnerTokenDB()
	if db == nil {
		return "", fmt.Errorf("runner token auth not available")
	}
	store := tools.NewRunnerTokenStore(db)
	userID := store.FindByTokenInRunnerTokens(token)
	if userID == "" {
		return "", fmt.Errorf("invalid token")
	}
	return userID, nil
}

// replayMissedEvents replays buffered events with seq > client's last_seq.
// Waits up to 2s for the client's sync message, then replays.
func (wc *WebChannel) replayMissedEvents(client *Client, senderID string) {
	// Resolve the user's currently active session (channel + chatID, respects chat switching)
	sel := wc.GetCurrentSession(senderID)
	chatID := sel.ChatID
	// The client sends sync immediately after WS connect.
	// If no sync arrives within 2s, send current state anyway (backward compat).
	syncCh := make(chan uint64, 1)
	client.syncCh.Store(&syncCh)
	defer client.syncCh.Store(nil)

	var fromSeq uint64
	select {
	case lastSeq := <-syncCh:
		fromSeq = lastSeq
	case <-time.After(2 * time.Second):
		// No sync message — client is old version. Send current progress snapshot.
		if wc.callbacks.GetActiveProgress != nil {
			if p := wc.callbacks.GetActiveProgress(sel.Channel, chatID); p != nil {
				select {
				case client.sendCh <- protocol.WSMessage{
					Type:     protocol.MsgTypeProgress,
					TS:       time.Now().Unix(),
					Progress: p,
				}:
				default:
				}
				if p.StreamContent != "" || p.ReasoningStreamContent != "" {
					select {
					case client.sendCh <- protocol.WSMessage{
						Type: protocol.MsgTypeStreamContent,
						TS:   time.Now().Unix(),
						Progress: &protocol.ProgressEvent{
							StreamContent:          p.StreamContent,
							ReasoningStreamContent: p.ReasoningStreamContent,
						},
					}:
					default:
					}
				}
			}
		}
		wc.enqueuePendingAskUser(client, sel.Channel, chatID)
		return
	}

	// Replay missed events from buffer. If no progress event is replayed, send the
	// current active progress snapshot once so reconnecting clients can still
	// restore an in-flight turn when their last_seq is already up to date.
	es := wc.getEventStream(sessionRouteKey(sel.Channel, chatID))
	events := es.eventsAfter(fromSeq)
	replayedProgress := false
	for _, evt := range events {
		if evt.Type == protocol.MsgTypeAskUser {
			continue
		}
		if evt.Type == protocol.MsgTypeProgress {
			replayedProgress = true
		}
		select {
		case client.sendCh <- evt:
		default:
			log.Debug("Client sendCh full during replay, stopping")
			return
		}
	}
	if !replayedProgress && wc.callbacks.GetActiveProgress != nil {
		if p := wc.callbacks.GetActiveProgress(sel.Channel, chatID); p != nil {
			select {
			case client.sendCh <- protocol.WSMessage{Type: protocol.MsgTypeProgress, TS: time.Now().Unix(), Progress: p}:
			default:
			}
		}
	}

	wc.enqueuePendingAskUser(client, sel.Channel, chatID)
}

func (wc *WebChannel) enqueuePendingAskUser(client *Client, channelName, chatID string) bool {
	if wc.callbacks.WithPendingAskUser == nil {
		return false
	}
	return wc.callbacks.WithPendingAskUser(channelName, chatID, func(pending *protocol.ProgressEvent) bool {
		select {
		case client.sendCh <- protocol.WSMessage{
			Type:     protocol.MsgTypeAskUser,
			TS:       time.Now().Unix(),
			Channel:  channelName,
			ChatID:   chatID,
			Progress: pending,
		}:
			return true
		default:
			return false
		}
	})
}

func (wc *WebChannel) writePump(c *Client) {
	defer c.wsConn.Close()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.statelessSig:
			// Drain all accumulated stateless messages (one per type — latest only).
			for _, msg := range c.drainStateless() {
				c.wsConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				if err := c.wsConn.WriteJSON(*msg); err != nil {
					log.WithError(err).Debug("WS write error (stateless)")
					return
				}
			}
		case msg, ok := <-c.sendCh:
			if !ok {
				c.wsConn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			// Internal pong — reply to client ping via single-writer goroutine.
			if msg.Type == "__pong__" {
				c.wsConn.WriteControl(websocket.PongMessage, []byte(msg.Content), time.Now().Add(5*time.Second))
				continue
			}
			_, err := wc.writeCurrentWSMessage(c, msg, func(current protocol.WSMessage) error {
				c.wsConn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				return c.wsConn.WriteJSON(current)
			})
			if err != nil {
				log.WithError(err).Debug("WS write error")
				return
			}
		case <-ticker.C:
			if err := c.wsConn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			// Server shutdown — send close frame with GoingAway status
			c.wsConn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, "server shutdown"))
			return
		}
	}
}

func (wc *WebChannel) readPump(c *Client, si *sessionInfo) {
	defer func() {
		c.wsConn.Close()
		c.closeDone()
		wc.hub.removeClient(c.id)
		// Note: do NOT removeRoutes here — multiple clients may share the same
		// senderID. Routes are idempotent and re-registered on each message.
		log.WithField("sender_id", c.userID).Info("Web client disconnected")
	}()

	c.wsConn.SetReadLimit(10 << 20) // 10MB max message (agent replies with code blocks can be large)
	c.wsConn.SetReadDeadline(time.Now().Add(120 * time.Second))
	c.wsConn.SetPongHandler(func(string) error {
		c.wsConn.SetReadDeadline(time.Now().Add(120 * time.Second))
		return nil
	})
	// Route client pings through sendCh so writePump handles the pong.
	// This avoids any direct write from readPump (no mutex needed).
	c.wsConn.SetPingHandler(func(appData string) error {
		c.wsConn.SetReadDeadline(time.Now().Add(120 * time.Second))
		select {
		case c.sendCh <- protocol.WSMessage{Type: "__pong__", Content: appData}:
		default:
		}
		return nil
	})

	// Resolve username safely (si is nil for CLI token-authed clients)
	username := "cli-remote"
	var feishuUserID string
	if si != nil {
		username = si.username
		feishuUserID = si.feishuUserID
	}

	// NOTE: chatID is NOT resolved once here. It was previously set to
	// c.userID and frozen for the lifetime of the WS connection, which
	// meant chat switching via POST /api/chats/{id}/switch had no effect
	// on WS message routing — messages went to the old (default) session.
	// Now each message handler resolves chatID dynamically via
	// wc.GetCurrentSession(c.userID) so chat switches take effect immediately.

	for {
		_, raw, err := c.wsConn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err,
				websocket.CloseGoingAway,
				websocket.CloseNormalClosure) {
				log.WithError(err).Debug("WS read error")
			}
			return
		}

		var msg protocol.WSClientMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.WithError(err).Debug("WS invalid message")
			continue
		}

		// Handle message type (default to "message" for backward compatibility)
		if msg.Type == "" {
			msg.Type = "message"
		}

		switch msg.Type {
		case protocol.MsgTypeSync:
			// Client reconnect sync: sends last_seq from history API response.
			// The replayMissedEvents goroutine is waiting on this.
			if ch := c.syncCh.Load(); ch != nil {
				lastSeq := uint64(0)
				var syncMsg struct {
					LastSeq uint64 `json:"last_seq"`
				}
				if err := json.Unmarshal(raw, &syncMsg); err == nil {
					lastSeq = syncMsg.LastSeq
				}
				select {
				case *ch <- lastSeq:
				default:
				}
			}
			continue
		case protocol.MsgTypeCancel:
			// Reuse existing /cancel mechanism: push "/cancel" text into msgBus.
			// Resolve business channel/chatID from getCurrentSession (same as message handler)
			// so the cancel key matches the one used during message processing.
			cancelSel := wc.GetCurrentSession(c.userID)
			msgChannel := cancelSel.Channel
			msgChatID := cancelSel.ChatID
			msgSenderID := c.userID
			msgSenderName := username
			if msg.Channel != "" && msg.ChatID != "" {
				msgChannel = msg.Channel
				msgChatID = msg.ChatID
				webUserID := 0
				if si != nil {
					webUserID = si.userID
				}
				if !c.isCLI && !wc.canAccessSession(context.Background(), webUserID, c.userID, msgChannel, msgChatID) {
					log.WithFields(log.Fields{"channel": msgChannel, "chat_id": msgChatID, "user_id": c.userID}).Warn("Web client cancel denied")
					continue
				}
			}
			if c.isCLI {
				if msg.SenderID != "" {
					msgSenderID = msg.SenderID
				}
				if msg.SenderName != "" {
					msgSenderName = msg.SenderName
				}
			}
			wc.msgBus.Inbound <- bus.InboundMessage{
				Channel:    msgChannel,
				SenderID:   msgSenderID,
				SenderName: msgSenderName,
				ChatID:     msgChatID,
				ChatType:   "p2p",
				Content:    "/cancel",
				Time:       time.Now(),
				RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
				From:       bus.NewIMAddress(msgChannel, msgSenderID),
			}
			continue
		case protocol.MsgTypeRPC:
			// CLI RemoteBackend RPC request — dispatch to server-side handler.
			//
			// RPC processing runs in a goroutine so readPump can continue
			// reading the next WebSocket message. Without this, a long-running
			// RPC (e.g. refresh_model_entries, which fetches /models from every
			// subscription with up to 8s timeout each) blocks readPump and
			// queues all subsequent RPCs — the CLI UI appears frozen for
			// model switches, settings, etc. until the slow RPC completes.
			//
			// Concurrency safety: each RPC carries a unique client-generated ID.
			// The response is matched by ID on the client side, so out-of-order
			// completion is safe. Dependent RPC sequences from the same caller
			// are naturally ordered because RemoteTransport.Call blocks until
			// the response arrives before the caller sends the next request.
			if wc.callbacks.RPCHandler == nil {
				continue
			}
			var rpcReq struct {
				ID     string          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(raw, &rpcReq); err != nil {
				log.WithError(err).Debug("Invalid RPC message from CLI client")
				continue
			}
			go func(id, method string, params json.RawMessage, identity RPCIdentity) {
				var result json.RawMessage
				var rpcErr error
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.WithField("method", method).
								WithField("rpc_id", id).
								WithField("stack", string(debug.Stack())).
								WithError(fmt.Errorf("%v", r)).
								Error("RPC handler panic")
							rpcErr = fmt.Errorf("internal error: %v", r)
						}
					}()
					result, rpcErr = wc.callbacks.RPCHandler(method, params, identity)
				}()
				rpcMsg := protocol.WSMessage{Type: protocol.MsgTypeRPCResponse, ID: id}
				if rpcErr != nil {
					rpcMsg.Error = rpcErr.Error()
				} else if result != nil {
					rpcMsg.Result = result
				}
				select {
				case c.sendCh <- rpcMsg:
				case <-time.After(10 * time.Second):
					log.WithField("rpc_id", id).WithField("method", method).
						Error("RPC response send timeout (10s)")
				}
			}(rpcReq.ID, rpcReq.Method, rpcReq.Params, RPCIdentity{
				SenderID:        c.userID,
				CanonicalUserID: c.canonicalUserID,
				CanonicalRole:   c.canonicalRole,
			})
			continue
		case protocol.MsgTypeSubscribe:
			// Subscribe to a business chatID so the Hub can route
			// progress/stream/outbound events to this WS client.
			var subMsg struct {
				ChatID string `json:"chat_id"`
			}
			if err := json.Unmarshal(raw, &subMsg); err != nil || subMsg.ChatID == "" {
				continue
			}
			// Authorization: web clients may subscribe to any chatID they own
			// (verified via userCurrentChat or owned chatroom list). CLI users
			// can subscribe to their business chatID (workspace path).
			if !c.isCLI {
				// Web client: allow subscribing to the user's current active chat
				// (set via POST /api/chats/{id}/switch), their default senderID,
				// or an authorized SubAgent tenant for a visible parent session.
				activeSel := wc.GetCurrentSession(c.userID)
				if subMsg.ChatID != c.userID && subMsg.ChatID != activeSel.ChatID {
					webUserID := 0
					if si != nil {
						webUserID = si.userID
					}
					channelName := "web"
					if webChatIDLooksLikeSubAgent(subMsg.ChatID) {
						channelName = "agent"
					}
					if !wc.canAccessSession(context.Background(), webUserID, c.userID, channelName, subMsg.ChatID) {
						log.WithFields(log.Fields{"client_id": c.id, "chat_id": subMsg.ChatID, "user_id": c.userID}).Warn("Hub: web client tried to subscribe to foreign chatID, denied")
						continue
					}
				}
			}
			wc.hub.subscribe(c.id, subMsg.ChatID)
			log.WithFields(log.Fields{"client_id": c.id, "chat_id": subMsg.ChatID}).Info("Hub: client subscribed to chatID")

			// Resend pending AskUser prompt if this session is waiting for user input.
			// This handles the case where the AskUser was triggered before the web
			// client subscribed (e.g. CLI session triggered AskUser, web user switches
			// to that session afterwards). GetPendingAskUser searches by chatID
			// across all channels, so we pass an empty channel to trigger fallback.
			if wc.callbacks.GetPendingAskUser != nil {
				if askP := wc.callbacks.GetPendingAskUser("", subMsg.ChatID); askP != nil {
					select {
					case c.sendCh <- protocol.WSMessage{
						Type:     protocol.MsgTypeAskUser,
						TS:       time.Now().Unix(),
						ChatID:   subMsg.ChatID,
						Progress: askP,
					}:
					default:
					}
				}
			}
		case protocol.MsgTypeTUIControlResp:
			// Remote CLI TUI control response — route to pending request handler
			if msg.TUIControl != nil && msg.ID != "" && wc.hub.tuiRespFn != nil {
				wc.hub.tuiRespFn(msg.ID, msg.TUIControl)
			}
		case protocol.MsgTypeMessage:
			if msg.Content == "" && len(msg.UploadKeys) == 0 {
				continue
			}

			var mediaPaths []string
			originalContent := msg.Content
			content := msg.Content

			// Handle OSS upload_keys: files already uploaded to cloud by frontend
			// Web uploads MUST go through OSS — local file storage is never allowed for security
			if len(msg.UploadKeys) > 0 && wc.ossProvider != nil {
				for i, key := range msg.UploadKeys {
					displayName := key
					if i < len(msg.FileNames) && msg.FileNames[i] != "" {
						displayName = filepath.Base(msg.FileNames[i])
					}
					var fileSize int64
					if i < len(msg.FileSizes) {
						fileSize = msg.FileSizes[i]
					}

					// Get signed download URL (private OSS requires signed URLs with TTL)
					downloadURL, err := wc.ossProvider.GetDownloadURL(key)
					if err != nil {
						log.WithError(err).WithField("key", key).Warn("Failed to get download URL for OSS file")
						content += fmt.Sprintf("\n\n📎 [用户上传文件: %s] (获取下载链接失败)", displayName)
						continue
					}

					ext := strings.ToLower(filepath.Ext(displayName))
					if isImageExt(ext) {
						content += fmt.Sprintf("\n\n<image url=\"%s\" name=\"%s\" size=\"%d\" />\n![%s](%s)", downloadURL, displayName, fileSize, displayName, downloadURL)
					} else {
						content += fmt.Sprintf("\n\n<file name=\"%s\" url=\"%s\" size=\"%d\" />", displayName, downloadURL, fileSize)
					}
				}
			}

			metadata := map[string]string{bus.MetadataReplyPolicy: bus.ReplyPolicyOptional}

			if feishuUserID != "" {
				metadata["feishu_user_id"] = feishuUserID
			}
			// Inject canonical user identity for agent layer
			if c.canonicalUserID > 0 {
				metadata["user_id"] = strconv.FormatInt(c.canonicalUserID, 10)
				metadata["user_role"] = c.canonicalRole
			}

			// Resolve active session (channel + chatID) — supports cross-channel browsing.
			sel := wc.GetCurrentSession(c.userID)
			msgChannel := sel.Channel
			msgSenderID := c.userID
			msgSenderName := username
			msgChatID := sel.ChatID
			msgChatType := "p2p"
			if msg.Channel != "" && msg.ChatID != "" {
				msgChannel = msg.Channel
				msgChatID = msg.ChatID
				webUserID := 0
				if si != nil {
					webUserID = si.userID
				}
				if !c.isCLI && !wc.canAccessSession(context.Background(), webUserID, c.userID, msgChannel, msgChatID) {
					log.WithFields(log.Fields{"channel": msgChannel, "chat_id": msgChatID, "user_id": c.userID}).Warn("Web client message denied")
					continue
				}
			}
			if c.isCLI {
				if msg.SenderID != "" {
					msgSenderID = msg.SenderID
				}
				if msg.SenderName != "" {
					msgSenderName = msg.SenderName
				}
				if msg.ChatType != "" {
					msgChatType = msg.ChatType
				}
			}

			// Echo back complete user message (with file info) so frontend can update optimistic message
			if content != originalContent && len(msg.UploadKeys) > 0 {
				echoMsg := protocol.WSMessage{
					Type:            "user_echo",
					Content:         content,
					OriginalContent: originalContent,
					TS:              time.Now().Unix(),
				}
				wc.hub.sendToClient(msgChatID, echoMsg)
			}

			// Subscribe this client to receive messages for this chatID.
			// Hub routes by business chatID directly — no transport metadata needed.
			// Always subscribe on every message — idempotent and handles both
			// vanilla web messages (no channel/chat_id) and CLI relay messages.
			wc.hub.subscribe(c.id, msgChatID)

			// Eagerly save user message so history API can return it during processing.
			// Skip command inputs (! and / prefixes). TUI handles slash commands
			// locally, so Web must not persist command text such as /new as chat
			// history before the agent command handler runs.
			// For remote CLI (business channel=cli), do NOT eager-save here: this web-layer
			// helper persists by web sender/chat tenant, while remote CLI history must be
			// stored under business tenant (channel=cli, chat_id=<abs cwd>) inside agent.processMessage().
			trimmed := strings.TrimSpace(content)
			if shouldEagerSaveUserMessage(msgChannel, trimmed) {
				if err := eagerSaveUserMsg(wc.db, msgChannel, msgChatID, content); err != nil {
					log.WithError(err).Warn("Failed to eager-save user message")
				}
				metadata["user_msg_eager_saved"] = "true"
			}

			wc.msgBus.Inbound <- bus.InboundMessage{
				Channel:    msgChannel,
				SenderID:   msgSenderID,
				SenderName: msgSenderName,
				ChatID:     msgChatID,
				ChatType:   msgChatType,
				Content:    content,
				Media:      mediaPaths,
				Time:       time.Now(),
				RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
				From:       bus.NewIMAddress(msgChannel, msgSenderID),
				Metadata:   metadata,
			}
		case protocol.MsgTypeAskUserResponse:
			var resp protocol.AskUserResponse
			if err := json.Unmarshal(raw, &resp); err != nil {
				log.WithError(err).Debug("WS invalid ask_user_response")
				continue
			}
			// Resolve business channel/chatID (same as message/cancel handlers)
			// so the response routes to the correct chatroom session.
			respSel := wc.GetCurrentSession(c.userID)
			respChatID := respSel.ChatID
			respChannel := respSel.Channel
			if msg.Channel != "" && msg.ChatID != "" {
				respChatID = msg.ChatID
				respChannel = msg.Channel
				webUserID := 0
				if si != nil {
					webUserID = si.userID
				}
				if !c.isCLI && !wc.canAccessSession(context.Background(), webUserID, c.userID, respChannel, respChatID) {
					log.WithFields(log.Fields{"channel": respChannel, "chat_id": respChatID, "user_id": c.userID}).Warn("Web client ask_user_response denied")
					continue
				}
			}
			if resp.Cancelled {
				// User cancelled — send /cancel equivalent
				wc.msgBus.Inbound <- bus.InboundMessage{
					Channel:    respChannel,
					SenderID:   c.userID,
					SenderName: username,
					ChatID:     respChatID,
					ChatType:   "p2p",
					Content:    "/cancel",
					Time:       time.Now(),
					RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
					From:       bus.NewIMAddress(respChannel, c.userID),
				}
			} else {
				// Format answers as indexed Q/A pairs
				var parts []string
				for idx, ans := range resp.Answers {
					parts = append(parts, fmt.Sprintf("Q%s: %s", idx, ans))
				}
				content := strings.Join(parts, "\n\n")
				wc.msgBus.Inbound <- bus.InboundMessage{
					Channel:    respChannel,
					SenderID:   c.userID,
					SenderName: username,
					ChatID:     respChatID,
					ChatType:   "p2p",
					Content:    content,
					Time:       time.Now(),
					RequestID:  strings.ReplaceAll(uuid.New().String(), "-", ""),
					From:       bus.NewIMAddress(respChannel, c.userID),
					Metadata:   map[string]string{"ask_user_answered": "true"},
				}
			}
		default:
			log.WithField("type", msg.Type).Debug("WS unknown message type")
		}
	}

}

// ---------------------------------------------------------------------------
// Security headers middleware
// ---------------------------------------------------------------------------

// securityHeadersMiddleware wraps an http.Handler with security response headers.
func (wc *WebChannel) securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Build img-src with OSS domain whitelist (if configured)
		imgSrc := "'self' data: blob:"
		if wc.ossProvider != nil {
			if d := wc.ossProvider.Domain(); d != "" {
				imgSrc += " " + d
			}
		}

		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self' 'unsafe-inline' 'unsafe-eval'; "+
				"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
				"font-src 'self' https://fonts.gstatic.com; "+
				"img-src "+imgSrc+"; "+
				"connect-src 'self' ws: wss:; "+
				"frame-ancestors 'none'",
		)
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------------------
// Static file handler
// ---------------------------------------------------------------------------

func (wc *WebChannel) handleStatic(w http.ResponseWriter, r *http.Request) {
	if wc.staticDir == "" {
		http.NotFound(w, r)
		return
	}

	path := r.URL.Path
	if path == "/" {
		path = "/index.html"
	}

	// Clean path to prevent directory traversal
	cleanPath := filepath.Clean(path)
	absPath := filepath.Join(wc.staticDir, cleanPath)

	// Ensure the resolved path is within the static directory
	absStaticDir, err := filepath.Abs(wc.staticDir)
	if err != nil {
		jsonErrorResponse(w, http.StatusInternalServerError, "internal error")
		return
	}
	absResolved, err := filepath.Abs(absPath)
	if err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid path")
		return
	}
	if !strings.HasPrefix(absResolved, absStaticDir+string(os.PathSeparator)) && absResolved != absStaticDir {
		http.NotFound(w, r)
		return
	}

	// Try exact path
	if _, err := os.Stat(absResolved); err == nil {
		http.FileServer(http.Dir(wc.staticDir)).ServeHTTP(w, r)
		return
	}

	// SPA fallback: serve index.html for non-file paths
	if !strings.Contains(path, ".") {
		r.URL.Path = "/"
		http.FileServer(http.Dir(wc.staticDir)).ServeHTTP(w, r)
		return
	}

	http.NotFound(w, r)
}

// ---------------------------------------------------------------------------
// Session cleanup
// ---------------------------------------------------------------------------

func (wc *WebChannel) sessionCleanup() {
	defer wc.wg.Done()
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-wc.stopCh:
			return
		case <-ticker.C:
			now := time.Now()
			wc.sessionsMu.Lock()
			for token, si := range wc.sessions {
				if now.After(si.expires) {
					delete(wc.sessions, token)
				}
			}
			wc.sessionsMu.Unlock()
		}
	}
}

func shouldEagerSaveUserMessage(channel, trimmedContent string) bool {
	if channel == "cli" {
		return false
	}
	return trimmedContent == "" || (trimmedContent[0] != '!' && trimmedContent[0] != '/')
}

// isImageExt returns true if the file extension is a common image format.
// eagerSaveUserMsg persists a user message to session_messages immediately
// so that a page-refresh can recover it while the backend is still processing.
func eagerSaveUserMsg(db *sql.DB, channel, chatID, content string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Ensure tenant exists before saving (first message from a new client).
	now := time.Now().Format(time.RFC3339)
	_, err = tx.Exec(`INSERT OR IGNORE INTO tenants (channel, chat_id, created_at, last_active_at) VALUES (?, ?, ?, ?)`,
		channel, chatID, now, now)
	if err != nil {
		return err
	}

	var tenantID int64
	if err := tx.QueryRow(
		"SELECT id FROM tenants WHERE channel = ? AND chat_id = ?", channel, chatID,
	).Scan(&tenantID); err != nil {
		return err
	}
	// Dedup by checking if the very last message for this tenant is an identical
	// user message saved within the last 2 seconds (handles page-refresh double-submit).
	// We do NOT dedup by content alone — users may send the same text legitimately.
	// IMPORTANT: wrap both sides in datetime() for correct comparison.
	// created_at stores RFC3339 with timezone (e.g. '2026-05-24T14:00:00+08:00'),
	// while datetime(?, '-2 seconds') returns UTC without timezone (e.g. '2026-05-24 05:59:58').
	// Raw string comparison of 'T' > ' ' makes the check always TRUE, breaking the 2s window
	// and deduping ALL duplicate-content messages regardless of time gap.
	_, err = tx.Exec(`INSERT INTO session_messages (tenant_id, role, content, created_at)
	SELECT ?, 'user', ?, ?
	WHERE NOT EXISTS (
	SELECT 1 FROM session_messages
	WHERE tenant_id = ? AND role = 'user' AND content = ?
	  AND datetime(created_at) > datetime(?, '-2 seconds')
	LIMIT 1
	)`, tenantID, content, now, tenantID, content, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// ---------------------------------------------------------------------------
