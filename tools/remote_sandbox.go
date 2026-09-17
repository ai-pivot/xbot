package tools

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"xbot/internal/runnerproto"
	"xbot/llm"
	log "xbot/logger"
)

// RemoteSandboxConfig holds configuration for creating a RemoteSandbox.
type RemoteSandboxConfig struct {
	Addr           string       // WebSocket listen address (e.g., "0.0.0.0:8080")
	AuthToken      string       // Optional shared token (legacy; runner tokens are preferred)
	AllowedOrigins []string     // Allowed WebSocket origins (empty = allow all, for development)
	RunnerStore    *RunnerStore // Runner registry + token validation
}

// RemoteSandboxSyncConfig holds directories to sync to runners on registration.
type RemoteSandboxSyncConfig struct {
	GlobalSkillDirs []string // Global skill directories (server-side)
	AgentsDir       string   // Global agents directory (server-side)
}

// runnerConnection represents a connected xbot-runner instance.
// All writes to the WebSocket go through sendCh, consumed by writePump.
type runnerConnection struct {
	wsConn     *websocket.Conn
	runnerName string // runner name from the registry
	version    string // protocol/runner version reported at registration
	workspace  string
	shell      string         // runner's default shell (e.g. /bin/bash)
	sendCh     chan sendEntry // buffered channel for serialized writes
	done       chan struct{}  // closed when writePump exits
}

// sendEntry represents a write to be sent by writePump.
type sendEntry struct {
	data []byte // TextMessage payload (nil for control-only entries)
	err  chan error
}

// RemoteSandbox implements the Sandbox interface via WebSocket communication
// with xbot-runner instances running on remote machines.
//
// Single-operator design (v69 runner collapse): connections are keyed by runner
// NAME only — there is no per-user dimension. Which runner a session uses is
// decided by the session→runner binding (SandboxRouter + tenants.runner_id).
type RemoteSandbox struct {
	runnersMu            sync.RWMutex
	runners              map[string]*runnerConnection // runnerName → live connection
	versions             map[string]string            // runnerName → reported version
	wsServer             *http.Server
	authToken            string
	addr                 string
	store                *RunnerStore
	sessionRunners       *sync.Map // shared with SandboxRouter: "channel:chatID" → runnerName
	bindingStore         SessionBindingStore
	pendingMu            sync.Mutex
	pending              map[string]chan *RunnerMessage // request ID → response channel
	upgrader             websocket.Upgrader             // per-instance upgrader with origin check
	globalSkillDirs      []string                       // global skill dirs to sync to runner on registration
	agentsDir            string                         // global agents dir to sync to runner on registration
	syncMu               sync.Mutex
	synced               map[string]bool // runnerName → whether initial sync has completed
	syncing              map[string]bool // runnerName → sync in progress (prevent concurrent syncs)
	stdioMu              sync.Mutex
	stdioStreams         map[string]*stdioStream // streamID → active stdio stream
	ptyMu                sync.Mutex
	ptyStreams           map[string]*ptyStream // streamID → active PTY stream
	OnRunnerStatusChange func(runnerName string, online bool)
	OnSyncProgress       func(runnerName string, phase string, message string)
}

// parseSandboxErrorResponse unmarshals a ProtoError body and returns a
// descriptive error. It maps the "ENOENT" code to os.ErrNotExist so callers
// can use os.IsNotExist. opName is used as a prefix (e.g. "read file").
func parseSandboxErrorResponse(raw json.RawMessage, opName string) error {
	var e ErrorResponse
	if err := json.Unmarshal(raw, &e); err != nil {
		return fmt.Errorf("%s error (raw: %s): unmarshal failed: %w", opName, string(raw), err)
	}
	if e.Code == "ENOENT" {
		return os.ErrNotExist
	}
	if e.Message == "" {
		return fmt.Errorf("%s error (raw: %s)", opName, string(raw))
	}
	return fmt.Errorf("%s: %s", opName, e.Message)
}

// NewRemoteSandbox creates and starts a RemoteSandbox server.
func NewRemoteSandbox(cfg RemoteSandboxConfig, syncCfg RemoteSandboxSyncConfig) (*RemoteSandbox, error) {
	if cfg.Addr == "" {
		cfg.Addr = "0.0.0.0:8080"
	}

	// Build per-instance upgrader with origin validation.
	var checkOrigin func(r *http.Request) bool
	if len(cfg.AllowedOrigins) == 0 {
		// No origins configured — allow all (development mode).
		checkOrigin = func(r *http.Request) bool { return true }
	} else {
		allowedSet := make(map[string]struct{}, len(cfg.AllowedOrigins))
		for _, o := range cfg.AllowedOrigins {
			allowedSet[o] = struct{}{}
		}
		checkOrigin = func(r *http.Request) bool {
			_, ok := allowedSet[r.Header.Get("Origin")]
			return ok
		}
	}

	rs := &RemoteSandbox{
		authToken: cfg.AuthToken,
		addr:      cfg.Addr,
		store:     cfg.RunnerStore,
		pending:   make(map[string]chan *RunnerMessage),
		runners:   make(map[string]*runnerConnection),
		versions:  make(map[string]string),
		upgrader: websocket.Upgrader{
			CheckOrigin: checkOrigin,
		},
		globalSkillDirs: syncCfg.GlobalSkillDirs,
		agentsDir:       syncCfg.AgentsDir,
		synced:          make(map[string]bool),
		syncing:         make(map[string]bool),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ws", rs.handleWebSocket)
	mux.HandleFunc("/ws/", rs.handleWebSocket)

	rs.wsServer = &http.Server{
		Addr:    cfg.Addr,
		Handler: mux,
	}

	go func() {
		log.Infof("RemoteSandbox WebSocket server listening on %s", cfg.Addr)
		if err := rs.wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.WithError(err).Error("RemoteSandbox server error")
		}
	}()

	return rs, nil
}

// handleWebSocket handles incoming WebSocket connections from runners.
//
// The canonical endpoint is /ws; the legacy user-scoped form /ws/{anything} is
// still accepted so existing installations keep connecting. Identity comes from
// the connect token — there is exactly one operator (v63), so no path-derived
// identity binding is required.
func (rs *RemoteSandbox) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := rs.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Error("WebSocket upgrade failed")
		return
	}

	// Set up ping/pong keep-alive.
	const (
		pongWait   = 60 * time.Second
		pingPeriod = 30 * time.Second
		writeWait  = 10 * time.Second
	)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	// Reset read deadline when we receive a pong (response to our pings).
	// Do NOT set SetPingHandler — the default auto-replies pong to the runner's pings.
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	// Read registration message
	_, raw, err := conn.ReadMessage()
	if err != nil {
		log.WithError(err).Error("Failed to read registration message")
		return
	}

	var msg RunnerMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		log.WithError(err).Error("Invalid registration message")
		return
	}
	if msg.Type != "register" {
		log.WithField("type", msg.Type).Error("Expected register message")
		return
	}

	var reg RegisterRequest
	if err := json.Unmarshal(msg.Body, &reg); err != nil {
		log.WithError(err).Error("Invalid registration body")
		return
	}
	// --- Authentication: the connect token IS the identity (single operator) ---
	authenticated := (rs.store != nil && rs.store.Validate(reg.AuthToken)) ||
		(rs.authToken != "" && subtle.ConstantTimeCompare([]byte(reg.AuthToken), []byte(rs.authToken)) == 1)
	if !authenticated {
		log.WithFields(log.Fields{
			"has_store":  rs.store != nil,
			"has_global": rs.authToken != "",
		}).Warn("Runner authentication failed")
		rs.sendRegisterError(conn, "AUTH_FAILED", "authentication failed")
		return
	}

	// Protocol version gate: refuse incompatible runners loudly instead of
	// failing mysteriously mid-session. 0 = legacy runner (accepted).
	if reg.ProtocolVersion != 0 && reg.ProtocolVersion != runnerproto.ProtocolVersion {
		log.WithFields(log.Fields{
			"runner":          reg.RunnerName,
			"runner_protocol": reg.ProtocolVersion,
			"server_protocol": runnerproto.ProtocolVersion,
		}).Warn("Runner protocol version mismatch")
		rs.sendRegisterError(conn, "PROTOCOL_MISMATCH", fmt.Sprintf(
			"runner protocol v%d is incompatible with server v%d — upgrade xbot-runner",
			reg.ProtocolVersion, runnerproto.ProtocolVersion))
		return
	}

	shell := reg.Shell
	if shell == "" {
		shell = "/bin/sh"
	}

	// Resolve the runner name: the registry is the authority. A runner that
	// connected with an unknown/shared token registers under its self-reported
	// name so it stays addressable (session bindings reference names).
	runnerName := ""
	if rs.store != nil {
		if name, ok := rs.store.FindByToken(reg.AuthToken); ok {
			runnerName = name
		}
	}
	if runnerName == "" {
		runnerName = reg.RunnerName
	}
	if runnerName == "" {
		runnerName = "default"
	}

	rc := &runnerConnection{
		wsConn:     conn,
		runnerName: runnerName,
		version:    reg.Version,
		workspace:  reg.Workspace,
		shell:      shell,
		sendCh:     make(chan sendEntry, 64),
		done:       make(chan struct{}),
	}

	// Replace any previous connection under the same name: a reconnecting runner
	// must not coexist with its stale socket (writes would go to the dead one).
	rs.runnersMu.Lock()
	old := rs.runners[runnerName]
	rs.runners[runnerName] = rc
	if reg.Version != "" {
		rs.versions[runnerName] = reg.Version
	}
	rs.runnersMu.Unlock()
	if old != nil && old != rc {
		log.WithField("runner", runnerName).Warn("Replacing stale runner connection")
		_ = old.wsConn.Close()
	}

	defer func() {
		rs.runnersMu.Lock()
		if rs.runners[runnerName] == rc {
			delete(rs.runners, runnerName)
			delete(rs.versions, runnerName)
		}
		rs.runnersMu.Unlock()
		if rs.OnRunnerStatusChange != nil {
			go rs.OnRunnerStatusChange(runnerName, false)
		}
	}()

	// Send registration acknowledgment
	okBody, err := json.Marshal(map[string]string{"status": "ok"})
	if err != nil {
		log.WithError(err).Error("Failed to marshal register_ok body")
		conn.Close()
		return
	}
	okMsg, err := json.Marshal(RunnerMessage{Type: "register_ok", Body: okBody})
	if err != nil {
		log.WithError(err).Error("Failed to marshal register_ok message")
		conn.Close()
		return
	}
	conn.WriteMessage(websocket.TextMessage, okMsg)

	log.WithFields(log.Fields{
		"runner_name": runnerName,
		"version":     reg.Version,
		"workspace":   reg.Workspace,
	}).Info("Runner connected")

	// If the runner declares LLM capability, update its record (queried by
	// injectProxyLLM to decide whether to proxy LLM calls to the runner).
	if reg.LLMProvider != "" && rs.store != nil {
		rs.store.UpdateLLM(runnerName, RunnerLLMSettings{
			Provider: reg.LLMProvider,
			Model:    reg.LLMModel,
			// APIKey and BaseURL are not needed here — the runner holds them locally
		})
		log.WithFields(log.Fields{
			"runner_name":  runnerName,
			"llm_provider": reg.LLMProvider,
			"llm_model":    reg.LLMModel,
		}).Info("Runner LLM capability recorded")
	}

	// Notify runner status change
	if rs.OnRunnerStatusChange != nil {
		go rs.OnRunnerStatusChange(runnerName, true)
	}

	// Single writer goroutine: handles both request writes and ping heartbeats.
	go rs.writePump(rc, pingPeriod, writeWait)

	// Sync global skills and agents to the runner in the background
	go rs.syncToRunner(runnerName, reg.Workspace)

	// Keep reading messages (responses, heartbeats, and stdio push messages)
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			log.WithError(err).WithFields(log.Fields{
				"user_id":     reg.UserID,
				"runner_name": runnerName,
			}).Debug("Runner disconnected")
			return
		}
		var resp RunnerMessage
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		// Handle push messages (stdio, pty) before request matching.
		if rs.handleStdioPush(&resp) {
			continue
		}
		if rs.handlePtyPush(&resp) {
			continue
		}
		if resp.ID != "" {
			rs.pendingMu.Lock()
			if ch, ok := rs.pending[resp.ID]; ok {
				select {
				case ch <- &resp:
				default:
					log.WithField("request_id", resp.ID).Warn("Runner: pending channel full, response dropped")
				}
				delete(rs.pending, resp.ID)
			}
			rs.pendingMu.Unlock()
		}
	}

}

// getRunner resolves the connection for a routing key (session key).
func (rs *RemoteSandbox) getRunner(routingKey string) (*runnerConnection, error) {
	return rs.getRunnerForSession(routingKey)
}

// getRunnerForSession resolves the live connection for a session.
//
// Resolution order (single-operator design — no user dimension):
//  1. the session's own binding (authoritative: tenants.runner_id, mirrored in
//     sessionRunners);
//  2. for session-less calls, the only connected runner when exactly one exists.
//
// It never guesses between multiple machines: an ambiguous or offline binding is
// an explicit error, not a silent fallback.
func (rs *RemoteSandbox) getRunnerForSession(sessionKey string) (*runnerConnection, error) {
	rs.runnersMu.RLock()
	defer rs.runnersMu.RUnlock()

	runnerName := ""
	if rs.sessionRunners != nil && sessionKey != "" {
		if v, ok := rs.sessionRunners.Load(sessionKey); ok {
			if name, _ := v.(string); name != "" {
				runnerName = name
			}
		}
	}
	if runnerName == "" {
		if len(rs.runners) == 1 {
			for name := range rs.runners {
				runnerName = name
			}
		} else if len(rs.runners) == 0 {
			return nil, fmt.Errorf("no runner connected")
		} else {
			return nil, fmt.Errorf("session %q is not bound to a runner and %d runners are connected — bind one first",
				sessionKey, len(rs.runners))
		}
	}
	rc, ok := rs.runners[runnerName]
	if !ok {
		return nil, fmt.Errorf("runner %q is not connected", runnerName)
	}
	return rc, nil
}

// IsRunnerOnline reports whether the named runner is connected.
func (rs *RemoteSandbox) IsRunnerOnline(runnerName string) bool {
	rs.runnersMu.RLock()
	defer rs.runnersMu.RUnlock()
	_, ok := rs.runners[runnerName]
	return ok
}

// RunnerVersion returns the version reported by the runner at registration.
func (rs *RemoteSandbox) RunnerVersion(runnerName string) string {
	rs.runnersMu.RLock()
	defer rs.runnersMu.RUnlock()
	return rs.versions[runnerName]
}

// OnlineRunnerNames lists currently connected runner names.
func (rs *RemoteSandbox) OnlineRunnerNames() []string {
	rs.runnersMu.RLock()
	defer rs.runnersMu.RUnlock()
	names := make([]string, 0, len(rs.runners))
	for name := range rs.runners {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetConnectionInfo returns the workspace and shell reported by the runner.
// Empty strings when the runner is not connected.
func (rs *RemoteSandbox) GetConnectionInfo(runnerName string) (workspace, shell string) {
	rs.runnersMu.RLock()
	defer rs.runnersMu.RUnlock()
	rc, ok := rs.runners[runnerName]
	if !ok {
		return "", ""
	}
	return rc.workspace, rc.shell
}

// sendRequest sends a request to the runner and waits for a response.
func (rs *RemoteSandbox) sendRequest(ctx context.Context, rc *runnerConnection, msg *RunnerMessage, timeout time.Duration) (*RunnerMessage, error) {
	rs.pendingMu.Lock()
	ch := make(chan *RunnerMessage, 1)
	rs.pending[msg.ID] = ch
	rs.pendingMu.Unlock()

	defer func() {
		rs.pendingMu.Lock()
		delete(rs.pending, msg.ID)
		rs.pendingMu.Unlock()
	}()

	data, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	// Send through the single writer goroutine.
	errCh := make(chan error, 1)
	select {
	case rc.sendCh <- sendEntry{data: data, err: errCh}:
	case <-rc.done:
		return nil, fmt.Errorf("runner disconnected")
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	if err = <-errCh; err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(timeout):
		return nil, fmt.Errorf("request %s timed out after %v", msg.ID, timeout)
	}
}

// sendOnly sends a message to the runner without waiting for a response.
func (rs *RemoteSandbox) sendOnly(rc *runnerConnection, msg *RunnerMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	errCh := make(chan error, 1)
	select {
	case rc.sendCh <- sendEntry{data: data, err: errCh}:
	case <-rc.done:
		return fmt.Errorf("runner disconnected")
	}
	return <-errCh
}

// writePump is the single writer goroutine for a runner connection.
// It consumes entries from rc.sendCh (text messages from sendRequest) and sends
// periodic pings to detect dead connections.
func (rs *RemoteSandbox) writePump(rc *runnerConnection, pingPeriod, writeWait time.Duration) {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		rc.wsConn.Close()
		close(rc.done)
	}()

	for {
		select {
		case entry := <-rc.sendCh:
			if entry.data != nil {
				err := rc.wsConn.WriteMessage(websocket.TextMessage, entry.data)
				if entry.err != nil {
					entry.err <- err
				}
				if err != nil {
					return
				}
			}
		case <-ticker.C:
			if err := rc.wsConn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait)); err != nil {
				log.WithError(err).WithField("runner", rc.runnerName).Debug("Ping to runner failed")
				return
			}
		}
	}
}

// sendRegisterError sends a registration error to the runner and closes the connection.
func (rs *RemoteSandbox) sendRegisterError(conn *websocket.Conn, code, message string) {
	errBody, err := json.Marshal(ErrorResponse{Code: code, Message: message})
	if err != nil {
		log.WithError(err).Error("Failed to marshal error response")
		conn.Close()
		return
	}
	errMsg, err := json.Marshal(RunnerMessage{Type: "error", Body: errBody})
	if err != nil {
		log.WithError(err).Error("Failed to marshal error message")
		conn.Close()
		return
	}
	conn.WriteMessage(websocket.TextMessage, errMsg)
	conn.Close()
}

// generateID generates a unique request ID.
func generateID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("req_%x", b)
}

// === Sandbox Interface Implementation ===

func (rs *RemoteSandbox) Name() string { return "remote" }

// SetRunnerStore sets or replaces the runner registry (token validation + names).
func (rs *RemoteSandbox) SetRunnerStore(store *RunnerStore) {
	rs.runnersMu.Lock()
	rs.store = store
	rs.runnersMu.Unlock()
}

// SetSessionBindingStore wires the session→runner binding persistence (shared
// with the SandboxRouter).
func (rs *RemoteSandbox) SetSessionBindingStore(store SessionBindingStore) {
	rs.runnersMu.Lock()
	rs.bindingStore = store
	rs.runnersMu.Unlock()
}

// Workspace returns the connected runner's workspace root ("" when none).
func (rs *RemoteSandbox) Workspace(_ string) string {
	rc, err := rs.getRunnerForSession("")
	if err != nil {
		return ""
	}
	return rc.workspace
}

func (rs *RemoteSandbox) Close() error {
	return rs.wsServer.Close()
}

// CloseForUser closes every runner connection (legacy name; connections are
// global in the single-operator design).
func (rs *RemoteSandbox) CloseForUser(_ string) error {
	rs.runnersMu.Lock()
	conns := rs.runners
	rs.runners = make(map[string]*runnerConnection)
	rs.versions = make(map[string]string)
	rs.runnersMu.Unlock()
	for _, rc := range conns {
		_ = rc.wsConn.Close()
	}
	return nil
}

// DisconnectRunner closes a specific runner connection by name.
func (rs *RemoteSandbox) DisconnectRunner(runnerName string) bool {
	rs.runnersMu.Lock()
	rc, ok := rs.runners[runnerName]
	if ok {
		delete(rs.runners, runnerName)
		delete(rs.versions, runnerName)
	}
	rs.runnersMu.Unlock()
	if !ok {
		return false
	}
	_ = rc.wsConn.Close()
	return true
}

func (rs *RemoteSandbox) ExportAndImport(_ string) error { return nil }

// GetShell returns the connected runner's default shell ("/bin/sh" when none).
func (rs *RemoteSandbox) GetShell(_, _ string) (string, error) {
	rc, err := rs.getRunnerForSession("")
	if err != nil {
		return "/bin/sh", nil
	}
	return rc.shell, nil
}

// LLMGenerate sends an LLM generation request to the runner and returns the response.
// This is used by ProxyLLM to forward LLM calls to runners with local LLM configured.
func (rs *RemoteSandbox) LLMGenerate(ctx context.Context, model string, messages []llm.ChatMessage, tools []llm.ToolDefinition, thinkingMode string) (*llm.LLMResponse, error) {
	rc, err := rs.getRunnerForSession("")
	if err != nil {
		return nil, err
	}

	reqBody, err := json.Marshal(llm.LLMProxyRequest{
		Model:        model,
		Messages:     messages,
		Tools:        llm.SerializeTools(tools),
		ThinkingMode: thinkingMode,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	msg := &RunnerMessage{
		ID:   generateID(),
		Type: runnerproto.ProtoLLMGenerate,
		Body: reqBody,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, llm.ProxyRequestTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "llm_generate")
	}

	var result llm.LLMResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal llm_generate result: %w", err)
	}

	return &result, nil
}

// LLMModels queries available models from the runner's local LLM.
func (rs *RemoteSandbox) LLMModels(ctx context.Context) ([]string, error) {
	rc, err := rs.getRunnerForSession("")
	if err != nil {
		return nil, err
	}

	msg := &RunnerMessage{
		ID:   generateID(),
		Type: runnerproto.ProtoLLMModels,
	}

	resp, err := rs.sendRequest(ctx, rc, msg, defaultRequestTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == ProtoError {
		return nil, parseSandboxErrorResponse(resp.Body, "llm_models")
	}

	var result llm.LLMListModelsResponse
	if err := json.Unmarshal(resp.Body, &result); err != nil {
		return nil, fmt.Errorf("unmarshal llm_models result: %w", err)
	}

	return result.Models, nil
}

// === Runner sync (server → runner file sync on registration) ===

// syncToRunner syncs global skills and agents from the server to the runner.
// Runs in a background goroutine; errors are logged but not fatal.
func (rs *RemoteSandbox) syncToRunner(runnerName, workspace string) {
	if workspace == "" {
		log.WithField("runner", runnerName).Warn("syncToRunner: workspace is empty, skipping sync")
		return
	}

	rs.syncMu.Lock()
	rs.syncing[runnerName] = true
	rs.syncMu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), RemoteSandboxSyncTimeout)
	defer cancel()

	log.WithFields(log.Fields{
		"runner":            runnerName,
		"workspace":         workspace,
		"global_skill_dirs": rs.globalSkillDirs,
		"agents_dir":        rs.agentsDir,
	}).Info("syncToRunner: starting sync")

	// Notify sync start
	if rs.OnSyncProgress != nil {
		rs.OnSyncProgress(runnerName, "start", "正在同步 skills 和 agents...")
	}

	// Sync each global skill directory
	for _, skillDir := range rs.globalSkillDirs {
		dstDir := filepath.Join(workspace, "skills")
		rs.syncDirToRunner(ctx, runnerName, workspace, skillDir, dstDir)
	}

	// Sync embedded skills (skipped if external version already exists)
	dstSkillsDir := filepath.Join(workspace, "skills")
	for _, name := range ListEmbeddedSkills() {
		rs.syncEmbeddedSkillToRunner(ctx, runnerName, workspace, name, dstSkillsDir)
	}

	// Sync global agents
	if rs.agentsDir != "" {
		dstDir := filepath.Join(workspace, "agents")
		rs.syncAgentsToRunner(ctx, runnerName, workspace, rs.agentsDir, dstDir)
	}

	// Sync embedded agents (skipped if external version already exists)
	dstAgentsDir := filepath.Join(workspace, "agents")
	for _, name := range ListEmbeddedAgents() {
		rs.syncEmbeddedAgentToRunner(ctx, runnerName, workspace, name, dstAgentsDir)
	}

	log.WithFields(log.Fields{
		"runner":    runnerName,
		"workspace": workspace,
	}).Info("Runner sync completed")

	// Notify sync done
	if rs.OnSyncProgress != nil {
		rs.OnSyncProgress(runnerName, "done", "同步完成")
	}
	// Mark sync as completed (even if some dirs failed, we don't retry individual failures)
	rs.syncMu.Lock()
	rs.synced[runnerName] = true
	rs.syncing[runnerName] = false
	rs.syncMu.Unlock()
}

// EnsureSynced implements SandboxSyncer interface.
// If the runner hasn't been synced yet (or sync failed), triggers a sync.
// This is called from EnsureSynced(ctx) in skill_sync.go.
func (rs *RemoteSandbox) EnsureSynced(ctx context.Context, runnerName string) {
	rs.syncMu.Lock()
	if rs.synced[runnerName] {
		rs.syncMu.Unlock()
		return
	}
	// If sync is already in progress, wait for it
	if rs.syncing[runnerName] {
		rs.syncMu.Unlock()
		// Poll every 500ms, up to 30s
		for i := 0; i < 60; i++ {
			time.Sleep(500 * time.Millisecond)
			rs.syncMu.Lock()
			if rs.synced[runnerName] {
				rs.syncMu.Unlock()
				return
			}
			rs.syncMu.Unlock()
		}
		log.WithField("runner", runnerName).Warn("EnsureSynced: timed out waiting for in-progress sync")
		return
	}
	rs.syncMu.Unlock()

	// Get runner workspace
	rc, err := rs.getRunnerForSession("")
	if err != nil {
		log.WithError(err).WithField("runner", runnerName).Debug("EnsureSynced: no runner connected, skipping sync")
		return
	}

	log.WithField("runner", runnerName).Info("EnsureSynced: triggering on-demand sync")
	go rs.syncToRunner(runnerName, rc.workspace)
}

// syncDirToRunner recursively syncs a skill directory tree from the server to the runner.
// Each skill is a subdirectory; only directories containing SKILL.md are synced.
func (rs *RemoteSandbox) syncDirToRunner(ctx context.Context, runnerName, workspace, srcDir, dstSubdir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).WithField("dir", srcDir).Warn("syncToRunner: failed to read source dir")
		return
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(srcDir, e.Name())
		skillFile := filepath.Join(skillDir, "SKILL.md")
		if _, err := os.Stat(skillFile); err != nil {
			continue // not a valid skill (no SKILL.md)
		}
		dstDir := filepath.Join(dstSubdir, e.Name())
		rs.syncTreeToRunner(ctx, runnerName, skillDir, dstDir)
	}
}

// syncAgentsToRunner syncs .md agent files from the server's agents dir to the runner.
func (rs *RemoteSandbox) syncAgentsToRunner(ctx context.Context, runnerName, workspace, srcDir, dstSubdir string) {
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		log.WithError(err).WithField("dir", srcDir).Warn("syncToRunner: failed to read agents dir")
		return
	}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(dstSubdir, e.Name())
		rs.syncFileToRunner(ctx, runnerName, srcPath, dstPath)
	}
}

// syncTreeToRunner recursively syncs a directory from the server to the runner.
func (rs *RemoteSandbox) syncTreeToRunner(ctx context.Context, runnerName, srcDir, dstDir string) {
	if err := rs.MkdirAll(ctx, dstDir, 0o755, runnerName); err != nil {
		log.WithError(err).WithFields(log.Fields{"src": srcDir, "dst": dstDir}).Warn("syncTree: mkdir failed")
		return
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		log.WithError(err).WithField("dir", srcDir).Warn("syncTree: read failed")
		return
	}

	for _, e := range entries {
		srcPath := filepath.Join(srcDir, e.Name())
		dstPath := filepath.Join(dstDir, e.Name())
		if e.IsDir() {
			rs.syncTreeToRunner(ctx, runnerName, srcPath, dstPath)
		} else {
			rs.syncFileToRunner(ctx, runnerName, srcPath, dstPath)
		}
	}
}

// syncFileToRunner reads a local file and writes it to the runner.
func (rs *RemoteSandbox) syncFileToRunner(ctx context.Context, runnerName, srcPath, dstPath string) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		log.WithError(err).WithField("file", srcPath).Warn("syncFile: read failed")
		return
	}
	if err := rs.WriteFile(ctx, dstPath, data, 0o644, runnerName); err != nil {
		log.WithError(err).WithFields(log.Fields{"src": srcPath, "dst": dstPath}).Warn("syncFile: write failed")
	}
}

// syncEmbeddedSkillToRunner syncs a single embedded skill to the runner.
// Skips if the skill directory already exists on the runner.
// Recursively syncs subdirectories (supports multi-file embed skills).
func (rs *RemoteSandbox) syncEmbeddedSkillToRunner(ctx context.Context, runnerName, workspace, skillName, dstSkillsDir string) {
	dstDir := filepath.Join(dstSkillsDir, skillName)
	// Check if already exists on runner
	if _, err := rs.Stat(ctx, dstDir, runnerName); err == nil {
		return // already exists
	}
	// Recursively walk the embedded skill directory
	skillDir := path.Join("embed_skills", skillName)
	err := fs.WalkDir(EmbeddedSkills, skillDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip on error
		}
		// embed FS always uses forward slashes; strings.TrimPrefix is
		// semantically correct without OS dependency.
		rel := strings.TrimPrefix(p, skillDir+"/")
		dstPath := filepath.Join(dstDir, rel)
		if d.IsDir() {
			if err := rs.MkdirAll(ctx, dstPath, 0o755, runnerName); err != nil {
				log.WithError(err).Warn("syncEmbeddedSkill: mkdir failed")
			}
			return nil
		}
		data, err := EmbeddedSkills.ReadFile(p)
		if err != nil {
			return nil
		}
		if err := rs.WriteFile(ctx, dstPath, data, 0o644, runnerName); err != nil {
			log.WithError(err).Warn("syncEmbeddedSkill: write failed")
		}
		return nil
	})
	if err != nil {
		log.WithError(err).Warn("syncEmbeddedSkill: walk failed")
	}
}

// syncEmbeddedAgentToRunner syncs a single embedded agent to the runner.
// Skips if the agent file already exists on the runner.
func (rs *RemoteSandbox) syncEmbeddedAgentToRunner(ctx context.Context, runnerName, workspace, agentName, dstAgentsDir string) {
	dstPath := filepath.Join(dstAgentsDir, agentName+".md")
	// Check if already exists on runner
	if _, err := rs.Stat(ctx, dstPath, runnerName); err == nil {
		return // already exists
	}
	data, err := ReadEmbeddedAgentFile(agentName)
	if err != nil {
		return
	}
	if err := rs.MkdirAll(ctx, dstAgentsDir, 0o755, runnerName); err != nil {
		log.WithError(err).Warn("syncEmbeddedAgent: mkdir failed")
		return
	}
	if err := rs.WriteFile(ctx, dstPath, data, 0o644, runnerName); err != nil {
		log.WithError(err).Warn("syncEmbeddedAgent: write failed")
	}
}
