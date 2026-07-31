package cli

import "sync"

// asyncRefreshCache provides a reusable cache-aside + async-refresh pattern.
//
// Read path (sync, fast): Get() returns cached data if loaded; on cache miss,
// loads synchronously from the sync source (e.g. DB read), stores to cache,
// and returns. This is non-blocking for fast sources like local SQLite.
//
// Refresh path (async, slow): the caller runs a slow operation (e.g. /models
// HTTP API) in a goroutine. On completion, the result is applied via Apply()
// on the main thread — the caller is responsible for persisting to DB inside
// the refresh function, then calling Apply() to update the cache. This is the
// "double-write" pattern: the refresh function writes to DB, Apply() writes
// to cache.
//
// Usage:
//
//	cache := newAsyncRefreshCache(func() MyData {
//	    return readFromDB() // fast sync read
//	})
//	// Read: sync, non-blocking
//	data := cache.Get()
//	// Refresh: async (returns tea.Cmd)
//	cmd := func() tea.Msg {
//	    fresh := callSlowAPI()  // writes to DB internally
//	    return refreshedMsg{data: fresh}
//	}
//	// On msg receipt (main thread):
//	cache.Apply(fresh)
type asyncRefreshCache[T any] struct {
	mu       sync.RWMutex
	data     T
	loaded   bool
	loadSync func() T // fast sync source (DB read)
}

// newAsyncRefreshCache creates a cache with the given sync load function.
// The load function must be fast (e.g. local DB read) and non-blocking.
func newAsyncRefreshCache[T any](loadSync func() T) *asyncRefreshCache[T] {
	return &asyncRefreshCache[T]{loadSync: loadSync}
}

// Get returns cached data. On cache miss, loads synchronously from the sync
// source (fast DB read), stores to cache, and returns.
//
// Safe to call from the main thread. The sync source must be fast enough to
// not block the event loop (e.g. local SQLite read ~ms).
func (c *asyncRefreshCache[T]) Get() T {
	c.mu.RLock()
	if c.loaded {
		defer c.mu.RUnlock()
		return c.data
	}
	c.mu.RUnlock()

	// Cache miss: load from sync source.
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded { // double-check after acquiring write lock
		return c.data
	}
	c.data = c.loadSync()
	c.loaded = true
	return c.data
}

// Apply updates the cache with refreshed data. Called from the main thread
// after an async refresh completes. The refresh function is responsible for
// persisting to DB; Apply() only updates the in-memory cache.
func (c *asyncRefreshCache[T]) Apply(data T) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data = data
	c.loaded = true
}

// Invalidate clears the cache. The next Get() will re-read from the sync source.
func (c *asyncRefreshCache[T]) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	c.data = zero
	c.loaded = false
}

// Loaded reports whether the cache has been populated (either by Get() or Apply()).
func (c *asyncRefreshCache[T]) Loaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}
