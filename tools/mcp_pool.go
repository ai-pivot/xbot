package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPConnectionPool is the process-level singleton pool of MCP connections.
//
// Motivation (per-session → pooled): every TenantSession used to own a
// SessionMCPManager with its OWN connection set — N sessions × M stdio
// servers = N×M MCP child processes, even though the resolved config is
// identical. The config scope is (global mcp.json + per-sender user config)
// — there is NO per-session config, so per-session connection instances
// were pure waste. Instance scope is now ≤ config scope: the pool keys
// entries by the 4-tuple (globalConfigPath, userConfigPath, workspaceRoot,
// userID), every SessionMCPManager with the same tuple shares one entry
// (one set of connections / child processes).
//
// Lifecycle (fixes the three historical bugs):
//   - BUG#1 (dead inactivity timeout): the old UnloadInactiveServers chain
//     only ran for sessions evicted from the 24h session cache — an ACTIVE
//     session's idle servers were never unloaded. The pool reaper now
//     checks every 30s: entries with refCount==0 and idle > idleTimeout
//     are closed and dropped, independent of any session cache.
//   - BUG#2 (never reconnect after unload): the old UnloadInactiveServers
//     reset `initialized` but left `initOnce=2`, so ensureInitAsync's fast
//     path never re-ran. In the pool a reaped entry is simply deleted —
//     the next Acquire creates a fresh entry and reconnects.
//   - BUG#3 (UpdateScope disconnect): scope changes now just detach from
//     the old entry and attach to the new one — the old entry's
//     connections stay alive for its other sharers (or the reaper).
type MCPConnectionPool struct {
	mu          sync.Mutex
	entries     map[string]*mcpPoolEntry
	idleTimeout time.Duration
}

// mcpPoolEntry is one shared connection set, keyed by the scope 4-tuple.
type mcpPoolEntry struct {
	key string

	// scope (reconnect inputs)
	globalConfigPath string
	userConfigPath   string
	workspaceRoot    string
	userID           string

	mu          sync.Mutex
	conns       map[string]*mcpConnection
	lastActive  time.Time // entry-level idle clock (touched by any sharer)
	refCount    int       // number of attached SessionMCPManagers
	initOnce    uint32    // 0=idle, 1=starting, 2=started (atomic)
	initDone    chan struct{}
	closed      bool                          // Invalidate'd or reaped — managers must re-acquire
	onChangeFns map[*SessionMCPManager]func() // init-complete callbacks per sharer
}

const (
	mcpPoolReapInterval = 30 * time.Second
	mcpPoolDefaultIdle  = 30 * time.Minute
)

var globalMCPPool = &MCPConnectionPool{
	entries:     make(map[string]*mcpPoolEntry),
	idleTimeout: mcpPoolDefaultIdle,
}

// GlobalMCPPool returns the process-level MCP connection pool.
func GlobalMCPPool() *MCPConnectionPool { return globalMCPPool }

// SetPoolIdleTimeout overrides the idle timeout (tests).
func (p *MCPConnectionPool) SetPoolIdleTimeout(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.idleTimeout = d
}

// mcpPoolKey hashes the scope 4-tuple into a pool key.
func mcpPoolKey(globalConfigPath, userConfigPath, workspaceRoot, userID string) string {
	h := fnv.New64a()
	for _, s := range []string{globalConfigPath, userConfigPath, workspaceRoot, userID} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// Acquire returns the pool entry for the scope 4-tuple, creating it if
// necessary, and increments its refCount. The caller MUST Release it when
// done (SessionMCPManager.Close / scope switch).
func (p *MCPConnectionPool) Acquire(globalConfigPath, userConfigPath, workspaceRoot, userID string) *mcpPoolEntry {
	key := mcpPoolKey(globalConfigPath, userConfigPath, workspaceRoot, userID)
	p.mu.Lock()
	entry, ok := p.entries[key]
	if !ok || entry.isClosed() {
		entry = &mcpPoolEntry{
			key:              key,
			globalConfigPath: globalConfigPath,
			userConfigPath:   userConfigPath,
			workspaceRoot:    workspaceRoot,
			userID:           userID,
			conns:            make(map[string]*mcpConnection),
			lastActive:       time.Now(),
			initDone:         make(chan struct{}),
			onChangeFns:      make(map[*SessionMCPManager]func()),
		}
		p.entries[key] = entry
	}
	p.mu.Unlock()

	entry.mu.Lock()
	entry.refCount++
	entry.mu.Unlock()

	p.startReaperIfNeeded()
	return entry
}

// Release decrements the entry's refCount. The entry stays in the pool
// (connections stay alive for other sharers); the reaper closes it once
// refCount==0 AND it has been idle longer than the idle timeout.
func (p *MCPConnectionPool) Release(entry *mcpPoolEntry) {
	if entry == nil {
		return
	}
	entry.mu.Lock()
	if entry.refCount > 0 {
		entry.refCount--
	}
	entry.mu.Unlock()
}

// Invalidate closes the entry for the scope 4-tuple, removes it from the
// pool, and marks it closed so attached managers re-acquire a fresh entry
// on next access. Used when the MCP config files change
// (ManageTools add_mcp/remove_mcp).
func (p *MCPConnectionPool) Invalidate(globalConfigPath, userConfigPath, workspaceRoot, userID string) {
	key := mcpPoolKey(globalConfigPath, userConfigPath, workspaceRoot, userID)
	p.mu.Lock()
	entry, ok := p.entries[key]
	if ok {
		delete(p.entries, key)
	}
	p.mu.Unlock()
	if ok {
		entry.closeLocked("invalidated (config changed)")
	}
}

// InvalidateAll closes every entry in the pool (global config changed —
// e.g. the global mcp.json was edited). Attached managers re-acquire on
// next access.
func (p *MCPConnectionPool) InvalidateAll() {
	p.mu.Lock()
	entries := make([]*mcpPoolEntry, 0, len(p.entries))
	for k, e := range p.entries {
		delete(p.entries, k)
		entries = append(entries, e)
	}
	p.mu.Unlock()
	for _, e := range entries {
		e.closeLocked("invalidated (global config changed)")
	}
}

// PoolStats returns the number of entries and their refCounts (observability).
func (p *MCPConnectionPool) PoolStats() (entries, refs int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries = len(p.entries)
	for _, e := range p.entries {
		e.mu.Lock()
		refs += e.refCount
		e.mu.Unlock()
	}
	return entries, refs
}

// --- reaper ---

var poolReaperOnce sync.Once

// startReaperIfNeeded launches the pool reaper exactly once per process.
func (p *MCPConnectionPool) startReaperIfNeeded() {
	poolReaperOnce.Do(func() {
		go p.reapLoop()
	})
}

// reapLoop periodically closes entries with refCount==0 that have been idle
// longer than the idle timeout. This is the fix for the dead-parameter bug:
// idle connections are reclaimed independent of any session cache eviction
// (the old chain only ran for sessions evicted from the 24h cache).
func (p *MCPConnectionPool) reapLoop() {
	ticker := time.NewTicker(mcpPoolReapInterval)
	defer ticker.Stop()
	for range ticker.C {
		p.ReapOnce()
	}
}

// ReapOnce performs a single reaping pass (also used by tests).
func (p *MCPConnectionPool) ReapOnce() {
	now := time.Now()
	p.mu.Lock()
	idleTimeout := p.idleTimeout
	type victim struct {
		entry *mcpPoolEntry
		key   string
	}
	var victims []victim
	for key, entry := range p.entries {
		entry.mu.Lock()
		rc, last := entry.refCount, entry.lastActive
		entry.mu.Unlock()
		if rc == 0 && now.Sub(last) > idleTimeout {
			victims = append(victims, victim{entry: entry, key: key})
		}
	}
	for _, v := range victims {
		delete(p.entries, v.key)
	}
	p.mu.Unlock()
	for _, v := range victims {
		v.entry.closeLocked("reaped (idle)")
	}
}

// --- pool entry ---

// isClosed reports whether the entry was invalidated or reaped (atomic read
// under entry.mu).
func (e *mcpPoolEntry) isClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// closeLocked closes all connections in the entry and marks it closed.
// The entry must already be removed from the pool map by the caller.
func (e *mcpPoolEntry) closeLocked(reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	e.closed = true
	for name, conn := range e.conns {
		if conn != nil && conn.session != nil {
			if err := conn.session.Close(); err != nil && !IsProcessExitError(err) {
				log.WithError(err).WithFields(log.Fields{
					"server": name,
					"reason": reason,
				}).Debug("Error closing pooled MCP session")
			}
		}
	}
	e.conns = make(map[string]*mcpConnection)
	// Wake any InitWait waiters.
	select {
	case <-e.initDone:
	default:
		close(e.initDone)
	}
	log.WithFields(log.Fields{
		"pool_key": e.key,
		"reason":   reason,
		"refs":     e.refCount,
	}).Info("MCP pool entry closed")
}

// touch marks the entry active (any sharer's tool call / catalog read).
func (e *mcpPoolEntry) touch() {
	e.mu.Lock()
	e.lastActive = time.Now()
	e.mu.Unlock()
}

// ensureInitAsync starts the entry's background initialization exactly once
// (idempotent via initOnce CAS). On errNotInitialized it resets to idle so
// the next access retries (config may be created later). onComplete (may be
// nil) fires after a successful initialization.
func (e *mcpPoolEntry) ensureInitAsync(onComplete func()) {
	if atomic.LoadUint32(&e.initOnce) != 0 {
		// Already started (or starting): run the completion callback after
		// initDone closes (a no-op if it already closed).
		if onComplete != nil {
			go func() {
				<-e.initDone
				onComplete()
			}()
		}
		return
	}
	if !atomic.CompareAndSwapUint32(&e.initOnce, 0, 1) {
		return
	}
	go func() {
		if err := e.loadAndConnect(context.Background()); err != nil {
			if err != errNotInitialized {
				log.WithError(err).WithField("pool_key", e.key).Warn("Failed to load MCP servers for pool entry")
			}
			// Reset to idle so the next access retries (config may be
			// created later — e.g. ManageTools writes it after this point).
			atomic.StoreUint32(&e.initOnce, 0)
			return
		}
		atomic.StoreUint32(&e.initOnce, 2)
		select {
		case <-e.initDone:
		default:
			close(e.initDone)
		}
		e.fireOnChange()
		if onComplete != nil {
			onComplete()
		}
	}()
}

// InitWait blocks until the entry's initialization completed at least once
// (or the entry was closed).
func (e *mcpPoolEntry) InitWait() {
	<-e.initDone
}

// loadAndConnect loads the merged config (global + user) and connects every
// enabled server that is not already connected. Moved from the per-session
// SessionMCPManager.loadAndConnect — identical semantics, pool scope.
func (e *mcpPoolEntry) loadAndConnect(ctx context.Context) error {
	config, err := loadMCPConfigFromPaths(e.globalConfigPath, e.userConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			return errNotInitialized
		}
		return fmt.Errorf("load mcp config: %w", err)
	}

	for name, serverCfg := range config.MCPServers {
		if serverCfg.Enabled != nil && !*serverCfg.Enabled {
			continue
		}
		e.mu.Lock()
		_, connected := e.conns[name]
		e.mu.Unlock()
		if connected {
			continue
		}
		if err := e.connectServer(ctx, name, serverCfg); err != nil {
			log.WithError(err).WithFields(log.Fields{
				"pool_key": e.key,
				"server":   name,
			}).Warn("MCP server connection failed")
		}
	}
	return nil
}

// connectServer connects a single MCP server into this entry.
func (e *mcpPoolEntry) connectServer(ctx context.Context, name string, cfg MCPServerConfig) error {
	e.mu.Lock()
	if len(e.conns) >= maxMCPConnections {
		e.mu.Unlock()
		return fmt.Errorf("MCP connection limit reached (%d), cannot connect server %q", maxMCPConnections, name)
	}
	e.mu.Unlock()

	var (
		session *mcp.ClientSession
		err     error
	)
	if cfg.URL != "" {
		session, err = ConnectHTTPServer(ctx, cfg)
	} else if cfg.Command != "" {
		configPath := e.globalConfigPath
		if configPath == "" {
			configPath = e.userConfigPath
		}
		session, err = ConnectStdioServer(ctx, cfg, configPath, e.workspaceRoot, e.userID, name)
	} else {
		return fmt.Errorf("mcp server config must have either 'url' or 'command'")
	}
	if err != nil {
		return err
	}

	initResult, err := InitializeMCPClient(ctx, session)
	if err != nil {
		_ = session.Close()
		return err
	}
	instructions := initResult.Instructions
	if instructions == "" {
		instructions = cfg.Instructions
	}
	conn := &mcpConnection{
		name:         name,
		session:      session,
		tools:        initResult.Tools,
		instructions: instructions,
	}
	e.mu.Lock()
	e.conns[name] = conn
	e.lastActive = time.Now()
	toolCount := len(conn.tools)
	e.mu.Unlock()

	log.WithFields(log.Fields{
		"pool_key": e.key,
		"server":   name,
		"tools":    toolCount,
	}).Infof("MCP server connected in pool (%d tools)", toolCount)
	return nil
}

// Snapshot returns a copy of the entry's connections for read-side
// iteration. Callers must not mutate the returned map (values are shared).
func (e *mcpPoolEntry) Snapshot() map[string]*mcpConnection {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]*mcpConnection, len(e.conns))
	for k, v := range e.conns {
		out[k] = v
	}
	return out
}

// addOnChange registers a per-manager init-complete callback.
func (e *mcpPoolEntry) addOnChange(mgr *SessionMCPManager, fn func()) {
	if fn == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if atomic.LoadUint32(&e.initOnce) == 2 {
		go fn()
		return
	}
	e.onChangeFns[mgr] = fn
}

// removeOnChange drops a manager's callback (scope switch / close).
func (e *mcpPoolEntry) removeOnChange(mgr *SessionMCPManager) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.onChangeFns, mgr)
}

// fireOnChange invokes all registered init-complete callbacks.
func (e *mcpPoolEntry) fireOnChange() {
	e.mu.Lock()
	fns := make([]func(), 0, len(e.onChangeFns))
	for _, fn := range e.onChangeFns {
		fns = append(fns, fn)
	}
	e.mu.Unlock()
	for _, fn := range fns {
		if fn != nil {
			go fn()
		}
	}
}

// loadMCPConfigFromPaths merges the global + user MCP config files (extracted
// from the old per-session SessionMCPManager.loadConfig — pool scope).
func loadMCPConfigFromPaths(globalConfigPath, userConfigPath string) (*MCPConfig, error) {
	merged := &MCPConfig{MCPServers: map[string]MCPServerConfig{}}

	if globalConfigPath != "" {
		if data, err := os.ReadFile(globalConfigPath); err == nil {
			var cfg MCPConfig
			if err := json.Unmarshal(data, &cfg); err != nil {
				log.Errorf("Failed to parse global MCP configuration JSON: path=%s, error=%v", globalConfigPath, err)
			} else {
				for name, server := range cfg.MCPServers {
					merged.MCPServers[name] = server
				}
			}
		} else if !os.IsNotExist(err) {
			log.WithError(err).WithField("path", globalConfigPath).Warn("Failed to read global MCP config")
		}
	}

	if userConfigPath == "" {
		if len(merged.MCPServers) == 0 {
			return nil, os.ErrNotExist
		}
		return merged, nil
	}

	data, err := os.ReadFile(userConfigPath)
	if err != nil {
		if os.IsNotExist(err) {
			if len(merged.MCPServers) == 0 {
				return nil, err
			}
			return merged, nil
		}
		return nil, err
	}

	var userConfig MCPConfig
	if err := json.Unmarshal(data, &userConfig); err != nil {
		return nil, fmt.Errorf("parse mcp.json: %w", err)
	}
	for name, server := range userConfig.MCPServers {
		merged.MCPServers[name] = server
	}
	return merged, nil
}
