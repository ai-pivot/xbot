package plugin

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// PluginRateLimiter tests
// ---------------------------------------------------------------------------

func TestRateLimiter_AllowWithinLimit(t *testing.T) {
	rl := NewPluginRateLimiter(map[string]RateLimit{
		"p1": {MaxCalls: 3, Window: time.Minute},
	})

	for i := 0; i < 3; i++ {
		if !rl.Allow("p1") {
			t.Fatalf("call %d should be allowed", i+1)
		}
	}
	if rl.Allow("p1") {
		t.Fatal("4th call should be denied")
	}
}

func TestRateLimiter_UnconfiguredPluginUnlimited(t *testing.T) {
	rl := NewPluginRateLimiter(map[string]RateLimit{
		"p1": {MaxCalls: 1, Window: time.Minute},
	})

	for i := 0; i < 100; i++ {
		if !rl.Allow("unconfigured") {
			t.Fatalf("unconfigured plugin call %d should always be allowed", i+1)
		}
	}
}

func TestRateLimiter_SlidingWindowExpiry(t *testing.T) {
	rl := NewPluginRateLimiter(map[string]RateLimit{
		"p1": {MaxCalls: 2, Window: 100 * time.Millisecond},
	})

	// Fill the window
	if !rl.Allow("p1") {
		t.Fatal("first call should be allowed")
	}
	if !rl.Allow("p1") {
		t.Fatal("second call should be allowed")
	}
	if rl.Allow("p1") {
		t.Fatal("third call should be denied")
	}

	// Wait for window to expire
	time.Sleep(150 * time.Millisecond)

	if !rl.Allow("p1") {
		t.Fatal("call after window expiry should be allowed")
	}
}

func TestRateLimiter_Remaining(t *testing.T) {
	rl := NewPluginRateLimiter(map[string]RateLimit{
		"p1": {MaxCalls: 5, Window: time.Minute},
	})

	if r := rl.Remaining("p1"); r != 5 {
		t.Fatalf("initial remaining should be 5, got %d", r)
	}

	rl.Allow("p1")
	if r := rl.Remaining("p1"); r != 4 {
		t.Fatalf("after 1 call remaining should be 4, got %d", r)
	}

	// Unconfigured plugin
	if r := rl.Remaining("unknown"); r != -1 {
		t.Fatalf("unconfigured plugin remaining should be -1, got %d", r)
	}
}

func TestRateLimiter_Reset(t *testing.T) {
	rl := NewPluginRateLimiter(map[string]RateLimit{
		"p1": {MaxCalls: 1, Window: time.Minute},
	})

	rl.Allow("p1")
	if rl.Allow("p1") {
		t.Fatal("second call should be denied")
	}

	rl.Reset("p1")
	if !rl.Allow("p1") {
		t.Fatal("after reset, call should be allowed")
	}
}

// ---------------------------------------------------------------------------
// PluginQuotaManager tests
// ---------------------------------------------------------------------------

func TestQuotaManager_ToolCallQuota(t *testing.T) {
	qm := NewPluginQuotaManager(map[string]PluginQuota{
		"p1": {MaxToolCallsPerDay: 3, MaxStorageMB: 10},
	})

	allowed, remaining := qm.CheckToolCall("p1")
	if !allowed || remaining != 2 {
		t.Fatalf("first call: allowed=%v, remaining=%d; want true, 2", allowed, remaining)
	}

	allowed, remaining = qm.CheckToolCall("p1")
	if !allowed || remaining != 1 {
		t.Fatalf("second call: allowed=%v, remaining=%d; want true, 1", allowed, remaining)
	}

	allowed, remaining = qm.CheckToolCall("p1")
	if !allowed || remaining != 0 {
		t.Fatalf("third call: allowed=%v, remaining=%d; want true, 0", allowed, remaining)
	}

	allowed, remaining = qm.CheckToolCall("p1")
	if allowed || remaining != 0 {
		t.Fatalf("fourth call: allowed=%v, remaining=%d; want false, 0", allowed, remaining)
	}

	// Unconfigured plugin
	allowed, remaining = qm.CheckToolCall("unknown")
	if !allowed || remaining != -1 {
		t.Fatalf("unconfigured: allowed=%v, remaining=%d; want true, -1", allowed, remaining)
	}
}

func TestQuotaManager_StorageQuota(t *testing.T) {
	// mock storage
	store := &mockStorage{
		data: map[string]string{
			"k1": string(make([]byte, 1024)), // 1KB
			"k2": string(make([]byte, 2048)), // 2KB
		},
	}

	qm := NewPluginQuotaManager(map[string]PluginQuota{
		"p1": {MaxToolCallsPerDay: 100, MaxStorageMB: 1}, // 1MB limit
	})
	qm.SetStorage("p1", store)

	ok, usedBytes := qm.CheckStorage("p1")
	if !ok {
		t.Fatal("3KB usage should be within 1MB quota")
	}
	if usedBytes != 3072 {
		t.Fatalf("used bytes should be 3072, got %d", usedBytes)
	}

	// Now set a very small quota to trigger violation
	qm.SetQuota("p1", PluginQuota{MaxToolCallsPerDay: 100, MaxStorageMB: 0})
	ok, usedBytes = qm.CheckStorage("p1")
	if ok {
		t.Fatal("0MB quota should reject any storage usage")
	}
	if usedBytes != 3072 {
		t.Fatalf("used bytes should still be 3072, got %d", usedBytes)
	}

	// GetQuotaUsage
	qm.SetQuota("p1", PluginQuota{MaxToolCallsPerDay: 100, MaxStorageMB: 1})
	qm.CheckToolCall("p1") // make 1 call
	toolCalls, storageBytes := qm.GetQuotaUsage("p1")
	if toolCalls != 1 {
		t.Fatalf("tool calls should be 1, got %d", toolCalls)
	}
	if storageBytes != 3072 {
		t.Fatalf("storage bytes should be 3072, got %d", storageBytes)
	}
}

// ---------------------------------------------------------------------------
// Concurrency tests
// ---------------------------------------------------------------------------

func TestRateLimiter_ConcurrentAccess(t *testing.T) {
	rl := NewPluginRateLimiter(map[string]RateLimit{
		"p1": {MaxCalls: 1000, Window: time.Minute},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow("p1")
			rl.Remaining("p1")
			rl.Allow("p1")
		}()
	}
	wg.Wait()

	// Should not panic; 200 calls made, limit is 1000, so all should succeed
	if r := rl.Remaining("p1"); r < 800 {
		t.Fatalf("expected at least 800 remaining, got %d", r)
	}
}

func TestQuotaManager_ConcurrentAccess(t *testing.T) {
	qm := NewPluginQuotaManager(map[string]PluginQuota{
		"p1": {MaxToolCallsPerDay: 10000, MaxStorageMB: 100},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			qm.CheckToolCall("p1")
			qm.GetQuotaUsage("p1")
		}()
	}
	wg.Wait()

	toolCalls, _ := qm.GetQuotaUsage("p1")
	if toolCalls != 100 {
		t.Fatalf("expected 100 tool calls, got %d", toolCalls)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// mockStorage implements StorageAccessor for testing.
type mockStorage struct {
	mu   sync.RWMutex
	data map[string]string
}

func (m *mockStorage) Get(key string) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	return v, ok
}

func (m *mockStorage) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *mockStorage) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *mockStorage) Keys() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	keys := make([]string, 0, len(m.data))
	for k := range m.data {
		keys = append(keys, k)
	}
	return keys
}

func (m *mockStorage) Clear() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = make(map[string]string)
	return nil
}

// ---------------------------------------------------------------------------
// SetRateLimit / ResetDaily Tests
// ---------------------------------------------------------------------------

func TestRateLimiter_SetRateLimit(t *testing.T) {
	rl := NewPluginRateLimiter(nil)

	// No limit configured — should allow
	if !rl.Allow("plugin-1") {
		t.Error("expected allow with no limit")
	}

	// Set limit dynamically
	rl.SetRateLimit("plugin-1", RateLimit{MaxCalls: 1, Window: time.Minute})
	if !rl.Allow("plugin-1") {
		t.Error("expected first call to be allowed")
	}
	if rl.Allow("plugin-1") {
		t.Error("expected second call to be rate limited")
	}
}

func TestQuotaManager_ResetDaily(t *testing.T) {
	qm := NewPluginQuotaManager(map[string]PluginQuota{
		"p1": {MaxToolCallsPerDay: 5},
	})

	// Consume some quota
	for i := 0; i < 3; i++ {
		qm.CheckToolCall("p1")
	}

	used, _ := qm.GetQuotaUsage("p1")
	if used != 3 {
		t.Errorf("expected 3 tool calls, got %d", used)
	}

	// Reset daily
	qm.ResetDaily()

	used, _ = qm.GetQuotaUsage("p1")
	if used != 0 {
		t.Errorf("expected 0 tool calls after reset, got %d", used)
	}
}
