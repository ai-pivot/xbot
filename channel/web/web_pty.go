package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"

	log "xbot/logger"
	"xbot/tools"
)

// ---------------------------------------------------------------------------
// PtyBackend — abstracts PTY location (server vs runner)
// ---------------------------------------------------------------------------

// PtyBackend abstracts where the PTY actually runs.
// - localPtyBackend: PTY on the web server (none sandbox)
// - remotePtyBackend: PTY on the runner (remote/docker sandbox)
type PtyBackend interface {
	// Create starts a PTY process and returns a streamID for the backend.
	Create(cwd string, cols, rows uint16) (streamID string, err error)
	// WriteStdin writes bytes to the PTY's stdin.
	WriteStdin(streamID string, data []byte) error
	// Resize changes the PTY's window size.
	Resize(streamID string, cols, rows uint16) error
	// Close destroys the PTY.
	Close(streamID string) error
	// SetHandlers registers onData/onExit callbacks for the stream.
	SetHandlers(streamID string, onData func([]byte), onExit func(int))
}

// ---------------------------------------------------------------------------
// localPtyBackend — PTY on the web server (none sandbox)
// ---------------------------------------------------------------------------

type localPtyBackend struct {
	senderID string
	shell    string
	procs    sync.Mutex
	streams  map[string]*localPtyProc // streamID → proc
}

type localPtyProc struct {
	ptmx *os.File
	cmd  *exec.Cmd
	done chan struct{}
}

func newLocalPtyBackend(senderID, shell string) *localPtyBackend {
	return &localPtyBackend{
		senderID: senderID,
		shell:    shell,
		streams:  make(map[string]*localPtyProc),
	}
}

func (b *localPtyBackend) Create(cwd string, cols, rows uint16) (string, error) {
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}
	shell := b.shell
	if shell == "" {
		shell = "bash"
	}
	cmd := exec.Command(shell)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ws := &pty.Winsize{Cols: cols, Rows: rows}
	ptmx, err := pty.StartWithSize(cmd, ws)
	if err != nil {
		return "", fmt.Errorf("start pty: %w", err)
	}

	streamID := generateTID()
	proc := &localPtyProc{ptmx: ptmx, cmd: cmd, done: make(chan struct{})}
	b.procs.Lock()
	b.streams[streamID] = proc
	b.procs.Unlock()

	return streamID, nil
}

func (b *localPtyBackend) WriteStdin(streamID string, data []byte) error {
	b.procs.Lock()
	proc, ok := b.streams[streamID]
	b.procs.Unlock()
	if !ok {
		return fmt.Errorf("stream not found: %s", streamID)
	}
	_, err := proc.ptmx.Write(data)
	return err
}

func (b *localPtyBackend) Resize(streamID string, cols, rows uint16) error {
	b.procs.Lock()
	proc, ok := b.streams[streamID]
	b.procs.Unlock()
	if !ok {
		return fmt.Errorf("stream not found: %s", streamID)
	}
	return pty.Setsize(proc.ptmx, &pty.Winsize{Cols: cols, Rows: rows})
}

func (b *localPtyBackend) Close(streamID string) error {
	b.procs.Lock()
	proc, ok := b.streams[streamID]
	delete(b.streams, streamID)
	b.procs.Unlock()
	if !ok {
		return nil
	}
	proc.ptmx.Close()
	select {
	case <-proc.done:
	case <-time.After(5 * time.Second):
		if proc.cmd.Process != nil {
			proc.cmd.Process.Kill() //nolint:errcheck
		}
	}
	return nil
}

func (b *localPtyBackend) SetHandlers(streamID string, onData func([]byte), onExit func(int)) {
	b.procs.Lock()
	proc, ok := b.streams[streamID]
	b.procs.Unlock()
	if !ok {
		return
	}

	// Read loop: ptmx → onData callback.
	go func() {
		defer close(proc.done)
		buf := make([]byte, 32*1024)
		for {
			n, err := proc.ptmx.Read(buf)
			if n > 0 && onData != nil {
				onData(append([]byte{}, buf[:n]...))
			}
			if err != nil {
				break
			}
		}
	}()

	// Wait loop: cmd.Wait → onExit callback.
	go func() {
		exitCode := 0
		if err := proc.cmd.Wait(); err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				exitCode = exitErr.ExitCode()
			} else {
				exitCode = -1
			}
		}
		if onExit != nil {
			onExit(exitCode)
		}
	}()
}

// ---------------------------------------------------------------------------
// remotePtyBackend — PTY on the runner (remote/docker sandbox)
// ---------------------------------------------------------------------------

type remotePtyBackend struct {
	sandbox  *tools.RemoteSandbox
	senderID string
}

func (b *remotePtyBackend) Create(cwd string, cols, rows uint16) (string, error) {
	streamID := generateTID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.sandbox.PtyCreate(ctx, b.senderID, streamID, "", cwd, cols, rows); err != nil {
		return "", err
	}
	return streamID, nil
}

func (b *remotePtyBackend) WriteStdin(streamID string, data []byte) error {
	return b.sandbox.PtyStdin(b.senderID, streamID, data)
}

func (b *remotePtyBackend) Resize(streamID string, cols, rows uint16) error {
	return b.sandbox.PtyResize(b.senderID, streamID, cols, rows)
}

func (b *remotePtyBackend) Close(streamID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return b.sandbox.PtyClose(ctx, b.senderID, streamID)
}

func (b *remotePtyBackend) SetHandlers(streamID string, onData func([]byte), onExit func(int)) {
	b.sandbox.SetPtyHandlers(streamID, onData, onExit)
}

// ---------------------------------------------------------------------------
// ptyManager — session lifecycle + multi-client broadcast
// ---------------------------------------------------------------------------

const (
	ptyHistoryCap   = 64 * 1024 // 64KB scrollback
	ptyIdleTimeout  = 5 * time.Minute
	ptyReapInterval = 30 * time.Second
)

type ptySession struct {
	tid       string
	backend   PtyBackend
	streamID  string
	senderID  string
	chatID    string
	cwd       string
	createdAt time.Time

	mu       sync.Mutex
	clients  map[*ptyWSClient]struct{}
	history  []byte
	closed   bool
	lastSeen time.Time
}

type ptyWSClient struct {
	conn *websocket.Conn
	send chan []byte // outbound messages (JSON-encoded)
}

type ptyManager struct {
	mu       sync.Mutex
	sessions map[string]*ptySession // tid → session
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

func newPtyManager() *ptyManager {
	return &ptyManager{
		sessions: make(map[string]*ptySession),
		stopCh:   make(chan struct{}),
	}
}

func (m *ptyManager) Start() {
	m.wg.Add(1)
	go m.idleReaper()
}

func (m *ptyManager) Stop() {
	close(m.stopCh)
	m.wg.Wait()
	m.mu.Lock()
	for tid, sess := range m.sessions {
		sess.backend.Close(sess.streamID)
		delete(m.sessions, tid)
	}
	m.mu.Unlock()
}

// selectBackend resolves the user's sandbox and picks the right PTY backend.
func (m *ptyManager) selectBackend(senderID string) (PtyBackend, error) {
	sandbox := tools.GetSandbox()
	if sandbox == nil {
		return nil, fmt.Errorf("no sandbox available")
	}

	// Try per-user resolution (SandboxRouter).
	resolver, ok := sandbox.(tools.SandboxResolver)
	if ok {
		userSbx := resolver.SandboxForUser(senderID)
		if userSbx == nil {
			return nil, fmt.Errorf("no sandbox for user %s", senderID)
		}
		// Remote sandbox → PTY on runner.
		if rs, ok := userSbx.(*tools.RemoteSandbox); ok {
			return &remotePtyBackend{sandbox: rs, senderID: senderID}, nil
		}
		// None sandbox → PTY on server.
		shell, _ := userSbx.GetShell(senderID, userSbx.Workspace(senderID))
		return newLocalPtyBackend(senderID, shell), nil
	}

	// Fallback: use sandbox directly.
	shell, _ := sandbox.GetShell(senderID, sandbox.Workspace(senderID))
	return newLocalPtyBackend(senderID, shell), nil
}

// Create creates a new PTY session.
func (m *ptyManager) Create(senderID, chatID, cwd string, cols, rows uint16) (string, error) {
	backend, err := m.selectBackend(senderID)
	if err != nil {
		return "", err
	}

	streamID, err := backend.Create(cwd, cols, rows)
	if err != nil {
		return "", err
	}

	tid := generateTID()
	sess := &ptySession{
		tid:       tid,
		backend:   backend,
		streamID:  streamID,
		senderID:  senderID,
		chatID:    chatID,
		cwd:       cwd,
		createdAt: time.Now(),
		clients:   make(map[*ptyWSClient]struct{}),
		lastSeen:  time.Now(),
	}

	// Wire backend → broadcast.
	backend.SetHandlers(streamID,
		func(data []byte) { sess.broadcast(data) },
		func(exitCode int) { sess.broadcastExit(exitCode) },
	)

	m.mu.Lock()
	m.sessions[tid] = sess
	m.mu.Unlock()

	return tid, nil
}

func (m *ptyManager) get(tid string) (*ptySession, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[tid]
	return sess, ok
}

func (m *ptyManager) Delete(tid string) {
	m.mu.Lock()
	sess, ok := m.sessions[tid]
	delete(m.sessions, tid)
	m.mu.Unlock()
	if !ok {
		return
	}
	sess.mu.Lock()
	sess.closed = true
	sess.mu.Unlock()
	sess.backend.Close(sess.streamID)
}

// ListByChat returns terminal sessions for a chatID.
func (m *ptyManager) ListByChat(chatID string) []ptySessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []ptySessionInfo
	for _, sess := range m.sessions {
		if sess.chatID == chatID {
			result = append(result, ptySessionInfo{
				Tid:       sess.tid,
				Cwd:       sess.cwd,
				CreatedAt: sess.createdAt,
			})
		}
	}
	return result
}

// CleanupChat destroys all PTY sessions for a chatID.
func (m *ptyManager) CleanupChat(chatID string) {
	m.mu.Lock()
	var toDelete []string
	for tid, sess := range m.sessions {
		if sess.chatID == chatID {
			toDelete = append(toDelete, tid)
		}
	}
	m.mu.Unlock()
	for _, tid := range toDelete {
		m.Delete(tid)
	}
}

func (m *ptyManager) idleReaper() {
	defer m.wg.Done()
	ticker := time.NewTicker(ptyReapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.reapIdle()
		}
	}
}

func (m *ptyManager) reapIdle() {
	m.mu.Lock()
	var toDelete []string
	now := time.Now()
	for tid, sess := range m.sessions {
		sess.mu.Lock()
		empty := len(sess.clients) == 0
		idle := now.Sub(sess.lastSeen) > ptyIdleTimeout
		sess.mu.Unlock()
		if empty && idle {
			toDelete = append(toDelete, tid)
		}
	}
	m.mu.Unlock()
	for _, tid := range toDelete {
		log.WithField("tid", tid).Debug("Reaping idle PTY session")
		m.Delete(tid)
	}
}

// --- ptySession methods ---

func (s *ptySession) addClient(c *ptyWSClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[c] = struct{}{}
	s.lastSeen = time.Now()
}

func (s *ptySession) removeClient(c *ptyWSClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, c)
	s.lastSeen = time.Now()
}

// broadcast sends PTY output to all connected WS clients and appends to history.
func (s *ptySession) broadcast(data []byte) {
	s.mu.Lock()
	s.history = append(s.history, data...)
	if len(s.history) > ptyHistoryCap {
		s.history = s.history[len(s.history)-ptyHistoryCap:]
	}
	clients := make([]*ptyWSClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.lastSeen = time.Now()
	s.mu.Unlock()

	msg, _ := json.Marshal(map[string]any{
		"type": "stdout",
		"data": base64.StdEncoding.EncodeToString(data),
	})
	for _, c := range clients {
		select {
		case c.send <- msg:
		default:
			// client send buffer full, drop this frame
		}
	}
}

func (s *ptySession) broadcastExit(exitCode int) {
	s.mu.Lock()
	clients := make([]*ptyWSClient, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()

	msg, _ := json.Marshal(map[string]any{
		"type": "exit",
		"code": exitCode,
	})
	for _, c := range clients {
		select {
		case c.send <- msg:
		default:
		}
	}
}

// getHistory returns the scrollback buffer for replay on reconnect.
func (s *ptySession) getHistory() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history
}

// --- types ---

type ptySessionInfo struct {
	Tid       string    `json:"tid"`
	Cwd       string    `json:"cwd"`
	CreatedAt time.Time `json:"createdAt"`
}

// generateTID generates a unique terminal ID.
func generateTID() string {
	return fmt.Sprintf("t-%d", time.Now().UnixNano())
}

// ---------------------------------------------------------------------------
// HTTP Handlers
// ---------------------------------------------------------------------------

type terminalCreateReq struct {
	ChatID string `json:"chatID"`
	Cwd    string `json:"cwd"`
}

type terminalCreateResp struct {
	Tid string `json:"tid"`
}

func (wc *WebChannel) handleTerminalCreate(w http.ResponseWriter, r *http.Request) {
	senderID := senderIDFromContext(r.Context())
	if senderID == "" {
		log.Warn("terminal/create: no senderID in context")
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req terminalCreateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErrorResponse(w, http.StatusBadRequest, "invalid request body")
		return
	}

	cwd := req.Cwd
	if cwd == "" {
		// Default to sandbox workspace.
		if sandbox := tools.GetSandbox(); sandbox != nil {
			cwd = sandbox.Workspace(senderID)
		}
	}

	tid, err := wc.ptyMgr.Create(senderID, req.ChatID, cwd, 80, 24)
	if err != nil {
		log.WithError(err).Warn("Failed to create PTY")
		jsonErrorResponse(w, http.StatusInternalServerError, fmt.Sprintf("create terminal: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, terminalCreateResp{Tid: tid})
}

func (wc *WebChannel) handleTerminalDelete(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("tid")
	if tid == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "tid is required")
		return
	}
	wc.ptyMgr.Delete(tid)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type terminalListReq struct {
	ChatID string `json:"chat_id"`
}

type terminalListResp struct {
	Terminals []ptySessionInfo `json:"terminals"`
}

func (wc *WebChannel) handleTerminalList(w http.ResponseWriter, r *http.Request) {
	var req terminalListReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// Allow empty body
		req.ChatID = ""
	}
	terminals := wc.ptyMgr.ListByChat(req.ChatID)
	if terminals == nil {
		terminals = []ptySessionInfo{}
	}
	writeJSON(w, http.StatusOK, terminalListResp{Terminals: terminals})
}

// terminalWSUpgrader reuses the web channel's WS upgrader.
var terminalWSUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // origin check handled by cookie auth
	},
}

func (wc *WebChannel) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	// Cookie auth (same as handleWS for browser clients).
	si := wc.validateSession(r)
	if si == nil {
		log.Warn("terminal/ws: no valid session")
		jsonErrorResponse(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	tid := r.URL.Query().Get("tid")
	if tid == "" {
		jsonErrorResponse(w, http.StatusBadRequest, "tid is required")
		return
	}

	sess, ok := wc.ptyMgr.get(tid)
	if !ok {
		log.Warnf("terminal/ws: tid=%s not found", tid)
		jsonErrorResponse(w, http.StatusNotFound, "terminal not found")
		return
	}

	conn, err := terminalWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		log.WithError(err).Warn("Terminal WS upgrade failed")
		return
	}

	client := &ptyWSClient{
		conn: conn,
		send: make(chan []byte, 64),
	}
	sess.addClient(client)

	// Replay history to the new client.
	if history := sess.getHistory(); len(history) > 0 {
		msg, _ := json.Marshal(map[string]any{
			"type": "stdout",
			"data": base64.StdEncoding.EncodeToString(history),
		})
		select {
		case client.send <- msg:
		default:
		}
	}

	// Read pump: client → backend.
	go func() {
		defer func() {
			conn.Close()
			sess.removeClient(client)
		}()
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var msg map[string]any
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}
			switch msg["type"] {
			case "stdin":
				if data, ok := msg["data"].(string); ok {
					decoded, err := base64.StdEncoding.DecodeString(data)
					if err != nil {
						continue
					}
					sess.backend.WriteStdin(sess.streamID, decoded) //nolint:errcheck
				}
			case "resize":
				cols := uint16(msg["cols"].(float64))
				rows := uint16(msg["rows"].(float64))
				sess.backend.Resize(sess.streamID, cols, rows) //nolint:errcheck
			case "close":
				sess.backend.Close(sess.streamID) //nolint:errcheck
				return
			}
		}
	}()

	// Write pump: send channel → WS.
	go func() {
		defer conn.Close()
		for msg := range client.send {
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()
}
