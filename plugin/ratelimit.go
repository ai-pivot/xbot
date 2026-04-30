package plugin

import (
	"sync"
	"time"
)

// RateLimit defines the maximum number of calls within a sliding time window.
type RateLimit struct {
	MaxCalls int
	Window   time.Duration
}

// PluginRateLimiter enforces per-plugin rate limits using a sliding window counter.
type PluginRateLimiter struct {
	mu      sync.Mutex
	limits  map[string]RateLimit
	windows map[string][]time.Time
}

// NewPluginRateLimiter creates a rate limiter with the given per-plugin limits.
// Plugins without configured limits are unlimited.
func NewPluginRateLimiter(config map[string]RateLimit) *PluginRateLimiter {
	rl := &PluginRateLimiter{
		limits:  make(map[string]RateLimit),
		windows: make(map[string][]time.Time),
	}
	for id, limit := range config {
		rl.limits[id] = limit
	}
	return rl
}

// Allow checks whether a call from pluginID is permitted.
// If allowed, it records the timestamp and returns true.
// Plugins without configured limits are unlimited.
func (rl *PluginRateLimiter) Allow(pluginID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limit, ok := rl.limits[pluginID]
	if !ok {
		return true // no limit configured
	}

	now := time.Now()
	windowStart := now.Add(-limit.Window)

	// Filter out expired timestamps
	timestamps := rl.windows[pluginID]
	valid := make([]time.Time, 0, len(timestamps))
	for _, ts := range timestamps {
		if ts.After(windowStart) {
			valid = append(valid, ts)
		}
	}

	if len(valid) >= limit.MaxCalls {
		rl.windows[pluginID] = valid
		return false
	}

	rl.windows[pluginID] = append(valid, now)
	return true
}

// Remaining returns remaining calls in current window.
// Returns -1 if no limit configured.
func (rl *PluginRateLimiter) Remaining(pluginID string) int {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limit, ok := rl.limits[pluginID]
	if !ok {
		return -1
	}

	now := time.Now()
	windowStart := now.Add(-limit.Window)

	timestamps := rl.windows[pluginID]
	count := 0
	for _, ts := range timestamps {
		if ts.After(windowStart) {
			count++
		}
	}
	remaining := limit.MaxCalls - count
	if remaining < 0 {
		remaining = 0
	}
	return remaining
}

// Reset clears all recorded timestamps for pluginID.
func (rl *PluginRateLimiter) Reset(pluginID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.windows, pluginID)
}

// SetRateLimit dynamically sets the rate limit for a plugin.
func (rl *PluginRateLimiter) SetRateLimit(pluginID string, limit RateLimit) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limits[pluginID] = limit
}

// ---- Quota System ----

// PluginQuota defines daily resource limits for a plugin.
type PluginQuota struct {
	MaxToolCallsPerDay int64
	MaxStorageMB       int64
}

// quotaUsage tracks per-plugin daily consumption.
type quotaUsage struct {
	toolCalls int64
	lastReset time.Time
}

// PluginQuotaManager enforces daily quotas for plugins.
type PluginQuotaManager struct {
	mu       sync.Mutex
	quotas   map[string]PluginQuota
	usage    map[string]*quotaUsage
	storages map[string]StorageAccessor
}

// NewPluginQuotaManager creates a quota manager with the given per-plugin daily quotas.
func NewPluginQuotaManager(quotas map[string]PluginQuota) *PluginQuotaManager {
	qm := &PluginQuotaManager{
		quotas:   make(map[string]PluginQuota),
		usage:    make(map[string]*quotaUsage),
		storages: make(map[string]StorageAccessor),
	}
	for id, q := range quotas {
		qm.quotas[id] = q
	}
	return qm
}

// SetQuota sets or updates the quota for a plugin.
func (qm *PluginQuotaManager) SetQuota(pluginID string, quota PluginQuota) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.quotas[pluginID] = quota
}

// SetStorage binds a storage accessor for storage size checking.
func (qm *PluginQuotaManager) SetStorage(pluginID string, storage StorageAccessor) {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	qm.storages[pluginID] = storage
}

// checkAndResetDaily performs lazy daily reset before counting.
func (qm *PluginQuotaManager) checkAndResetDaily(pluginID string) {
	today := time.Now().UTC().Truncate(24 * time.Hour)
	u, ok := qm.usage[pluginID]
	if !ok {
		qm.usage[pluginID] = &quotaUsage{lastReset: today}
		return
	}
	if u.lastReset.Before(today) {
		u.toolCalls = 0
		u.lastReset = today
	}
}

// CheckToolCall checks if the plugin can make another tool call today.
// If allowed, increments the counter. Returns (allowed, remaining).
func (qm *PluginQuotaManager) CheckToolCall(pluginID string) (bool, int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	quota, ok := qm.quotas[pluginID]
	if !ok || quota.MaxToolCallsPerDay == 0 {
		return true, -1 // no quota configured
	}

	qm.checkAndResetDaily(pluginID)
	u := qm.usage[pluginID]

	if u.toolCalls >= quota.MaxToolCallsPerDay {
		return false, 0
	}

	u.toolCalls++
	remaining := quota.MaxToolCallsPerDay - u.toolCalls
	if remaining < 0 {
		remaining = 0
	}
	return true, remaining
}

// CheckStorage checks if the plugin's storage usage is within quota.
// Returns (ok, usedBytes).
func (qm *PluginQuotaManager) CheckStorage(pluginID string) (bool, int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	quota, ok := qm.quotas[pluginID]
	if !ok {
		return true, -1
	}

	storage, ok := qm.storages[pluginID]
	if !ok || storage == nil {
		return true, 0
	}

	var totalBytes int64
	keys := storage.Keys()
	for _, k := range keys {
		if v, found := storage.Get(k); found {
			totalBytes += int64(len(v))
		}
	}

	maxBytes := quota.MaxStorageMB * 1024 * 1024
	return totalBytes <= maxBytes, totalBytes
}

// GetQuotaUsage returns current usage for a plugin.
func (qm *PluginQuotaManager) GetQuotaUsage(pluginID string) (toolCalls int64, storageBytes int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	qm.checkAndResetDaily(pluginID)
	u, ok := qm.usage[pluginID]
	if ok {
		toolCalls = u.toolCalls
	}

	storage, ok := qm.storages[pluginID]
	if ok && storage != nil {
		keys := storage.Keys()
		for _, k := range keys {
			if v, found := storage.Get(k); found {
				storageBytes += int64(len(v))
			}
		}
	}
	return
}

// ResetDaily resets all tool call counters (exposed for testing).
func (qm *PluginQuotaManager) ResetDaily() {
	qm.mu.Lock()
	defer qm.mu.Unlock()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	for _, u := range qm.usage {
		u.toolCalls = 0
		u.lastReset = today
	}
}
