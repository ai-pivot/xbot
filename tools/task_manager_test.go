package tools

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestAdoptRealtimeOutputSync guards the real-time progress fix: a timed-out
// command adopted as a background task (Adopt) must keep task.Output in sync
// with the live capture buffer while the process still runs — before the fix,
// Output was frozen at the adoption-time partialOutput snapshot and only
// overwritten once at completion, so the web /api/tasks/list poll (BackgroundPanel)
// showed stale output for the entire run.
func TestAdoptRealtimeOutputSync(t *testing.T) {
	m := NewBackgroundTaskManager()

	// Real process: Adopt's poll loop calls isProcessAlive(proc.Pid).
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	// ongoingOutput simulates the capture buffer with lock-protected growth
	// (mirrors none_sandbox's snapshotOutput: non-blocking locked snapshot).
	var mu sync.Mutex
	chunks := []string{"initial partial\n"}
	ongoing := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(chunks, "")
	}

	exitCh := make(chan int, 1)
	task := m.Adopt("test-sess", "test-sender", "test cmd", cmd.Process, "initial partial\n", exitCh, ongoing)

	// Adoption-time snapshot.
	if got := task.CurrentOutput(); got != "initial partial\n" {
		t.Fatalf("initial CurrentOutput = %q, want adoption snapshot %q", got, "initial partial\n")
	}

	// Simulate the capture goroutine appending live output.
	mu.Lock()
	chunks = append(chunks, "chunk-2 (real-time)\n", "chunk-3\n")
	mu.Unlock()

	// Wait for the Adopt ticker (500ms) to sync the live snapshot into
	// task.Output. Before the fix this polls "initial partial" forever.
	deadline := time.Now().Add(3 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = task.CurrentOutput()
		if strings.Contains(got, "chunk-3") {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(got, "chunk-3") {
		t.Fatalf("Adopt did not sync live output while running (frozen at snapshot): got %q", got)
	}
	if !strings.Contains(got, "chunk-2") || !strings.Contains(got, "initial partial") {
		t.Fatalf("synced output should keep the full buffer (prefix preserved): got %q", got)
	}

	// Completion path: kill the process first (real timing — exitCodeCh is fed
	// by the capture goroutine AFTER process death; Adopt's inner poll loop only
	// reads exitCodeCh once isProcessAlive goes false), then fire the channel.
	// Final snapshot wins, status becomes done.
	mu.Lock()
	chunks = append(chunks, "final\n")
	mu.Unlock()
	// Real timing: the capture goroutine reaps the process BEFORE feeding
	// exitCodeCh — otherwise the child stays a zombie and signal-0 probes
	// (isProcessAlive) keep returning true, so Adopt's poll loop never exits.
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	exitCh <- 0
	select {
	case <-task.done:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not finish after exitCh fired")
	}
	if got := task.CurrentOutput(); !strings.Contains(got, "final") {
		t.Fatalf("final output should include the completion snapshot: got %q", got)
	}
	if task.Status != BgTaskDone {
		t.Fatalf("status = %q, want %q", task.Status, BgTaskDone)
	}
}

// TestAdoptRealtimeOutputSyncTailTruncate guards the 50KB tail cap on the
// real-time sync path (same cap as the Start path's outputBuf) — a chatty task
// must not grow task.Output unboundedly while running.
func TestAdoptRealtimeOutputSyncTailTruncate(t *testing.T) {
	m := NewBackgroundTaskManager()

	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot start sleep process: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	big := strings.Repeat("x", maxBgOutputSize+1024)
	ongoing := func() string { return big }

	exitCh := make(chan int, 1)
	task := m.Adopt("test-sess", "test-sender", "tail test", cmd.Process, "", exitCh, ongoing)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(task.CurrentOutput()) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got := task.CurrentOutput(); len(got) != maxBgOutputSize {
		t.Fatalf("running output should be tail-truncated to %d bytes, got %d", maxBgOutputSize, len(got))
	}

	// Completion path (real timing: process death precedes exitCodeCh; reap
	// the zombie so isProcessAlive goes false).
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	exitCh <- 0
	select {
	case <-task.done:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not finish after exitCh fired")
	}
	if got := task.CurrentOutput(); len(got) != maxBgOutputSize {
		t.Fatalf("final output should also be tail-truncated, got %d bytes", len(got))
	}
}
