package agent

import (
	"context"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

func sessionOperationGateCount(a *Agent) int {
	a.sessionOperationGatesMu.Lock()
	defer a.sessionOperationGatesMu.Unlock()
	return len(a.sessionOperationGates)
}

func TestSessionOperationGateReleasesIdleEntries(t *testing.T) {
	a := &Agent{}
	for i := 0; i < 1000; i++ {
		lease := a.sessionOperationGate("web", "short-lived-"+strconv.Itoa(i))
		if !lease.lock(context.Background()) {
			t.Fatal("failed to acquire session operation gate")
		}
		lease.unlock()
	}
	if count := sessionOperationGateCount(a); count != 0 {
		t.Fatalf("idle session operation gates=%d, want 0", count)
	}
}

func TestSessionOperationGateFailedAcquireReleasesLease(t *testing.T) {
	a := &Agent{}
	holder := a.sessionOperationGate("web", "chat")
	if !holder.lock(context.Background()) {
		t.Fatal("failed to acquire holder")
	}
	blocked := a.sessionOperationGate("web", "chat")
	if blocked.tryLock() {
		t.Fatal("tryLock acquired a held gate")
	}
	if count := sessionOperationGateCount(a); count != 1 {
		t.Fatalf("gate count while held=%d, want 1", count)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	canceled := a.sessionOperationGate("web", "chat")
	if canceled.lock(canceledCtx) {
		t.Fatal("canceled lock acquired a held gate")
	}
	if count := sessionOperationGateCount(a); count != 1 {
		t.Fatalf("gate count after canceled waiter=%d, want 1", count)
	}
	holder.unlock()
	if count := sessionOperationGateCount(a); count != 0 {
		t.Fatalf("gate count after final release=%d, want 0", count)
	}
}

func TestSessionOperationGateConcurrentReclamationDoesNotSplitGate(t *testing.T) {
	a := &Agent{}
	const workers = 32
	const iterations = 100
	var inside atomic.Int32
	var overlap atomic.Bool
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < iterations; j++ {
				lease := a.sessionOperationGate("web", "shared")
				if !lease.lock(context.Background()) {
					overlap.Store(true)
					return
				}
				if inside.Add(1) != 1 {
					overlap.Store(true)
				}
				runtime.Gosched()
				inside.Add(-1)
				lease.unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	if overlap.Load() {
		t.Fatal("same-session operations overlapped during gate reclamation")
	}
	if count := sessionOperationGateCount(a); count != 0 {
		t.Fatalf("idle session operation gates=%d, want 0", count)
	}
}
