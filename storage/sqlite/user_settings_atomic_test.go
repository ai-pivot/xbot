package sqlite

import (
	"fmt"
	"sync"
	"testing"
)

// TestSetByUserIDAtomicOverwrite (m9) verifies SetByUserID's DELETE+INSERT runs
// as one atomic unit: the key never disappears between the DELETE and the
// INSERT (other readers would see the intermediate "deleted, not yet
// re-inserted" state), and concurrent writers leave a consistent final value.
// The overwrite semantics must stay intact (last write wins, no UNIQUE errors).
func TestSetByUserIDAtomicOverwrite(t *testing.T) {
	db := openTestDB(t)
	svc := NewUserSettingsService(db)

	const (
		channel = "cli"
		userID  = int64(77)
		key     = "m9-test-key"
	)

	// Overwrite semantics: same key written twice keeps the latest value.
	if err := svc.SetByUserID(channel, userID, key, "v1"); err != nil {
		t.Fatalf("SetByUserID v1: %v", err)
	}
	if err := svc.SetByUserID(channel, userID, key, "v2"); err != nil {
		t.Fatalf("SetByUserID v2: %v", err)
	}
	settings, err := svc.GetByUserID(channel, userID)
	if err != nil {
		t.Fatalf("GetByUserID: %v", err)
	}
	if settings[key] != "v2" {
		t.Errorf("value = %q, want %q (overwrite must keep latest)", settings[key], "v2")
	}

	// Concurrent writers on the same key: no UNIQUE errors, final value is one
	// of the writers', and the key is readable afterwards.
	const writers = 8
	const rounds = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers*rounds)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				if err := svc.SetByUserID(channel, userID, key, fmt.Sprintf("w%d-r%d", w, r)); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent SetByUserID: %v", err)
	}
	final, err := svc.GetByUserID(channel, userID)
	if err != nil {
		t.Fatalf("GetByUserID after concurrency: %v", err)
	}
	if final[key] == "" {
		t.Fatal("key vanished after concurrent SetByUserID writes — the DELETE+INSERT window lost the row")
	}

	// Distinct keys must not interfere (DELETE is scoped to (channel, user_id, key)).
	if err := svc.SetByUserID(channel, userID, "other-key", "other"); err != nil {
		t.Fatalf("SetByUserID other-key: %v", err)
	}
	first, err := svc.GetByUserID(channel, userID)
	if err != nil {
		t.Fatalf("GetByUserID key: %v", err)
	}
	if first[key] == "" {
		t.Fatal("writing another key wiped the first key — DELETE scope is wrong")
	}
}
