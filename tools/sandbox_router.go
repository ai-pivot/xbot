package tools

import (
	"context"
	"os"
	"strconv"
	"strings"
	"sync"

	"xbot/config"
	log "xbot/logger"
)

// BuiltinDockerRunnerName is the special name for the built-in docker sandbox.
// Used in user_settings.active_runner to indicate "use server-side docker sandbox".
const BuiltinDockerRunnerName = "__docker__"

// SandboxRouter implements Sandbox interface and routes per-user to either
// DockerSandbox, RemoteSandbox, or NoneSandbox based on user state.
//
// Routing rules (per-user, determined by user_settings.active_runner):
//   - active_runner == BuiltinDockerRunnerName → docker (if enabled)
//   - active_runner == specific remote name → remote (if connected)
//   - Fallback: remote if connected, then docker, then none
type SandboxRouter struct {
	docker     *DockerSandbox
	remote     *RemoteSandbox
	none       *NoneSandbox
	denied     *DeniedSandbox
	tokenStore *RunnerTokenStore

	// IsAdminFn, when non-nil, is called to check if a user has admin privileges.
	// Admin users bypass the web-user DeniedSandbox restriction in SandboxForUser.
	// Set by serverapp after construction (server has admin identity info).
	IsAdminFn func(userID string) bool

	// sessionRunners holds session-level runner bindings (sessionKey → runnerName).
	// This is session-scoped: switching runner in one session doesn't affect others.
	// Shared with RemoteSandbox so getRunner can read the same binding.
	sessionRunners sync.Map // "channel:chatID" → runnerName

	// Lazy-init state for remote sandbox
	remoteMu      sync.Mutex
	remoteCfg     RemoteSandboxConfig // stored for EnsureRemote lazy init
	remoteSyncCfg RemoteSandboxSyncConfig

	// defaultMode is used when SandboxForUser can't determine per-user routing.
	// "docker" if docker is enabled, "remote" if remote is enabled, "none" otherwise.
	defaultMode string
}

// SetIsAdminFn sets the admin-check callback used by SandboxForUser to bypass
// the web-user DeniedSandbox restriction for admin users.
func (r *SandboxRouter) SetIsAdminFn(fn func(userID string) bool) {
	r.IsAdminFn = fn
}

// NewSandboxRouter creates a router that holds both docker and remote sandbox instances.
// Either (or both) may be nil — the router falls back gracefully.
// Remote sandbox can be lazy-started later via EnsureRemote() if not configured at construction time.
func NewSandboxRouter(sandboxCfg config.SandboxConfig, workDir string) *SandboxRouter {
	r := &SandboxRouter{
		none:   &NoneSandbox{},
		denied: &DeniedSandbox{},
	}

	// Initialize docker sandbox if configured
	// Docker is enabled when Mode=="docker", or when RemoteMode is set and Mode is not "none".
	// Mode=="none" + RemoteMode=="remote" means remote-only, no docker.
	if sandboxCfg.Mode == "docker" || (sandboxCfg.RemoteMode != "" && sandboxCfg.Mode != "none") {
		cleanupStaleTmpFiles()
		pruneDockerResources()
		r.docker = NewDockerSandbox(sandboxCfg, workDir)
	}

	// Store config for lazy remote sandbox startup.
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

	// Initialize remote sandbox if configured
	if sandboxCfg.RemoteMode != "" || sandboxCfg.Mode == "remote" {
		r.EnsureRemote()
	}

	// Determine default mode for Name() and fallback routing
	switch {
	case r.remote != nil:
		r.defaultMode = "remote"
	case r.docker != nil:
		r.defaultMode = "docker"
	default:
		r.defaultMode = "none"
	}

	log.Infof("SandboxRouter initialized: default=%s, docker=%v, remote=%v",
		r.defaultMode, r.docker != nil, r.remote != nil)

	return r
}

// EnsureRemote starts the remote sandbox WebSocket server if not already running.
// Safe to call multiple times — subsequent calls are no-ops.
// Returns true if the remote sandbox is available (started now or already running).
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
		log.WithError(err).Error("Failed to start remote sandbox, falling back")
		return false
	}
	// Transfer token store if already set
	if r.tokenStore != nil {
		rs.SetTokenStore(r.tokenStore)
	}
	// Share session-level runner bindings
	rs.sessionRunners = &r.sessionRunners
	r.remote = rs
	if r.defaultMode == "none" {
		r.defaultMode = "remote"
	}
	log.Info("Remote sandbox started dynamically")
	return true
}

// Name returns the default sandbox mode name.
// For per-user resolution, use SandboxForUser(userID).Name().
func (r *SandboxRouter) Name() string {
	return r.defaultMode
}

// HasDocker reports whether the built-in docker sandbox is available.
func (r *SandboxRouter) HasDocker() bool {
	return r.docker != nil
}

// DockerImage returns the configured docker image name (e.g. "ubuntu:22.04").
func (r *SandboxRouter) DockerImage() string {
	if r.docker == nil {
		return ""
	}
	return r.docker.Image()
}

// IsRunnerOnline reports whether a specific named runner is connected for the user.
func (r *SandboxRouter) IsRunnerOnline(userID, runnerName string) bool {
	if r.remote == nil {
		return false
	}
	return r.remote.IsRunnerOnline(userID, runnerName)
}

// Remote returns the underlying RemoteSandbox instance (may be nil).
func (r *SandboxRouter) Remote() *RemoteSandbox {
	return r.remote
}

// DisconnectRunner disconnects a runner by name for the given user.
func (r *SandboxRouter) DisconnectRunner(userID, runnerName string) bool {
	if r.remote == nil {
		return false
	}
	return r.remote.DisconnectRunner(userID, runnerName)
}

// SetTokenStore stores the runner token store for reading user active_runner preferences.
func (r *SandboxRouter) SetTokenStore(store *RunnerTokenStore) {
	r.tokenStore = store
}

// SetSessionRunner binds a session to a specific runner (session-level, not user-level).
// This is called by config tool's runner switch action.
func (r *SandboxRouter) SetSessionRunner(sessionKey, runnerName string) {
	r.sessionRunners.Store(sessionKey, runnerName)
}

// GetSessionRunner returns the session-level runner binding, or "" if not set.
func (r *SandboxRouter) GetSessionRunner(sessionKey string) string {
	if v, ok := r.sessionRunners.Load(sessionKey); ok {
		return v.(string)
	}
	return ""
}

// SandboxForSession resolves the sandbox for a session, checking session-level binding first.
// sessionKey format: "channel:chatID".
func (r *SandboxRouter) SandboxForSession(sessionKey, userID string) Sandbox {
	// 1. Check session-level runner binding (highest priority)
	if runnerName := r.GetSessionRunner(sessionKey); runnerName != "" {
		if runnerName == BuiltinDockerRunnerName {
			if r.docker != nil {
				return r.docker
			}
			return r.none
		}
		// Specific remote runner
		if r.remote != nil && r.remote.IsRunnerOnline(userID, runnerName) {
			return r.remote
		}
		// Session wants a specific runner but it's offline → local
		return r.none
	}

	// 2. Fall back to user-level routing
	return r.SandboxForUser(userID)
}

// SandboxForUser returns the user-specific sandbox instance.
// This is the key method for per-user routing — buildToolContext uses it
// to inject the correct sandbox into ToolContext.Sandbox.
//
// Routing priority:
//  1. active_runner == BuiltinDockerRunnerName → docker (if enabled)
//  2. active_runner == specific remote name → remote (only if that runner is online)
//  3. active_runner set but not online → none (local execution, don't silently fallback to wrong runner)
//  4. No active_runner → any connected remote (if any), then docker, then none
func (r *SandboxRouter) SandboxForUser(userID string) Sandbox {
	// 1. Check explicit active_runner preference
	if userID != "" && r.tokenStore != nil {
		if activeName, err := r.tokenStore.GetActiveRunner(userID); err == nil && activeName != "" {
			// Built-in docker
			if activeName == BuiltinDockerRunnerName {
				if r.docker != nil {
					return r.docker
				}
			}
			// Specific remote runner: only route if THAT runner is online
			if r.remote != nil {
				if r.remote.IsRunnerOnline(userID, activeName) {
					return r.remote
				}
				// active_runner set to a specific remote name but it's NOT connected
				// → fall through to local (don't silently route to a different runner)
			}
			return r.none
		}
	}

	// 2. No active_runner set → use any connected remote runner
	if userID != "" && r.remote != nil {
		if r.remote.HasUser(userID) {
			return r.remote
		}
	}

	// 3. Pure web user without remote runner — denied by default
	//    unless the user is admin or the server has no sandbox at all.
	if strings.HasPrefix(userID, "web-") {
		// Admin users bypass the web-user restriction — they own the server
		// and should have the same access as CLI users.
		if r.IsAdminFn != nil && r.IsAdminFn(userID) {
			// Fall through to docker/none below — admin gets host access.
		} else {
			webServerRunner := false
			if v := os.Getenv("WEB_USER_SERVER_RUNNER"); v != "" {
				if b, err := strconv.ParseBool(v); err == nil {
					webServerRunner = b
				}
			}
			if !webServerRunner {
				// When no sandbox is configured (no docker, no remote), the server
				// is running in local mode — CLI users get NoneSandbox. Denying web
				// users here would be inconsistent: the server explicitly chose to
				// run without isolation, so all users get host access.
				// DeniedSandbox only applies when there IS a sandbox but the user
				// doesn't have access (docker or remote configured but not for them).
				if r.docker == nil && r.remote == nil {
					return r.none
				}
				return r.denied
			}
			// Explicitly enabled: allow fallback to server sandbox (docker)
		}
	}

	if r.docker != nil {
		return r.docker
	}
	return r.none
}

// Ensure SandboxRouter implements SandboxResolver
var _ SandboxResolver = (*SandboxRouter)(nil)

// --- Sandbox interface delegation ---

func (r *SandboxRouter) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	return r.resolve(spec.UserID).Exec(ctx, spec)
}

func (r *SandboxRouter) ReadFile(ctx context.Context, path string, userID string) ([]byte, error) {
	return r.resolve(userID).ReadFile(ctx, path, userID)
}

func (r *SandboxRouter) WriteFile(ctx context.Context, path string, data []byte, perm os.FileMode, userID string) error {
	return r.resolve(userID).WriteFile(ctx, path, data, perm, userID)
}

func (r *SandboxRouter) Stat(ctx context.Context, path string, userID string) (*SandboxFileInfo, error) {
	return r.resolve(userID).Stat(ctx, path, userID)
}

func (r *SandboxRouter) ReadDir(ctx context.Context, path string, userID string) ([]DirEntry, error) {
	return r.resolve(userID).ReadDir(ctx, path, userID)
}

func (r *SandboxRouter) MkdirAll(ctx context.Context, path string, perm os.FileMode, userID string) error {
	return r.resolve(userID).MkdirAll(ctx, path, perm, userID)
}

func (r *SandboxRouter) Remove(ctx context.Context, path string, userID string) error {
	return r.resolve(userID).Remove(ctx, path, userID)
}

func (r *SandboxRouter) RemoveAll(ctx context.Context, path string, userID string) error {
	return r.resolve(userID).RemoveAll(ctx, path, userID)
}

func (r *SandboxRouter) DownloadFile(ctx context.Context, url, outputPath, userID string) error {
	return r.resolve(userID).DownloadFile(ctx, url, outputPath, userID)
}

func (r *SandboxRouter) GetShell(userID string, workspace string) (string, error) {
	return r.resolve(userID).GetShell(userID, workspace)
}

func (r *SandboxRouter) Workspace(userID string) string {
	return r.resolve(userID).Workspace(userID)
}

// Close closes all sandbox instances (docker containers, remote connections).
func (r *SandboxRouter) Close() error {
	var errs []error
	if r.docker != nil {
		if err := r.docker.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.remote != nil {
		if err := r.remote.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0] // Return first error
	}
	return nil
}

// CloseForUser closes sandbox resources for a specific user across all backends.
// Remote sandbox connections are not closed — runners should be persistent.
func (r *SandboxRouter) CloseForUser(userID string) error {
	if r.docker != nil {
		return r.docker.CloseForUser(userID)
	}
	return nil
}

// IsExporting checks if docker sandbox is exporting for this user.
func (r *SandboxRouter) IsExporting(userID string) bool {
	if r.docker != nil {
		return r.docker.IsExporting(userID)
	}
	return false
}

// ExportAndImport triggers export+import on the docker sandbox.
func (r *SandboxRouter) ExportAndImport(userID string) error {
	if r.docker != nil {
		return r.docker.ExportAndImport(userID)
	}
	return nil
}

// resolve returns the per-user sandbox instance (delegates to SandboxForUser).
func (r *SandboxRouter) resolve(userID string) Sandbox {
	return r.SandboxForUser(userID)
}
