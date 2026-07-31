package cli

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"xbot/protocol"
)

// dummyCountModel is a minimal tea.Model for creating a tea.Program in tests.
type dummyCountModel struct{}

func (m *dummyCountModel) Init() tea.Cmd                       { return nil }
func (m *dummyCountModel) Update(tea.Msg) (tea.Model, tea.Cmd) { return m, nil }
func (m *dummyCountModel) View() tea.View                      { return tea.NewView("") }

// newTestProgram creates a minimal tea.Program suitable for SendProgress tests.
// The program is started in a goroutine and must be killed by the caller.
func newTestProgram() *tea.Program {
	m := &dummyCountModel{}
	p := tea.NewProgram(m, tea.WithoutRenderer(), tea.WithoutSignals(), tea.WithInput(nil))
	go func() { _, _ = p.Run() }()
	time.Sleep(20 * time.Millisecond)
	return p
}

// TestSendProgress_DifferentIteration_ForwardsOldEvent verifies that when two
// structured events from different iterations arrive before the drain goroutine
// delivers the first one, the older event is forwarded to asyncCh instead of
// being silently dropped. Without this, intermediate iterations disappear from
// the TUI (e.g. A→B→C becomes A→C).
func TestSendProgress_DifferentIteration_ForwardsOldEvent(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})
	p := newTestProgram()
	defer p.Quit()
	ch.programMu.Lock()
	ch.program = p
	ch.programMu.Unlock()

	// Send iteration 1 structured event
	iter1Payload := &protocol.ProgressEvent{
		ChatID:    "cli:test",
		Phase:     "running",
		Iteration: 1,
		Content:   "iteration 1 content",
	}
	ch.SendProgress("test", iter1Payload)

	// Send iteration 2 structured event WITHOUT draining iteration 1 first.
	// This replaces the slot — iteration 1 should be forwarded to asyncCh.
	iter2Payload := &protocol.ProgressEvent{
		ChatID:    "cli:test",
		Phase:     "running",
		Iteration: 2,
		Content:   "iteration 2 content",
	}
	ch.SendProgress("test", iter2Payload)

	// The slot should now hold iteration 2.
	ch.progressMu.Lock()
	slot := ch.progressSlot
	ch.progressMu.Unlock()
	if slot == nil {
		t.Fatal("progressSlot is nil after SendProgress")
	}
	if slot.Iteration != 2 {
		t.Errorf("progressSlot iteration = %d, want 2", slot.Iteration)
	}

	// asyncCh should contain the forwarded iteration 1 event.
	// It may also contain a cliProgressMsg from the progressSignal drain, but
	// we only care that iteration 1 was forwarded.
	foundIter1 := false
	drainTimeout := time.After(500 * time.Millisecond)
	for {
		select {
		case msg := <-ch.asyncCh:
			if pm, ok := msg.(cliProgressMsg); ok && pm.payload != nil {
				if pm.payload.Iteration == 1 {
					foundIter1 = true
				}
			}
		case <-drainTimeout:
			if !foundIter1 {
				t.Fatal("iteration 1 event was not forwarded to asyncCh — it was silently dropped")
			}
			return
		}
	}
}

// TestSendProgress_SameIteration_MergesStreamFields verifies that stream-only
// events merge into a structured slot of the same iteration without replacing it.
func TestSendProgress_SameIteration_MergesStreamFields(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})
	p := newTestProgram()
	defer p.Quit()
	ch.programMu.Lock()
	ch.program = p
	ch.programMu.Unlock()

	// Send structured event for iteration 1
	ch.SendProgress("test", &protocol.ProgressEvent{
		ChatID:    "cli:test",
		Phase:     "running",
		Iteration: 1,
	})

	// Send stream-only event (Phase=="", Iteration==0) — should merge, not replace
	ch.SendProgress("test", &protocol.ProgressEvent{
		ChatID:        "cli:test",
		StreamContent: "streaming text...",
	})

	ch.progressMu.Lock()
	slot := ch.progressSlot
	ch.progressMu.Unlock()

	if slot == nil {
		t.Fatal("progressSlot is nil")
	}
	// Structured event should still be there (stream-only can't evict structured)
	if slot.Iteration != 1 {
		t.Errorf("slot iteration = %d, want 1 (stream-only must not evict structured)", slot.Iteration)
	}
	if slot.StreamContent != "streaming text..." {
		t.Errorf("slot StreamContent = %q, want %q", slot.StreamContent, "streaming text...")
	}
}

// TestSendProgress_StreamOnly_DoesNotReplaceStructured verifies that a stream-only
// event arriving after a structured event preserves the structured event's iteration.
func TestSendProgress_StreamOnly_DoesNotReplaceStructured(t *testing.T) {
	ch := NewCLIChannel(&CLIChannelConfig{})
	p := newTestProgram()
	defer p.Quit()
	ch.programMu.Lock()
	ch.program = p
	ch.programMu.Unlock()

	// Structured event
	ch.SendProgress("test", &protocol.ProgressEvent{
		ChatID:    "cli:test",
		Phase:     "thinking",
		Iteration: 3,
	})

	// Stream-only event
	ch.SendProgress("test", &protocol.ProgressEvent{
		ChatID:        "cli:test",
		StreamContent: "partial...",
	})

	// Another stream-only event
	ch.SendProgress("test", &protocol.ProgressEvent{
		ChatID:                 "cli:test",
		ReasoningStreamContent: "thinking...",
	})

	ch.progressMu.Lock()
	slot := ch.progressSlot
	ch.progressMu.Unlock()

	if slot == nil {
		t.Fatal("progressSlot is nil")
	}
	if slot.Phase != "thinking" {
		t.Errorf("slot Phase = %q, want %q", slot.Phase, "thinking")
	}
	if slot.Iteration != 3 {
		t.Errorf("slot Iteration = %d, want 3", slot.Iteration)
	}
	if slot.StreamContent != "partial..." {
		t.Errorf("slot StreamContent = %q, want %q", slot.StreamContent, "partial...")
	}
	if slot.ReasoningStreamContent != "thinking..." {
		t.Errorf("slot ReasoningStreamContent = %q, want %q", slot.ReasoningStreamContent, "thinking...")
	}
}
