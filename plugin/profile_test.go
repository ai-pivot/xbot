package plugin

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Profiler tests
// ---------------------------------------------------------------------------

func TestProfiler_RecordToolCall(t *testing.T) {
	p := NewProfiler()

	p.RecordToolCall("p1", 50*time.Millisecond)
	p.RecordToolCall("p1", 30*time.Millisecond)

	prof := p.GetProfile("p1")
	if prof.PluginID != "p1" {
		t.Fatalf("expected PluginID=p1, got %s", prof.PluginID)
	}
	if prof.ToolCalls != 2 {
		t.Fatalf("expected 2 tool calls, got %d", prof.ToolCalls)
	}
	if prof.ToolCallTime != 80*time.Millisecond {
		t.Fatalf("expected 80ms total, got %v", prof.ToolCallTime)
	}
	if prof.LastToolCall.IsZero() {
		t.Fatal("LastToolCall should not be zero")
	}

	// Hook fields should remain zero
	if prof.HookCalls != 0 {
		t.Fatalf("expected 0 hook calls, got %d", prof.HookCalls)
	}
}

func TestProfiler_RecordHookCall(t *testing.T) {
	p := NewProfiler()

	p.RecordHookCall("p1", 20*time.Millisecond)
	p.RecordHookCall("p1", 10*time.Millisecond)

	prof := p.GetProfile("p1")
	if prof.HookCalls != 2 {
		t.Fatalf("expected 2 hook calls, got %d", prof.HookCalls)
	}
	if prof.HookCallTime != 30*time.Millisecond {
		t.Fatalf("expected 30ms total, got %v", prof.HookCallTime)
	}
	if prof.LastHookCall.IsZero() {
		t.Fatal("LastHookCall should not be zero")
	}
}

func TestProfiler_RecordEnricherCall(t *testing.T) {
	p := NewProfiler()

	p.RecordEnricherCall("p1", 15*time.Millisecond)
	p.RecordEnricherCall("p1", 25*time.Millisecond)

	prof := p.GetProfile("p1")
	if prof.EnricherCalls != 2 {
		t.Fatalf("expected 2 enricher calls, got %d", prof.EnricherCalls)
	}
	if prof.EnricherCallTime != 40*time.Millisecond {
		t.Fatalf("expected 40ms total, got %v", prof.EnricherCallTime)
	}
	if prof.LastEnricherCall.IsZero() {
		t.Fatal("LastEnricherCall should not be zero")
	}
}

func TestProfiler_GetAllProfiles(t *testing.T) {
	p := NewProfiler()

	p.RecordToolCall("p1", 10*time.Millisecond)
	p.RecordHookCall("p2", 20*time.Millisecond)
	p.RecordEnricherCall("p3", 30*time.Millisecond)

	profiles := p.GetAllProfiles()
	if len(profiles) != 3 {
		t.Fatalf("expected 3 profiles, got %d", len(profiles))
	}

	if profiles["p1"].ToolCalls != 1 {
		t.Fatalf("expected p1 tool calls=1, got %d", profiles["p1"].ToolCalls)
	}
	if profiles["p2"].HookCalls != 1 {
		t.Fatalf("expected p2 hook calls=1, got %d", profiles["p2"].HookCalls)
	}
	if profiles["p3"].EnricherCalls != 1 {
		t.Fatalf("expected p3 enricher calls=1, got %d", profiles["p3"].EnricherCalls)
	}

	// Returned map should be a copy — mutations should not affect the Profiler
	tmp := profiles["p1"]
	tmp.ToolCalls = 999
	profiles["p1"] = tmp
	prof := p.GetProfile("p1")
	if prof.ToolCalls != 1 {
		t.Fatal("GetAllProfiles should return a copy, not a reference")
	}
}

func TestProfiler_GetProfile_NonExistent(t *testing.T) {
	p := NewProfiler()

	prof := p.GetProfile("nonexistent")
	if prof.PluginID != "nonexistent" {
		t.Fatalf("expected PluginID=nonexistent, got %s", prof.PluginID)
	}
	if prof.ToolCalls != 0 || prof.HookCalls != 0 || prof.EnricherCalls != 0 {
		t.Fatal("non-existent plugin should have zero-value counters")
	}
}

func TestProfiler_Reset(t *testing.T) {
	p := NewProfiler()

	p.RecordToolCall("p1", 10*time.Millisecond)
	p.RecordToolCall("p2", 20*time.Millisecond)

	p.Reset("p1")

	prof := p.GetProfile("p1")
	if prof.ToolCalls != 0 {
		t.Fatalf("expected p1 tool calls=0 after reset, got %d", prof.ToolCalls)
	}

	// p2 should be unaffected
	prof = p.GetProfile("p2")
	if prof.ToolCalls != 1 {
		t.Fatalf("expected p2 tool calls=1, got %d", prof.ToolCalls)
	}

	// Reset non-existent should not panic
	p.Reset("nonexistent")
}

func TestProfiler_ResetAll(t *testing.T) {
	p := NewProfiler()

	p.RecordToolCall("p1", 10*time.Millisecond)
	p.RecordHookCall("p2", 20*time.Millisecond)

	p.ResetAll()

	profiles := p.GetAllProfiles()
	if len(profiles) != 0 {
		t.Fatalf("expected 0 profiles after ResetAll, got %d", len(profiles))
	}
}

func TestProfiler_Concurrent(t *testing.T) {
	p := NewProfiler()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pid := "p1"
			if id%2 == 0 {
				pid = "p2"
			}
			p.RecordToolCall(pid, time.Millisecond)
			p.RecordHookCall(pid, time.Millisecond)
			p.RecordEnricherCall(pid, time.Millisecond)
			p.GetProfile(pid)
			p.GetAllProfiles()
			if id%10 == 0 {
				p.Reset(pid)
			}
			if id%20 == 0 {
				p.ResetAll()
			}
		}(i)
	}
	wg.Wait()

	// After concurrent operations, counters should be non-negative
	for _, pid := range []string{"p1", "p2"} {
		prof := p.GetProfile(pid)
		if prof.ToolCalls < 0 {
			t.Fatalf("ToolCalls should not be negative, got %d", prof.ToolCalls)
		}
		if prof.HookCalls < 0 {
			t.Fatalf("HookCalls should not be negative, got %d", prof.HookCalls)
		}
		if prof.EnricherCalls < 0 {
			t.Fatalf("EnricherCalls should not be negative, got %d", prof.EnricherCalls)
		}
	}
}
