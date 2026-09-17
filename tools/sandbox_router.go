package tools

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"xbot/config"
	log "xbot/logger"
)

// SessionBindingStore persists session→runner bindings.
//
// Injected by the server layer (adapter over the tenants table) so the tools
// package stays free of a storage dependency. The binding key is the canonical
// session key ("channel:chatID").
type SessionBindingStore interface {
	// SetSessionRunner persists the binding. runnerName == "" unbinds.
	SetSessionRunner(sessionKey, runnerName string) error
	// GetSessionRunner reads the binding ("" when unbound).
	GetSessionRunner(sessionKey string) (string, error)
}

// errSandboxNeedsSession is returned by SandboxRouter's delegating Sandbox
// methods. The router does not guess which session a call belongs to: the
// engine resolves the concrete sandbox once per tool call via
// SandboxForSession, and every tool uses ToolContext.Sandbox.
//
// Returning an explicit error here (instead of falling back to the local host)
// is deliberate: a mis-routed call must be loud, never a silent local execution
// on the wrong machine.
var errSandboxNeedsSession = errors.New(
	"SandboxRouter: cannot route without a session — resolve it first via SandboxForSession(channel:chatID)")

// SandboxRouter routes tool execution to the session's bound runner (remote) or
// to the local host (none).
//
// Single-operator design (v63 multi-user removal + v69 runner collapse): there
// is no per-user dimension. Routing is purely session-scoped:
//
//	session bound to runner R, R online   → RemoteSandbox
//	session bound to runner R, R offline  → OfflineRunnerSandbox (HARD FAIL)
//	session unbound                       → NoneSandbox (local host)
//
// The offline case never falls back to the local host: a user who bound a
// session to machine X must not have work silently executed on the server.
type SandboxRouter struct {
	remote  *RemoteSandbox
	none    *NoneSandbox
	offline *OfflineRunnerSandbox

	// sessionRunners is the write-through cache of the persisted session→runner
	// binding (authority = SessionBindingStore). Shared with RemoteSandbox so
	// the connection lookup sees the same binding.
	sessionRunners sync.Map // "channel:chatID" → runnerName

	bindingStore SessionBindingStore

	// Lazy-init state for the remote sandbox.
	remoteMu      sync.Mutex
	remoteCfg     RemoteSandboxConfig
	remoteSyncCfg RemoteSandboxSyncConfig

	// defaultMode describes the router's capability ("remote" when a remote
	// sandbox is available, otherwise "none"). Per-session routing is driven by
	// the bindings above.
	defaultMode string
}

// NewSandboxRouter creates a router holding the runner (remote) sandbox instance.
// Remote sandbox can be lazy-started later via EnsureRemote().
func NewSandboxRouter(sandboxCfg config.SandboxConfig, workDir string) *SandboxRouter {
	r := &SandboxRouter{
		none:    &NoneSandbox{},
		offline: &OfflineRunnerSandbox{},
	}

	wsPort := sandboxCfg.WSPort
	if wsPort == 0 {
		wsPort = 8080
	}
	xbotDir := workDir + "/.xbot"
	r.remoteCfg = RemoteSandboxConfig{
		Addr:      "0.0.0.0:" + strconv.Itoa(wsPort),
		AuthToken: sandboxCfg.AuthToken,
	}
	r.remoteSyncCfg = RemoteSandboxSyncConfig{
		GlobalSkillDirs: []string{xbotDir + "/skills"},
		AgentsDir:       xbotDir + "/agents",
	}

	if sandboxCfg.RemoteMode != "" || sandboxCfg.Mode == "remote" {
		r.EnsureRemote()
	}

	if r.remote != nil {
		r.defaultMode = "remote"
	} else {
		r.defaultMode = "none"
	}
	log.Infof("SandboxRouter initialized: default=%s, remote=%v", r.defaultMode, r.remote != nil)
	return r
}

// EnsureRemote starts the remote sandbox WebSocket server if not already running.
// Safe to call multiple times. Returns true when the remote sandbox is available.
func (r *SandboxRouter) EnsureRemote() bool {
	if r.remote != nil {
		return true
	}
	r.remoteMu.Lock()
	defer r.remoteMu.Unlock()
	if r.remote != nil {
		return true
	}
	rs, err := NewRemoteSandbox(r.remoteCfg, r.remoteSyncCfg)
	if err != nil {
		log.WithError(err).Error("Failed to start remote sandbox")
		return false
	}
	// Share the session-level bindings so the connection layer resolves the
	// same runner the router does.
	rs.sessionRunners = &r.sessionRunners
	rs.SetSessionBindingStore(r.bindingStore)
	r.remote = rs
	r.defaultMode = "remote"
	log.Info("Remote sandbox started dynamically")
	return true
}

// SetRunnerStore stores the runner registry used for token validation, wiring it
// into the remote sandbox (starting it lazily if needed).
func (r *SandboxRouter) SetRunnerStore(store *RunnerStore) {
	r.remoteMu.Lock()
	r.remoteCfg.RunnerStore = store
	rs := r.remote
	r.remoteMu.Unlock()
	if rs != nil {
		rs.SetRunnerStore(store)
		return
	}
	if r.bindingStore != nil {
		r.EnsureRemote()
	}
}

// SetBindingStore wires the persistence backend for session→runner bindings.
// Must be called before serving traffic (set by the server layer).
func (r *SandboxRouter) SetBindingStore(store SessionBindingStore) {
	r.bindingStore = store
	if r.remote != nil {
		r.remote.SetSessionBindingStore(store)
	}
}

// Name returns the router's capability name ("remote" or "none").
// Per-session resolution is SandboxForSession.
func (r *SandboxRouter) Name() string { return r.defaultMode }

// Remote returns the underlying RemoteSandbox instance (may be nil).
func (r *SandboxRouter) Remote() *RemoteSandbox { return r.remote }

// IsRunnerOnline reports whether the named runner currently holds a connection.
func (r *SandboxRouter) IsRunnerOnline(runnerName string) bool {
	if r.remote == nil {
		return false
	}
	return r.remote.IsRunnerOnline(runnerName)
}

// RunnerVersion returns the version reported by the named runner ("" if unknown).
func (r *SandboxRouter) RunnerVersion(runnerName string) string {
	if r.remote == nil {
		return ""
	}
	return r.remote.RunnerVersion(runnerName)
}

// DisconnectRunner drops the named runner's connection.
func (r *SandboxRouter) DisconnectRunner(runnerName string) bool {
	if r.remote == nil {
		return false
	}
	return r.remote.DisconnectRunner(runnerName)
}

// OnlineRunnerNames lists the currently connected runner names.
func (r *SandboxRouter) OnlineRunnerNames() []string {
	if r.remote == nil {
		return nil
	}
	return r.remote.OnlineRunnerNames()
}

// SetSessionRunner binds a session to a runner (sessionKey "channel:chatID").
// An empty runnerName unbinds the session (back to the local host).
// The binding is persisted immediately; the in-memory map is a write-through
// cache (reads lazily backfill from the store).
func (r *SandboxRouter) SetSessionRunner(sessionKey, runnerName string) error {
	if sessionKey == "" {
		return errors.New("SetSessionRunner: sessionKey is required")
	}
	if r.bindingStore == nil {
		return errors.New("SetSessionRunner: session binding store not configured")
	}
	if err := r.bindingStore.SetSessionRunner(sessionKey, runnerName); err != nil {
		return err
	}
	if runnerName == "" {
		r.sessionRunners.Delete(sessionKey)
	} else {
		r.sessionRunners.Store(sessionKey, runnerName)
	}
	return nil
}

// GetSessionRunner returns the runner bound to a session ("" when unbound).
func (r *SandboxRouter) GetSessionRunner(sessionKey string) string {
	if v, ok := r.sessionRunners.Load(sessionKey); ok {
		if name, _ := v.(string); name != "" {
			return name
		}
	}
	if r.bindingStore == nil || sessionKey == "" {
		return ""
	}
	name, err := r.bindingStore.GetSessionRunner(sessionKey)
	if err != nil || name == "" {
		return ""
	}
	r.sessionRunners.Store(sessionKey, name)
	return name
}

// ForgetSession drops any cached binding for a deleted/rewound session.
func (r *SandboxRouter) ForgetSession(sessionKey string) {
	r.sessionRunners.Delete(sessionKey)
}

// SessionsForRunner lists the session keys currently bound to a runner.
//
// Used to (re)wire per-session side effects when a machine comes online or goes
// away — e.g. injecting/clearing the runner-local ProxyLLM.
func (r *SandboxRouter) SessionsForRunner(runnerName string) []string {
	if runnerName == "" {
		return nil
	}
	var keys []string
	r.sessionRunners.Range(func(k, v any) bool {
		key, ok := k.(string)
		if !ok {
			return true
		}
		if name, _ := v.(string); name == runnerName {
			keys = append(keys, key)
		}
		return true
	})
	sort.Strings(keys)
	return keys
}

// SandboxForSession resolves the sandbox for a session ("channel:chatID").
// Unbound sessions run locally; a bound-but-offline runner is a HARD FAILURE.
func (r *SandboxRouter) SandboxForSession(sessionKey string) Sandbox {
	name := r.GetSessionRunner(sessionKey)
	if name == "" {
		return r.none
	}
	if r.remote != nil && r.remote.IsRunnerOnline(name) {
		return r.remote
	}
	off := *r.offline
	off.runnerName = name
	return &off
}

// Sandbox returns the deployment default (the local host).
//
// There is intentionally no "any connected runner" fallback: with runners now
// session-scoped, guessing a target is exactly the failure mode this design
// removes.
func (r *SandboxRouter) Sandbox() Sandbox { return r.none }

// --- Sandbox interface implementation ---
//
// The router deliberately does not implement the delegating operations: a call
// reaching it carries no session identity, so routing it would require a guess.
// Per-tool-call resolution happens in the engine (agent/engine.go,
// agent/engine_wire.go) which sets ToolContext.Sandbox to the concrete backend.

func (r *SandboxRouter) Exec(context.Context, ExecSpec) (*ExecResult, error) {
	return nil, errSandboxNeedsSession
}

func (r *SandboxRouter) ReadFile(context.Context, string, string) ([]byte, error) {
	return nil, errSandboxNeedsSession
}

func (r *SandboxRouter) WriteFile(context.Context, string, []byte, os.FileMode, string) error {
	return errSandboxNeedsSession
}

func (r *SandboxRouter) Stat(context.Context, string, string) (*SandboxFileInfo, error) {
	return nil, errSandboxNeedsSession
}

func (r *SandboxRouter) ReadDir(context.Context, string, string) ([]DirEntry, error) {
	return nil, errSandboxNeedsSession
}

func (r *SandboxRouter) MkdirAll(context.Context, string, os.FileMode, string) error {
	return errSandboxNeedsSession
}

func (r *SandboxRouter) Remove(context.Context, string, string) error {
	return errSandboxNeedsSession
}

func (r *SandboxRouter) RemoveAll(context.Context, string, string) error {
	return errSandboxNeedsSession
}

func (r *SandboxRouter) DownloadFile(context.Context, string, string, string) error {
	return errSandboxNeedsSession
}

// GetShell returns the shell of the deployment default (local host).
func (r *SandboxRouter) GetShell(sessionKey, workspace string) (string, error) {
	return r.none.GetShell(sessionKey, workspace)
}

// Workspace returns the workspace root of the deployment default (local host).
func (r *SandboxRouter) Workspace(sessionKey string) string {
	return r.none.Workspace(sessionKey)
}

// Close closes all sandbox instances (remote connections).
func (r *SandboxRouter) Close() error {
	if r.remote != nil {
		return r.remote.Close()
	}
	return nil
}

// CloseForUser is retained for interface compatibility; runner connections are
// persistent and are not torn down per session.
func (r *SandboxRouter) CloseForUser(string) error { return nil }

// Ensure SandboxRouter implements SandboxResolver and Sandbox.
var (
	_ SandboxResolver = (*SandboxRouter)(nil)
	_ Sandbox         = (*SandboxRouter)(nil)
)

// SplitSessionKey splits "channel:chatID" on the FIRST colon (chatIDs may
// themselves contain colons, e.g. worktree/agent session ids).
func SplitSessionKey(sessionKey string) (channel, chatID string) {
	idx := strings.IndexByte(sessionKey, ':')
	if idx < 0 {
		return "", sessionKey
	}
	return sessionKey[:idx], sessionKey[idx+1:]
}
