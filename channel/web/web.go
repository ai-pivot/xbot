// xbot Web Channel implementation

package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"xbot/bus"
	ch "xbot/channel"
	log "xbot/logger"
)

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
}

type sessionInfo struct {
	userID       int
	username     string
	feishuUserID string // non-empty when logged in via Feishu identity
	expires      time.Time
}

// Compile-time interface assertion.
var _ ch.SessionStateSender = (*WebChannel)(nil)

// NewWebChannel 创建 Web 渠道
func NewWebChannel(cfg WebChannelConfig, msgBus *bus.MessageBus) *WebChannel {
	wc := &WebChannel{
		config:             cfg,
		msgBus:             msgBus,
		hub:                newHub(),
		sessions:           make(map[string]sessionInfo),
		db:                 cfg.DB,
		stopCh:             make(chan struct{}),
		userCurrentSession: make(map[string]SessionSelector),
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
