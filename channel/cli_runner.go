package channel

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"xbot/clipanic"
	"xbot/internal/runnerclient"
	"xbot/llm"
)

// ---------------------------------------------------------------------------
// Runner Status & Stats
// ---------------------------------------------------------------------------

// RunnerStatus Represents runner connection status
type RunnerStatus string

const (
	RunnerDisconnected RunnerStatus = "disconnected"
	RunnerConnecting   RunnerStatus = "connecting"
	RunnerConnected    RunnerStatus = "connected"
)

// RunnerStats Connection statistics
type RunnerStats struct {
	ConnectedAt  time.Time
	RequestCount int64
	LastRequest  time.Time
}

// ---------------------------------------------------------------------------
// Bubble Tea Messages (runner → TUI)
// ---------------------------------------------------------------------------

// runnerStatusMsg Notify TUI of runner connection status change
type runnerStatusMsg struct {
	status RunnerStatus
	err    error
}

// ---------------------------------------------------------------------------
// RunnerBridge — Manage TUI's runner connection lifecycle
// ---------------------------------------------------------------------------

// RunnerBridge Manage TUI's runner connection
type RunnerBridge struct {
	mu        sync.Mutex
	status    RunnerStatus
	serverURL string
	token     string
	workspace string
	stats     RunnerStats

	// Internal state
	handler *runnerclient.Handler
	stopCh  chan struct{}
	doneCh  chan struct{} // Goroutine exit signal

	// Callbacks (notify TUI via tea.Cmd)
	program *tea.Program

	// Log file
	logFile *os.File
	logPath string
}

// NewRunnerBridge Create RunnerBridge
func NewRunnerBridge(program *tea.Program) *RunnerBridge {
	return &RunnerBridge{
		status:  RunnerDisconnected,
		program: program,
	}
}

// Connect Connect to server (async, report result via program.Send)
func (rb *RunnerBridge) Connect(serverURL, token, workspace string, llmClient llm.LLM, models []string, llmProvider string) {
	rb.mu.Lock()
	if rb.status == RunnerConnected || rb.status == RunnerConnecting {
		rb.mu.Unlock()
		return
	}
	rb.status = RunnerConnecting
	rb.serverURL = serverURL
	rb.token = token
	rb.workspace = workspace
	stopCh := make(chan struct{})
	doneCh := make(chan struct{})
	rb.stopCh = stopCh
	rb.doneCh = doneCh
	program := rb.program
	rb.mu.Unlock()

	clipanic.Go("channel.RunnerBridge.Connect", func() {
		defer close(doneCh)

		// 1. Create log file
		cacheDir, _ := os.UserCacheDir()
		logDir := filepath.Join(cacheDir, "xbot", "runner-logs")
		os.MkdirAll(logDir, 0755)
		ts := time.Now().Format("20060102-150405")
		logPath := filepath.Join(logDir, fmt.Sprintf("runner-%s.log", ts))
		logFile, logErr := os.Create(logPath)
		if logErr != nil {
			logFile = nil // fallback: no logging
		}

		// 2. Create log callback
		logf := func(format string, args ...interface{}) {
			if logFile != nil {
				now := time.Now().Format("15:04:05")
				fmt.Fprintf(logFile, "[%s] "+format+"\n", append([]interface{}{now}, args...)...)
			}
		}

		// Save to RunnerBridge
		rb.mu.Lock()
		rb.logFile = logFile
		rb.logPath = logPath
		rb.mu.Unlock()

		// 3. Create NativeExecutor
		executor := runnerclient.NewNativeExecutor(workspace)

		// 4. Create Handler (with log callback)
		handler := runnerclient.NewHandler(executor, runnerclient.WithLogFunc(logf))

		// 5. Set LLM client (if available)
		if llmClient != nil {
			handler.SetLLMClient(llmClient, models, llmProvider)
		}

		// 6. Parse userID
		userID := parseUserID(serverURL)
		if userID == "" {
			program.Send(runnerStatusMsg{
				status: RunnerDisconnected,
				err:    fmt.Errorf("cannot parse userID from server URL"),
			})
			return
		}

		// 7. Ensure ws:// prefix
		wsURL := serverURL
		if !strings.Contains(wsURL, "://") {
			wsURL = "ws://" + wsURL
		}

		// 8. Connect to server (self-report LLM capabilities)
		shell := runnerclient.DetectShell(false, executor)
		var opts runnerclient.ConnectOptions
		opts.LogFunc = logf
		if handler.LLMProvider() != "" {
			opts.LLMProvider = handler.LLMProvider()
			opts.LLMModel = handler.LLMModel()
		}
		conn, err := runnerclient.Connect(wsURL, userID, token, workspace, shell, opts)
		if err != nil {
			program.Send(runnerStatusMsg{
				status: RunnerDisconnected,
				err:    err,
			})
			return
		}

		// 9. Save internal state
		rb.mu.Lock()
		rb.handler = handler
		rb.stats = RunnerStats{
			ConnectedAt: time.Now(),
		}
		rb.status = RunnerConnected
		rb.mu.Unlock()

		// 10. Start WritePump + ReadLoop
		writeCh := make(chan runnerclient.WriteMsg, 64)
		writeDone := make(chan struct{})
		handler.SetWriteChannels(writeCh, writeDone)

		// Start WritePump
		go runnerclient.WritePump(conn, writeCh, stopCh, writeDone, logf)

		// Start ReadLoop (blocks until connection drops)
		runnerclient.ReadLoop(conn, handler, writeCh, writeDone, logf)

		// ReadLoop exit → connection dropped
		rb.mu.Lock()
		rb.status = RunnerDisconnected
		handler.Cleanup()
		rb.handler = nil
		rb.mu.Unlock()

		program.Send(runnerStatusMsg{
			status: RunnerDisconnected,
			err:    nil,
		})
	})
}

// Disconnect Disconnect
func (rb *RunnerBridge) Disconnect() {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.status != RunnerConnected && rb.status != RunnerConnecting {
		return
	}

	rb.status = RunnerDisconnected

	// Close stopCh → WritePump exits → ReadLoop also exits
	select {
	case <-rb.stopCh:
		// already closed
	default:
		close(rb.stopCh)
	}

	// Close log file
	if rb.logFile != nil {
		rb.logFile.Close()
		rb.logFile = nil
	}

	// Clean up handler resources
	if rb.handler != nil {
		rb.handler.Cleanup()
		rb.handler = nil
	}
}

// Status Return current status
func (rb *RunnerBridge) Status() RunnerStatus {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.status
}

// Stats Return statistics
func (rb *RunnerBridge) Stats() RunnerStats {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.stats
}

// ServerURL Return current server URL
func (rb *RunnerBridge) ServerURL() string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.serverURL
}

// Workspace Return current workspace
func (rb *RunnerBridge) Workspace() string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.workspace
}

// LogPath returns the current log file path.
func (rb *RunnerBridge) LogPath() string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.logPath
}

// parseUserID: parse userID from server URL
// e.g.: ws://host:port/ws/abc123 → abc123
func parseUserID(serverURL string) string {
	// Strip protocol prefix
	u := serverURL
	if idx := strings.Index(u, "://"); idx >= 0 {
		u = u[idx+3:]
	}
	// Take last path segment
	if idx := strings.LastIndex(u, "/"); idx >= 0 {
		return u[idx+1:]
	}
	return ""
}
