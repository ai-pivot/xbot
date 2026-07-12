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
func (wc *WebChannel) SetRPCHandler(fn func(method string, params json.RawMessage, senderID string) (json.RawMessage, error)) {
	wc.callbacks.RPCHandler = fn
}

func (wc *WebChannel) Name() string { return "web" }

// ---------------------------------------------------------------------------
// Start / Stop
// ---------------------------------------------------------------------------

// Start 启动 Web 渠道 HTTP server
func (wc *WebChannel) Start() error {
	mux := http.NewServeMux()

	// WebSocket endpoint
	mux.HandleFunc("/ws", wc.handleWS)
	// Server-sent events endpoint for browser push notifications.
	mux.HandleFunc("/api/sse", wc.authMiddleware(wc.handleSSE))

	// Auth API
	mux.HandleFunc("/api/auth/register", limitBodySize(wc.handleRegister))
	mux.HandleFunc("/api/auth/login", limitBodySize(wc.handleLogin))
	mux.HandleFunc("/api/auth/logout", wc.handleLogout)
	mux.HandleFunc("/api/auth/feishu-link", limitBodySize(wc.handleFeishuLink))
	mux.HandleFunc("/api/auth/feishu-login", limitBodySize(wc.handleFeishuLogin))
	mux.HandleFunc("/api/auth/config", wc.handleAuthConfig)

	// REST API
	mux.HandleFunc("/api/history", wc.authMiddleware(wc.handleHistory))
	mux.HandleFunc("/api/history/rewind", wc.authMiddleware(wc.handleHistoryRewind))
	mux.HandleFunc("/api/settings", wc.authMiddleware(wc.handleSettings))
	mux.HandleFunc("/api/runner/token", wc.authMiddleware(wc.handleRunnerToken))
	mux.HandleFunc("/api/search", wc.authMiddleware(wc.handleSearch))
	mux.HandleFunc("/api/cwd", wc.authMiddleware(wc.handleCWD))
	mux.HandleFunc("/api/tasks", wc.authMiddleware(wc.handleTasks))
	mux.HandleFunc("/api/background-tasks", wc.authMiddleware(wc.handleBackgroundTasks))
	mux.HandleFunc("/api/commands", wc.authMiddleware(wc.handleCommands))
	mux.HandleFunc("/api/session-subscription", wc.authMiddleware(wc.handleSessionSubscription))

	// Multi-runner API
	mux.HandleFunc("/api/runners", wc.authMiddleware(wc.handleRunners))
	mux.HandleFunc("/api/runners/active", wc.authMiddleware(wc.handleRunnerActive))
	mux.HandleFunc("/api/runners/{name}", wc.authMiddleware(wc.handleRunnerByName))

	// LLM Config API
	mux.HandleFunc("/api/llm-config", wc.authMiddleware(wc.handleLLMConfig))
	mux.HandleFunc("/api/llm-config/model", wc.authMiddleware(wc.handleLLMModelSet))
	mux.HandleFunc("/api/llm-max-context", wc.authMiddleware(wc.handleLLMMaxContext))

	// File API
	mux.HandleFunc("/api/files/upload", wc.authMiddleware(wc.handleFileUpload))

	// File System API (browse, read, search, stat)
	mux.HandleFunc("/api/fs/list", wc.authMiddleware(wc.handleFsList))
	mux.HandleFunc("/api/fs/read", wc.authMiddleware(wc.handleFsRead))
	mux.HandleFunc("/api/fs/raw", wc.authMiddleware(wc.handleFsRaw))
	mux.HandleFunc("/api/fs/search", wc.authMiddleware(wc.handleFsSearch))
	mux.HandleFunc("/api/fs/stat", wc.authMiddleware(wc.handleFsStat))

	// Chatroom API
	mux.HandleFunc("/api/chats", wc.authMiddleware(wc.handleChats))
	mux.HandleFunc("/api/session-tree", wc.authMiddleware(wc.handleSessionTree))
	mux.HandleFunc("/api/subagents", wc.authMiddleware(wc.handleSubAgents))
	mux.HandleFunc("/api/chats/{chatID}/switch", wc.authMiddleware(wc.handleChatSwitch))
	mux.HandleFunc("/api/chats/{chatID}/rename", wc.authMiddleware(wc.handleChatRename))
	mux.HandleFunc("/api/chats/{chatID}", wc.authMiddleware(wc.handleChatDelete))

	// Cross-channel browsing API
	mux.HandleFunc("/api/context-info", wc.authMiddleware(wc.handleContextInfo))
	mux.HandleFunc("/api/channels", wc.authMiddleware(wc.handleChannels))

	// Account linking + identity management API
	mux.HandleFunc("/api/account/link-code", wc.authMiddleware(wc.handleLinkCode))
	mux.HandleFunc("/api/account/link", wc.authMiddleware(wc.handleLink))
	mux.HandleFunc("/api/account/identities", wc.authMiddleware(wc.handleIdentities))
	mux.HandleFunc("/api/account/identities/{id}", wc.authMiddleware(wc.handleUnlinkIdentity))

	// Admin management API
	mux.HandleFunc("/api/admin/users", wc.authMiddleware(wc.handleAdminUsers))
	mux.HandleFunc("/api/admin/users/{id}/role", wc.authMiddleware(wc.handleAdminSetRole))

	// Static files
	if wc.staticDir != "" {
		mux.HandleFunc("/", wc.handleStatic)
	}

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
