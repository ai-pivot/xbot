package tools

import (
	"sync/atomic"
	"testing"
	"time"
)

// newPooledTestManager builds a SessionMCPManager and attaches it to the
// pool directly (bypassing the config-file-driven lazy connect — pool
// behavior tests don't need live MCP connections).
func newPooledTestManager(sessionKey, globalCfg, userCfg, workspace, userID string) *SessionMCPManager {
	sm := NewSessionMCPManager(sessionKey, userID, globalCfg, userCfg, workspace, 30*time.Minute)
	sm.mu.Lock()
	sm.entry = globalMCPPool.Acquire(globalCfg, userCfg, workspace, userID)
	sm.mu.Unlock()
	return sm
}

// TestMCPPool_SharedAcrossManagers verifies the core pooling invariant: N
// managers with the same scope 4-tuple share ONE pool entry (one connection
// set). The per-session duplication (N×M child processes) is the bug this
// pool fixes.
func TestMCPPool_SharedAcrossManagers(t *testing.T) {
	pool := &MCPConnectionPool{entries: make(map[string]*mcpPoolEntry), idleTimeout: time.Hour}

	e1 := pool.Acquire("global.json", "user.json", "/ws", "op")
	e2 := pool.Acquire("global.json", "user.json", "/ws", "op")
	if e1 != e2 {
		t.Fatalf("same scope 4-tuple must share one pool entry: %p != %p", e1, e2)
	}

	// Different scope (different user config path) → different entry.
	e3 := pool.Acquire("global.json", "user2.json", "/ws", "op")
	if e3 == e1 {
		t.Fatalf("different scope must map to a different pool entry")
	}

	entries, refs := pool.PoolStats()
	if entries != 2 {
		t.Errorf("pool entries = %d, want 2", entries)
	}
	if refs != 3 {
		t.Errorf("pool refs = %d, want 3 (two acquires on e1/e2, one on e3)", refs)
	}

	pool.Release(e1)
	pool.Release(e2)
	pool.Release(e3)
}

// TestMCPPool_UpdateScopeKeepsOldEntryAlive verifies BUG#3's fix: a scope
// switch detaches from the old entry WITHOUT closing it — its connections
// stay alive for other sharers (and the reaper decides reclamation later).
func TestMCPPool_UpdateScopeKeepsOldEntryAlive(t *testing.T) {
	pool := &MCPConnectionPool{entries: make(map[string]*mcpPoolEntry), idleTimeout: time.Hour}

	old1 := pool.Acquire("g.json", "u1.json", "/ws", "op")
	old2 := pool.Acquire("g.json", "u1.json", "/ws", "op") // second sharer
	if old1 != old2 {
		t.Fatalf("precondition: same scope shares the entry")
	}

	// Manager A switches scope: releases old entry, acquires the new one.
	pool.Release(old1)
	new1 := pool.Acquire("g.json", "u2.json", "/ws", "op")

	if old1.isClosed() {
		t.Fatalf("scope switch must NOT close the old entry (other sharers remain)")
	}
	if new1 == old1 {
		t.Fatalf("new scope must acquire a different entry")
	}

	// Old entry still holds a ref from sharer B — reaper must keep it.
	pool.ReapOnce()
	if old2.isClosed() {
		t.Fatalf("reaper must not close an entry with refCount > 0")
	}

	// Sharer B releases too; after the idle timeout the reaper reclaims it.
	pool.Release(old2)
	pool.Release(new1)
	old2.mu.Lock()
	old2.lastActive = time.Now().Add(-2 * time.Hour) // force idle
	old2.mu.Unlock()
	pool.ReapOnce()
	if !old2.isClosed() {
		t.Fatalf("reaper must close an idle entry with refCount == 0")
	}

	// Reaped entry is gone from the pool: the next acquire creates a fresh one.
	fresh := pool.Acquire("g.json", "u1.json", "/ws", "op")
	if fresh == old2 {
		t.Fatalf("reaped entry must not be reused — a fresh entry must be created")
	}
}

// TestMCPPool_ReapRespectsRefCount verifies the reaper rules: entries with
// active references are never reaped, entries with refCount == 0 are only
// reaped after the idle timeout, and a touched entry survives.
func TestMCPPool_ReapRespectsRefCount(t *testing.T) {
	pool := &MCPConnectionPool{entries: make(map[string]*mcpPoolEntry), idleTimeout: time.Hour}

	// Active reference: survives even when idle-eligible.
	active := pool.Acquire("g.json", "u.json", "/ws", "op")
	active.mu.Lock()
	active.lastActive = time.Now().Add(-2 * time.Hour)
	active.mu.Unlock()
	pool.ReapOnce()
	if active.isClosed() {
		t.Fatalf("entry with refCount > 0 must never be reaped")
	}
	pool.Release(active)

	// No reference + fresh: survives (not idle long enough).
	idle1 := pool.Acquire("g2.json", "u.json", "/ws", "op")
	pool.Release(idle1)
	pool.ReapOnce()
	if idle1.isClosed() {
		t.Fatalf("entry within the idle timeout must survive")
	}

	// No reference + stale: reaped. But touching it resets the clock.
	idle1.mu.Lock()
	idle1.lastActive = time.Now().Add(-90 * time.Minute)
	idle1.mu.Unlock()
	idle1.touch()
	pool.ReapOnce()
	if idle1.isClosed() {
		t.Fatalf("touched entry must survive (idle clock reset)")
	}
	idle1.mu.Lock()
	idle1.lastActive = time.Now().Add(-90 * time.Minute)
	idle1.mu.Unlock()
	pool.ReapOnce()
	if !idle1.isClosed() {
		t.Fatalf("stale entry with refCount == 0 must be reaped")
	}
}

// TestMCPPool_InvalidateRebuildsOnNextAccess verifies BUG#2's fix: after
// Invalidate (config changed), the closed entry is dropped from the pool —
// the next Acquire builds a fresh entry (reconnects with the new config).
func TestMCPPool_InvalidateRebuildsOnNextAccess(t *testing.T) {
	pool := &MCPConnectionPool{entries: make(map[string]*mcpPoolEntry), idleTimeout: time.Hour}

	e1 := pool.Acquire("g.json", "u.json", "/ws", "op")
	pool.Invalidate("g.json", "u.json", "/ws", "op")
	if !e1.isClosed() {
		t.Fatalf("Invalidate must close the entry")
	}

	entries, _ := pool.PoolStats()
	if entries != 0 {
		t.Errorf("invalidated entry must be removed from the pool (entries=%d)", entries)
	}

	// Next acquire rebuilds (fresh entry — reconnect path).
	e2 := pool.Acquire("g.json", "u.json", "/ws", "op")
	if e2 == e1 {
		t.Fatalf("invalidated entry must not be reused — a fresh entry must be created")
	}
	pool.Release(e2)

	// InvalidateAll closes everything.
	e3 := pool.Acquire("g3.json", "u.json", "/ws", "op")
	pool.InvalidateAll()
	if !e2.isClosed() || !e3.isClosed() {
		t.Fatalf("InvalidateAll must close every entry")
	}
}

// TestSessionMCPManager_PoolSharing verifies the SessionMCPManager view:
// two managers with the same scope share the same pool entry, and a scope
// switch (UpdateScope) moves the manager to a different entry without
// closing the shared one.
func TestSessionMCPManager_PoolSharing(t *testing.T) {
	// Reset the global pool for test isolation.
	prevPool := globalMCPPool
	defer func() { globalMCPPool = prevPool }()
	globalMCPPool = &MCPConnectionPool{entries: make(map[string]*mcpPoolEntry), idleTimeout: time.Hour}

	sm1 := newPooledTestManager("web:chat-a", "/g/mcp.json", "/ws/.xbot/users/op/mcp.json", "/ws", "op")
	sm2 := newPooledTestManager("web:chat-b", "/g/mcp.json", "/ws/.xbot/users/op/mcp.json", "/ws", "op")

	sm1.mu.RLock()
	e1 := sm1.entry
	sm1.mu.RUnlock()
	sm2.mu.RLock()
	e2 := sm2.entry
	sm2.mu.RUnlock()

	if e1 == nil || e1 != e2 {
		t.Fatalf("two managers on the same scope must share one pool entry: %p != %p", e1, e2)
	}
	if e1.refCountForTest() != 2 {
		t.Errorf("shared entry refCount = %d, want 2", e1.refCountForTest())
	}

	// Manager 2 switches scope (BUG#3 fix: no disconnect of the shared entry).
	sm2.UpdateScope("op2", "/ws/.xbot/users/op2/mcp.json", "/ws")
	sm2.mu.RLock()
	e2b := sm2.entry
	sm2.mu.RUnlock()
	if e2b == nil || e2b == e1 {
		t.Fatalf("scope switch must move the manager to a different entry")
	}
	if e1.isClosed() {
		t.Fatalf("scope switch must NOT close the entry still used by manager 1")
	}
	if e1.refCountForTest() != 1 {
		t.Errorf("old entry refCount after switch = %d, want 1 (manager 1 still holds it)", e1.refCountForTest())
	}

	// Close manager 1: the shared entry survives with the manager-2 ref only.
	sm1.Close()
	if e1.isClosed() {
		t.Fatalf("Close must only release the ref — the entry has another sharer path via reaper")
	}

	// Manager 2's Invalidate: closes + drops the CURRENT entry; the old
	// (released) entry is untouched by this scope's invalidate.
	sm2.Invalidate()
	if !e2b.isClosed() {
		t.Fatalf("Invalidate must close the current entry")
	}
	// Manager 2 re-acquires a fresh entry on next access.
	_ = sm2.GetCatalog()
	sm2.mu.RLock()
	e2c := sm2.entry
	sm2.mu.RUnlock()
	if e2c == nil || e2c == e2b {
		t.Fatalf("manager must re-acquire a fresh entry after Invalidate")
	}
	sm2.Close()
}

// TestSessionMCPManager_GetSessionToolsSharesPoolEntry verifies the tool
// listing path reads from the shared pool entry (identical tool sets from
// two managers — one connection set).
func TestSessionMCPManager_GetSessionToolsSharesPoolEntry(t *testing.T) {
	prevPool := globalMCPPool
	defer func() { globalMCPPool = prevPool }()
	globalMCPPool = &MCPConnectionPool{entries: make(map[string]*mcpPoolEntry), idleTimeout: time.Hour}

	sm1 := newPooledTestManager("web:chat-a", "/g/mcp.json", "/ws/.xbot/users/op/mcp.json", "/ws", "op")
	sm2 := newPooledTestManager("web:chat-b", "/g/mcp.json", "/ws/.xbot/users/op/mcp.json", "/ws", "op")

	// Seed one connection directly into the shared pool entry (bypasses the
	// config-driven connect — we're testing the sharing, not the connect).
	sm1.mu.RLock()
	entry := sm1.entry
	sm1.mu.RUnlock()
	entry.mu.Lock()
	entry.conns["shared-server"] = &mcpConnection{
		name:         "shared-server",
		session:      nil, // no live session needed for the listing path
		tools:        nil,
		instructions: "test",
	}
	atomic.StoreUint32(&entry.initOnce, 2)
	close(entry.initDone)
	entry.mu.Unlock()

	// Both managers list the same connection.
	c1 := sm1.GetCatalog()
	c2 := sm2.GetCatalog()
	if len(c1) != 1 || len(c2) != 1 || c1[0].Name != "shared-server" || c2[0].Name != "shared-server" {
		t.Fatalf("both managers must list the shared connection: c1=%v c2=%v", c1, c2)
	}

	sm1.Close()
	sm2.Close()
}

// refCountForTest exposes the entry refCount under lock (tests only).
func (e *mcpPoolEntry) refCountForTest() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.refCount
}
