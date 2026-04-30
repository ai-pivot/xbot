package plugin

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// CallTracer tests
// ---------------------------------------------------------------------------

func TestCallTracer_Record(t *testing.T) {
	ct := NewCallTracer(10)

	now := time.Now()
	ct.Record(CallTrace{
		PluginID:  "p1",
		ToolName:  "tool_a",
		StartTime: now,
		EndTime:   now.Add(100 * time.Millisecond),
		Duration:  100 * time.Millisecond,
		InputLen:  50,
		OutputLen: 200,
	})

	recent := ct.Recent(1)
	if len(recent) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(recent))
	}
	if recent[0].PluginID != "p1" || recent[0].ToolName != "tool_a" {
		t.Fatalf("unexpected trace: %+v", recent[0])
	}
	if recent[0].Duration != 100*time.Millisecond {
		t.Fatalf("expected duration 100ms, got %v", recent[0].Duration)
	}
	if recent[0].InputLen != 50 || recent[0].OutputLen != 200 {
		t.Fatalf("expected inputLen=50, outputLen=200, got %d, %d", recent[0].InputLen, recent[0].OutputLen)
	}
}

func TestCallTracer_Recent(t *testing.T) {
	ct := NewCallTracer(10)

	// Record 3 traces with distinct StartTimes
	for i := 0; i < 3; i++ {
		ct.Record(CallTrace{
			PluginID:  "p1",
			ToolName:  "tool",
			StartTime: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		})
	}

	// n > count → return all
	recent := ct.Recent(100)
	if len(recent) != 3 {
		t.Fatalf("expected 3 traces, got %d", len(recent))
	}

	// Newest first
	if !recent[0].StartTime.After(recent[1].StartTime) {
		t.Fatal("traces should be in reverse chronological order (0 before 1)")
	}
	if !recent[1].StartTime.After(recent[2].StartTime) {
		t.Fatal("traces should be in reverse chronological order (1 before 2)")
	}

	// n = 0 → nil
	if ct.Recent(0) != nil {
		t.Fatal("Recent(0) should return nil")
	}

	// n = 2 → return 2 most recent
	recent = ct.Recent(2)
	if len(recent) != 2 {
		t.Fatalf("expected 2 traces, got %d", len(recent))
	}
}

func TestCallTracer_ByPlugin(t *testing.T) {
	ct := NewCallTracer(10)

	ct.Record(CallTrace{PluginID: "p1", ToolName: "tool_a"})
	ct.Record(CallTrace{PluginID: "p2", ToolName: "tool_b"})
	ct.Record(CallTrace{PluginID: "p1", ToolName: "tool_c"})

	traces := ct.ByPlugin("p1")
	if len(traces) != 2 {
		t.Fatalf("expected 2 traces for p1, got %d", len(traces))
	}
	// Newest first — tool_c was recorded after tool_a
	if traces[0].ToolName != "tool_c" {
		t.Fatalf("expected tool_c first, got %s", traces[0].ToolName)
	}

	traces = ct.ByPlugin("p2")
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace for p2, got %d", len(traces))
	}

	// Non-existent plugin
	traces = ct.ByPlugin("p3")
	if len(traces) != 0 {
		t.Fatalf("expected 0 traces for p3, got %d", len(traces))
	}
}

func TestCallTracer_MaxTraces(t *testing.T) {
	ct := NewCallTracer(3)

	// Record 5 traces — only last 3 should survive
	for i := 0; i < 5; i++ {
		ct.Record(CallTrace{
			PluginID:  "p1",
			ToolName:  "tool",
			StartTime: time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC),
		})
	}

	recent := ct.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("expected 3 traces (maxTraces), got %d", len(recent))
	}

	// Newest first: StartTime seconds should be 4, 3, 2
	for i, tr := range recent {
		expectedSec := 4 - i
		gotSec := tr.StartTime.Second()
		if gotSec != expectedSec {
			t.Fatalf("trace[%d]: expected second=%d, got %d", i, expectedSec, gotSec)
		}
	}
}

func TestCallTracer_Clear(t *testing.T) {
	ct := NewCallTracer(10)

	for i := 0; i < 5; i++ {
		ct.Record(CallTrace{PluginID: "p1", ToolName: "tool"})
	}

	ct.Clear()

	recent := ct.Recent(10)
	if len(recent) != 0 {
		t.Fatalf("expected 0 traces after Clear, got %d", len(recent))
	}

	// Verify we can record again after Clear
	ct.Record(CallTrace{PluginID: "p1", ToolName: "tool_after_clear"})
	recent = ct.Recent(1)
	if len(recent) != 1 {
		t.Fatalf("expected 1 trace after post-clear record, got %d", len(recent))
	}
	if recent[0].ToolName != "tool_after_clear" {
		t.Fatalf("expected tool_after_clear, got %s", recent[0].ToolName)
	}
}

func TestCallTracer_DefaultMaxTraces(t *testing.T) {
	ct := NewCallTracer(0) // should use default 100
	ct.Record(CallTrace{PluginID: "p1", ToolName: "tool"})
	if recent := ct.Recent(1); len(recent) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(recent))
	}

	ct = NewCallTracer(-5) // should use default 100
	ct.Record(CallTrace{PluginID: "p1", ToolName: "tool"})
	if recent := ct.Recent(1); len(recent) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(recent))
	}
}

func TestCallTracer_EmptyBuffer(t *testing.T) {
	ct := NewCallTracer(10)

	// Recent on empty buffer
	if traces := ct.Recent(5); len(traces) != 0 {
		t.Fatalf("expected 0 traces from empty buffer, got %d", len(traces))
	}

	// ByPlugin on empty buffer
	if traces := ct.ByPlugin("p1"); len(traces) != 0 {
		t.Fatalf("expected 0 traces from empty buffer, got %d", len(traces))
	}
}

func TestCallTracer_Concurrent(t *testing.T) {
	ct := NewCallTracer(50)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ct.Record(CallTrace{PluginID: "p1", ToolName: "tool"})
			ct.Recent(5)
			ct.ByPlugin("p1")
			if id%10 == 0 {
				ct.Clear()
			}
		}(i)
	}
	wg.Wait()

	// Should not panic — that's the main assertion
}
