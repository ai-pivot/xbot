package plugin

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Call Tracer — ring-buffered tool call audit trail
// ---------------------------------------------------------------------------

// CallTrace records a plugin tool call for tracing/auditing.
type CallTrace struct {
	PluginID  string
	ToolName  string
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
	InputLen  int
	OutputLen int
	IsError   bool
}

const defaultMaxTraces = 100

// CallTracer maintains a fixed-capacity ring buffer of CallTrace entries.
// It is safe for concurrent use.
type CallTracer struct {
	mu        sync.Mutex
	traces    []CallTrace
	head      int // next write position
	count     int // number of valid entries (capped at maxTraces)
	maxTraces int
}

// NewCallTracer creates a CallTracer with the given buffer capacity.
// If maxTraces <= 0, defaultMaxTraces (100) is used.
func NewCallTracer(maxTraces int) *CallTracer {
	if maxTraces <= 0 {
		maxTraces = defaultMaxTraces
	}
	return &CallTracer{
		traces:    make([]CallTrace, maxTraces),
		maxTraces: maxTraces,
	}
}

// Record appends a CallTrace to the ring buffer.
// If the buffer is full, the oldest entry is overwritten.
func (ct *CallTracer) Record(trace CallTrace) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.traces[ct.head] = trace
	ct.head = (ct.head + 1) % ct.maxTraces
	if ct.count < ct.maxTraces {
		ct.count++
	}
}

// Recent returns up to n most recent traces in reverse chronological order
// (newest first). If n <= 0, returns nil.
func (ct *CallTracer) Recent(n int) []CallTrace {
	if n <= 0 {
		return nil
	}
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if n > ct.count {
		n = ct.count
	}
	result := make([]CallTrace, n)
	for i := 0; i < n; i++ {
		idx := (ct.head - 1 - i + ct.maxTraces) % ct.maxTraces
		result[i] = ct.traces[idx]
	}
	return result
}

// ByPlugin returns all traces for the given pluginID in reverse chronological
// order (newest first).
func (ct *CallTracer) ByPlugin(pluginID string) []CallTrace {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	var result []CallTrace
	for i := 0; i < ct.count; i++ {
		idx := (ct.head - 1 - i + ct.maxTraces) % ct.maxTraces
		if ct.traces[idx].PluginID == pluginID {
			result = append(result, ct.traces[idx])
		}
	}
	return result
}

// Clear removes all stored traces.
func (ct *CallTracer) Clear() {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	ct.head = 0
	ct.count = 0
}
