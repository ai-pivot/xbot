package sqlite

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestWriteGate_SerializesWriteTransactions is the guard for the 2026-09-18 P0:
// a background SubAgent's turn was aborted with
//
//	persist message batch: begin immediate history write:
//	database is locked (5) (SQLITE_BUSY)
//
// SQLite allows exactly ONE writer, and the modernc (pure-Go) driver can return
// SQLITE_BUSY on the write-lock acquisition path WITHOUT consulting
// busy_timeout (the hole user_token_usage.go originally worked around with its
// own mutex). Therefore every write transaction holds the process-wide gate
// (db.writeMu) for its whole BEGIN..COMMIT window — serializing writers at the
// source instead of retrying into the collision.
//
// Why it matters at scale (frequency grows with uptime): the history write
// holds the write lock across a replay/validation read, so the hold time grows
// with history size — and with concurrent sessions/SubAgents (each its own
// tenant, so the per-tenant striped historyLocks do NOT serialize the single
// global SQLite write lock) collisions become ever more likely.
//
// Mutation check: drop the gate from withImmediateHistoryWrite → the observed
// max concurrency becomes > 1 and this test fails.
func TestWriteGate_SerializesWriteTransactions(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "write-gate.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	svc := NewSessionService(db)

	const workers = 8
	var inTx, maxSeen int64
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := svc.withImmediateHistoryWrite(func(_ historyQueryExecer) error {
				cur := atomic.AddInt64(&inTx, 1)
				for {
					old := atomic.LoadInt64(&maxSeen)
					if cur <= old || atomic.CompareAndSwapInt64(&maxSeen, old, cur) {
						break
					}
				}
				// Widen the overlap window so an unsynchronized
				// implementation is guaranteed to be observed overlapping.
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt64(&inTx, -1)
				return nil
			}); err != nil {
				t.Errorf("withImmediateHistoryWrite: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&maxSeen); got != 1 {
		t.Fatalf("write gate must serialize write transactions: max concurrent = %d, want 1", got)
	}
}

// TestWriteGate_AllWritersSucceedConcurrently: no writer may ever fail with a
// lock error when the gate is in place — the P0 signature (SQLITE_BUSY) must be
// unreachable from in-process concurrency.
func TestWriteGate_AllWritersSucceedConcurrently(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "write-gate-stress.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()
	svc := NewSessionService(db)

	const workers = 16
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.withImmediateHistoryWrite(func(_ historyQueryExecer) error {
				time.Sleep(time.Millisecond)
				return nil
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("no writer may fail under the gate, got: %v", err)
		}
	}
}
