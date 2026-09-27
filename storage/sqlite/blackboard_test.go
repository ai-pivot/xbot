package sqlite

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func newBoardEntry(board, key, title string) *BlackboardEntry {
	return &BlackboardEntry{Board: board, Key: key, Kind: "task", Title: title, Body: "body of " + key, CreatedBy: "cli:/repo"}
}

// TestBlackboard_PostGetList covers the happy path: post, get (body present),
// list (body omitted by default — a list is a digest, not a dump).
func TestBlackboard_PostGetList(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))

	created, err := svc.Post(newBoardEntry("@board1", "a", "first"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if created.Revision != 1 || created.Closed || created.Ready != true {
		t.Fatalf("post result = %+v, want revision 1, open, ready", created)
	}

	got, err := svc.Get("@board1", "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Body != "body of a" {
		t.Fatalf("get body = %q, want %q", got.Body, "body of a")
	}

	if _, err := svc.Post(newBoardEntry("@board1", "b", "second")); err != nil {
		t.Fatalf("post b: %v", err)
	}
	list, err := svc.List(BlackboardListOptions{Board: "@board1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	for _, e := range list {
		if e.Body != "" {
			t.Errorf("list entry %s carries body %q — lists must omit bodies unless requested", e.Key, e.Body)
		}
	}
	withBody, err := svc.List(BlackboardListOptions{Board: "@board1", IncludeBody: true})
	if err != nil {
		t.Fatalf("list with body: %v", err)
	}
	if withBody[0].Body == "" {
		t.Error("IncludeBody list must carry bodies")
	}
}

// TestBlackboard_PostDuplicateConflictKeepsExisting: a duplicate key must not
// overwrite a teammate's entry — the error carries the live entry so the caller
// can update it with the right revision.
func TestBlackboard_PostDuplicateConflictKeepsExisting(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	first, err := svc.Post(newBoardEntry("@board1", "a", "first"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	_, err = svc.Post(newBoardEntry("@board1", "a", "clobber"))
	var conflict *BlackboardConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate post error = %v, want BlackboardConflictError", err)
	}
	if conflict.Entry == nil || conflict.Entry.Revision != first.Revision {
		t.Fatalf("conflict entry = %+v, want the live entry at revision %d", conflict.Entry, first.Revision)
	}
	got, _ := svc.Get("@board1", "a")
	if got.Title != "first" {
		t.Fatalf("title = %q, want %q (duplicate post must not overwrite)", got.Title, "first")
	}
}

// TestBlackboard_UpdateCASIsLossless is the lost-update guard: two writers read
// the same revision, only the first write lands, and the loser learns the
// winner's value instead of clobbering it.
func TestBlackboard_UpdateCASIsLossless(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	base, err := svc.Post(newBoardEntry("@board1", "a", "first"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	winner, err := svc.Update("@board1", "a", "task", "winner", "w", "open", nil, base.Revision)
	if err != nil {
		t.Fatalf("update winner: %v", err)
	}
	if winner.Revision != base.Revision+1 {
		t.Fatalf("revision = %d, want %d", winner.Revision, base.Revision+1)
	}

	_, err = svc.Update("@board1", "a", "task", "loser", "l", "open", nil, base.Revision)
	var conflict *BlackboardConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale update error = %v, want BlackboardConflictError", err)
	}
	if conflict.Entry.Title != "winner" {
		t.Fatalf("conflict entry title = %q, want %q", conflict.Entry.Title, "winner")
	}
	if !strings.Contains(conflict.Reason, "revision mismatch") {
		t.Errorf("conflict reason = %q, want a revision-mismatch explanation", conflict.Reason)
	}
}

// TestBlackboard_ClaimIsAtomicUnderConcurrency: N workers race for one entry,
// exactly one wins (the losers get a conflict naming the holder).
func TestBlackboard_ClaimIsAtomicUnderConcurrency(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	if _, err := svc.Post(newBoardEntry("@board1", "a", "race")); err != nil {
		t.Fatalf("post: %v", err)
	}

	const workers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, losses := 0, 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Distinct identities: a claim by the SAME holder is legitimately an
			// extension, so the race must be between different workers (which is
			// what distinct agent sessions produce in practice).
			_, err := svc.Claim("@board1", "a", "worker-"+strconv.Itoa(i), "", BlackboardClaimTTL)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				losses++
				return
			}
			wins++
		}(i)
	}
	wg.Wait()

	if wins != 1 {
		t.Fatalf("claim wins = %d, want exactly 1 (%d losses)", wins, losses)
	}
	got, err := svc.Get("@board1", "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !strings.HasPrefix(got.ClaimedBy, "worker-") {
		t.Fatalf("claimed_by = %q, want one of the racing workers", got.ClaimedBy)
	}
	if got.Ready {
		t.Error("a claimed entry must not be ready")
	}
}

// TestBlackboard_ClaimLeaseExpiryHeals: an expired lease frees the entry again
// with no background sweeper — the next claim succeeds and the honest reader
// sees no holder.
func TestBlackboard_ClaimLeaseExpiryHeals(t *testing.T) {
	db := openTestDB(t)
	svc := NewBlackboardService(db)
	if _, err := svc.Post(newBoardEntry("@board1", "a", "stale")); err != nil {
		t.Fatalf("post: %v", err)
	}
	if _, err := svc.Claim("@board1", "a", "crashed-worker", "", BlackboardClaimTTL); err != nil {
		t.Fatalf("claim: %v", err)
	}

	// Simulate the holder dying: the lease lapses (TTL is 10m, too slow to wait
	// for in a test).
	if _, err := db.Conn().Exec(
		"UPDATE blackboard_entries SET claim_expires_at = ? WHERE board = '@board1' AND key = 'a'",
		time.Now().Add(-time.Minute).UnixMilli()); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	got, err := svc.Get("@board1", "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClaimedBy != "" {
		t.Fatalf("claimed_by = %q, want empty (expired lease must read as free)", got.ClaimedBy)
	}
	if !got.Ready {
		t.Error("an expired-lease entry must be ready again")
	}

	reclaimed, err := svc.Claim("@board1", "a", "healthy-worker", "", BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("reclaim after expiry: %v", err)
	}
	if reclaimed.ClaimedBy != "healthy-worker" {
		t.Fatalf("claimed_by = %q, want healthy-worker", reclaimed.ClaimedBy)
	}
}

// TestBlackboard_ClaimRules: re-claiming extends the lease for the holder, but a
// live lease blocks others; a closed entry must be reopened first.
func TestBlackboard_ClaimRules(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	if _, err := svc.Post(newBoardEntry("@board1", "a", "rules")); err != nil {
		t.Fatalf("post: %v", err)
	}
	first, err := svc.Claim("@board1", "a", "alice", "", BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	extended, err := svc.Claim("@board1", "a", "alice", first.ClaimToken, 2*BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("extend with the lease token: %v", err)
	}
	if extended.ClaimExpiresAt <= first.ClaimExpiresAt {
		t.Fatalf("lease not extended: %d -> %d", first.ClaimExpiresAt, extended.ClaimExpiresAt)
	}

	_, err = svc.Claim("@board1", "a", "bob", "", BlackboardClaimTTL)
	var conflict *BlackboardConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("claim by other = %v, want BlackboardConflictError", err)
	}
	if !strings.Contains(conflict.Reason, "alice") {
		t.Errorf("conflict reason = %q, want the live holder named", conflict.Reason)
	}

	closed, err := svc.SetClosed("@board1", "a", true, extended.Revision)
	if err != nil {
		t.Fatalf("close: %v", err)
	}
	if closed.Closed != true || closed.ClaimedBy != "" {
		t.Fatalf("closed entry = %+v, want closed with the lease released", closed)
	}
	_, err = svc.Claim("@board1", "a", "bob", "", BlackboardClaimTTL)
	if !errors.As(err, &conflict) {
		t.Fatalf("claim on closed entry = %v, want a conflict telling the caller to reopen", err)
	}
	if !strings.Contains(err.Error(), "reopen") {
		t.Errorf("error = %v, want an explicit reopen hint", err)
	}
	if _, err := svc.SetClosed("@board1", "a", false, closed.Revision); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if _, err := svc.Claim("@board1", "a", "bob", "", BlackboardClaimTTL); err != nil {
		t.Fatalf("claim after reopen: %v", err)
	}
}

// TestBlackboard_DependenciesGateReadiness: blocked_by is structural — an entry
// is not ready until every dependency is closed, and closing the dependency
// unblocks it without touching the dependent.
func TestBlackboard_DependenciesGateReadiness(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	if _, err := svc.Post(newBoardEntry("@board1", "design", "design the API")); err != nil {
		t.Fatalf("post design: %v", err)
	}
	impl := newBoardEntry("@board1", "impl", "implement the API")
	impl.BlockedBy = []string{"design"}
	createdImpl, err := svc.Post(impl)
	if err != nil {
		t.Fatalf("post impl: %v", err)
	}

	got, err := svc.Get("@board1", "impl")
	if err != nil {
		t.Fatalf("get impl: %v", err)
	}
	if !got.Blocked || got.Ready {
		t.Fatalf("impl = %+v, want blocked (dependency open) and not ready", got)
	}

	// The derived flags must survive the list path too (the pointer/append trap
	// above silently returned blocked=false for every listed entry).
	listed, err := svc.List(BlackboardListOptions{Board: "@board1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var listedImpl *BlackboardEntry
	for i := range listed {
		if listed[i].Key == "impl" {
			listedImpl = &listed[i]
		}
	}
	if listedImpl == nil {
		t.Fatal("impl missing from the list")
	}
	if !listedImpl.Blocked || listedImpl.Ready {
		t.Fatalf("listed impl = %+v, want blocked=true / ready=false (dependency still open)", listedImpl)
	}
	if len(listedImpl.BlockedBy) != 1 || listedImpl.BlockedBy[0] != "design" {
		t.Fatalf("listed impl blocked_by = %v, want [design]", listedImpl.BlockedBy)
	}

	// Readiness is a GATE, not advice: claiming blocked work must be refused
	// atomically (the precondition lives in the claim UPDATE itself).
	if _, err := svc.Claim("@board1", "impl", "eager-worker", "", BlackboardClaimTTL); err == nil {
		t.Fatal("claiming a blocked entry must fail — parallel agents must not start on sand")
	} else if !strings.Contains(err.Error(), "blocked by design") {
		t.Fatalf("claim-on-blocked error = %v, want it to name the unmet dependency", err)
	}

	design, _ := svc.Get("@board1", "design")
	if _, err := svc.SetClosed("@board1", "design", true, design.Revision); err != nil {
		t.Fatalf("close design: %v", err)
	}
	got, err = svc.Get("@board1", "impl")
	if err != nil {
		t.Fatalf("get impl after close: %v", err)
	}
	if got.Blocked || !got.Ready {
		t.Fatalf("impl = %+v, want unblocked and ready once the dependency is closed", got)
	}
	if got.Revision != createdImpl.Revision {
		t.Errorf("dependency closure mutated the dependent entry (revision %d, want %d)", got.Revision, createdImpl.Revision)
	}

	// A forward reference (dependency not created yet) must block, never panic.
	fwd := newBoardEntry("@board1", "later", "uses a future dep")
	fwd.BlockedBy = []string{"not-created-yet"}
	if _, err := svc.Post(fwd); err != nil {
		t.Fatalf("post forward ref: %v", err)
	}
	got, _ = svc.Get("@board1", "later")
	if !got.Blocked {
		t.Error("a missing dependency must count as unsatisfied")
	}
}

// TestBlackboard_SameIdentityCannotShareLease is the discriminating test for
// token-based leases. Identity strings are NOT unique — two SubAgent instances
// of the same role share one session key (the instance name never reaches the
// tool context) — so an identity-based lease would be extendable by a different
// worker. The token makes that impossible.
func TestBlackboard_SameIdentityCannotShareLease(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	if _, err := svc.Post(newBoardEntry("@board1", "a", "same role, two instances")); err != nil {
		t.Fatalf("post: %v", err)
	}

	first, err := svc.Claim("@board1", "a", "main/explore", "", BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if first.ClaimToken == "" {
		t.Fatal("a successful acquire must mint a claim token (the lease handle)")
	}

	// The second instance carries the SAME identity and no token ⇒ must lose.
	_, err = svc.Claim("@board1", "a", "main/explore", "", BlackboardClaimTTL)
	var conflict *BlackboardConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("second claim with the same identity = %v, want a conflict (identity is not a lease)", err)
	}

	// A stolen/guessed token must not extend the lease either.
	if _, err := svc.Claim("@board1", "a", "main/explore", "not-the-token", BlackboardClaimTTL); !errors.As(err, &conflict) {
		t.Fatalf("extend with a wrong token = %v, want a conflict", err)
	}

	// The real holder extends with its token.
	if _, err := svc.Claim("@board1", "a", "main/explore", first.ClaimToken, BlackboardClaimTTL); err != nil {
		t.Fatalf("extend with the real token: %v", err)
	}
	// ...and a stale token no longer releases after a takeover.
	if _, _, err := svc.Release("@board1", "a", ""); err != nil { // force-release by the operator
		t.Fatalf("force release: %v", err)
	}
	second, err := svc.Claim("@board1", "a", "main/explore", "", BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if _, _, err := svc.Release("@board1", "a", first.ClaimToken); err == nil {
		t.Fatal("releasing with a stale token must conflict, not free the new lease")
	}
	if _, _, err := svc.Release("@board1", "a", second.ClaimToken); err != nil {
		t.Fatalf("releasing with the live token: %v", err)
	}
}

// TestBlackboard_ClaimNamingTheHolder: the lease records WHO holds it (audit /
// UI), while exclusivity stays with the token.
func TestBlackboard_ClaimNamingTheHolder(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	if _, err := svc.Post(newBoardEntry("@board1", "a", "audit")); err != nil {
		t.Fatalf("post: %v", err)
	}
	claimed, err := svc.Claim("@board1", "a", "main/reviewer", "", BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ClaimedBy != "main/reviewer" {
		t.Fatalf("claimed_by = %q, want the holder label", claimed.ClaimedBy)
	}
	// The token never appears in a normal read: only the acquirer receives it.
	got, err := svc.Get("@board1", "a")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClaimToken != "" {
		t.Fatal("a read must never hand out the lease token")
	}
}

// TestBlackboard_ReleaseIsIdempotent: releasing a free entry is a no-op — no
// revision bump, so it cannot wake watchers for nothing.
func TestBlackboard_ReleaseIsIdempotent(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	created, err := svc.Post(newBoardEntry("@board1", "a", "release"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	free, changed, err := svc.Release("@board1", "a", "")
	if err != nil {
		t.Fatalf("release free entry: %v", err)
	}
	if changed {
		t.Error("releasing an unclaimed entry must report changed=false")
	}
	if free.Revision != created.Revision {
		t.Errorf("revision = %d, want %d (a no-op must not bump it)", free.Revision, created.Revision)
	}

	claimed, err := svc.Claim("@board1", "a", "alice", "", BlackboardClaimTTL)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	released, changed, err := svc.Release("@board1", "a", claimed.ClaimToken)
	if err != nil {
		t.Fatalf("release claimed entry: %v", err)
	}
	if !changed {
		t.Error("releasing a claimed entry must report changed=true")
	}
	if released.ClaimedBy != "" || released.Revision != claimed.Revision+1 {
		t.Fatalf("released = %+v, want no holder at revision %d", released, claimed.Revision+1)
	}
	if !released.Ready {
		t.Error("a released entry must be ready")
	}
}

// TestBlackboard_DeleteCASAndMissing: delete honours the CAS guard and reports a
// missing entry explicitly.
func TestBlackboard_DeleteCASAndMissing(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	created, err := svc.Post(newBoardEntry("@board1", "a", "doomed"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}

	if _, err := svc.Delete("@board1", "a", created.Revision+7); err == nil {
		t.Fatal("delete with a stale revision must fail")
	} else if !errors.As(err, new(*BlackboardConflictError)) {
		t.Fatalf("delete conflict error = %v, want BlackboardConflictError", err)
	}

	deleted, err := svc.Delete("@board1", "a", created.Revision)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted.Key != "a" || deleted.Title != "doomed" {
		t.Fatalf("deleted entry = %+v, want the removed row (for reporting/broadcasting)", deleted)
	}
	if _, err := svc.Get("@board1", "a"); !errors.Is(err, ErrBlackboardNotFound) {
		t.Fatalf("get after delete = %v, want ErrBlackboardNotFound", err)
	}
	if _, err := svc.Delete("@board1", "a", 0); !errors.Is(err, ErrBlackboardNotFound) {
		t.Fatalf("delete missing = %v, want ErrBlackboardNotFound", err)
	}
}

// TestBlackboard_ValidationIsExplicit: every rejected input fails loudly with a
// reason — no silent clamping, no truncation.
func TestBlackboard_ValidationIsExplicit(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))

	cases := []struct {
		name string
		run  func() error
	}{
		{"bare name that is neither @name nor a session key", func() error {
			e := newBoardEntry("a/b", "a", "t")
			_, err := svc.Post(e)
			return err
		}},
		{"@name with illegal characters", func() error {
			e := newBoardEntry("@bad name", "a", "t")
			_, err := svc.Post(e)
			return err
		}},
		{"bad key charset", func() error {
			e := newBoardEntry("@board1", "a b", "t")
			_, err := svc.Post(e)
			return err
		}},
		{"empty title", func() error {
			e := newBoardEntry("@board1", "a", "   ")
			_, err := svc.Post(e)
			return err
		}},
		{"oversized body", func() error {
			e := newBoardEntry("@board1", "a", "t")
			e.Body = strings.Repeat("x", BlackboardMaxBodyBytes+1)
			_, err := svc.Post(e)
			return err
		}},
		{"ttl below minimum", func() error {
			if _, err := svc.Post(newBoardEntry("@board1", "a", "t")); err != nil {
				return err
			}
			_, err := svc.Claim("@board1", "a", "alice", "", time.Second)
			return err
		}},
		{"claim without holder", func() error {
			_, err := svc.Claim("@board1", "a", " ", "", BlackboardClaimTTL)
			return err
		}},
		{"list limit above maximum", func() error {
			_, err := svc.List(BlackboardListOptions{Board: "@board1", Limit: BlackboardMaxListLimit + 1})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil {
				t.Fatal("expected an explicit error, got nil")
			}
			if !errors.Is(err, ErrBlackboardInvalid) && !errors.Is(err, ErrBlackboardConflict) {
				t.Fatalf("error = %v, want ErrBlackboardInvalid or ErrBlackboardConflict", err)
			}
		})
	}
}

// TestBlackboard_QuotaIsExplicit: the board cap fails loudly rather than
// evicting a teammate's entry.
func TestBlackboard_QuotaIsExplicit(t *testing.T) {
	db := openTestDB(t)
	svc := NewBlackboardService(db)
	now := time.Now().UnixMilli()
	writeRows := func(from, to int) {
		t.Helper()
		db.writeMu.Lock()
		defer db.writeMu.Unlock()
		tx, err := db.Conn().Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback()
		for i := from; i < to; i++ {
			if _, err := tx.Exec(`
				INSERT INTO blackboard_entries (board, key, title, revision, created_at, updated_at)
				VALUES ('@board1', ?, 't', 1, ?, ?)`, "k"+strconv.Itoa(i), now, now); err != nil {
				t.Fatalf("seed row %d: %v", i, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	writeRows(0, BlackboardMaxEntriesPerBoard)

	if _, err := svc.Post(newBoardEntry("@board1", "one-too-many", "overflow")); err == nil {
		t.Fatal("posting past the board quota must fail explicitly")
	} else if !errors.Is(err, ErrBlackboardInvalid) || !strings.Contains(err.Error(), "max") {
		t.Fatalf("quota error = %v, want an explicit max-entries explanation", err)
	}
}

// TestBlackboard_BoardsSummary: the dashboard counts are derived (live claims,
// blocked only count) — not stored.
func TestBlackboard_BoardsSummary(t *testing.T) {
	db := openTestDB(t)
	svc := NewBlackboardService(db)
	mustPost := func(e *BlackboardEntry) *BlackboardEntry {
		t.Helper()
		got, err := svc.Post(e)
		if err != nil {
			t.Fatalf("post %s: %v", e.Key, err)
		}
		return got
	}
	mustPost(newBoardEntry("@board1", "open", "free"))
	mustPost(newBoardEntry("@board1", "taken", "claimed"))
	dep := newBoardEntry("@board1", "dep", "dependency")
	blocked := newBoardEntry("@board1", "blocked", "waiting")
	blocked.BlockedBy = []string{"dep"}
	mustPost(dep)
	mustPost(blocked)
	done := mustPost(newBoardEntry("@board1", "done", "finished"))
	mustPost(newBoardEntry("@board2", "other", "another board"))

	if _, err := svc.Claim("@board1", "taken", "alice", "", BlackboardClaimTTL); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := svc.SetClosed("@board1", "done", true, done.Revision); err != nil {
		t.Fatalf("close: %v", err)
	}

	boards, err := svc.Boards()
	if err != nil {
		t.Fatalf("boards: %v", err)
	}
	byName := map[string]BlackboardBoard{}
	for _, b := range boards {
		byName[b.Board] = b
	}
	b1, ok := byName["@board1"]
	if !ok {
		t.Fatalf("board1 missing from %+v", boards)
	}
	if b1.Total != 5 || b1.Open != 4 || b1.Claimed != 1 || b1.Blocked != 1 || b1.Closed != 1 {
		t.Fatalf("board1 summary = %+v, want total 5 / open 4 / claimed 1 / blocked 1 / closed 1", b1)
	}
	if b2 := byName["@board2"]; b2.Total != 1 || b2.Open != 1 {
		t.Fatalf("board2 summary = %+v, want total 1 / open 1", b2)
	}
}

// TestBlackboard_BoardIsolation: identical keys in different boards never
// collide, and a cross-board write never touches the other board's entry.
func TestBlackboard_BoardIsolation(t *testing.T) {
	svc := NewBlackboardService(openTestDB(t))
	for _, board := range []string{"@alpha", "@beta"} {
		if _, err := svc.Post(newBoardEntry(board, "same-key", board)); err != nil {
			t.Fatalf("post %s: %v", board, err)
		}
	}
	a, _ := svc.Get("@alpha", "same-key")
	if _, err := svc.Claim("@beta", "same-key", "bob", "", BlackboardClaimTTL); err != nil {
		t.Fatalf("claim in beta: %v", err)
	}
	a2, _ := svc.Get("@alpha", "same-key")
	if a2.ClaimedBy != "" || a2.Revision != a.Revision {
		t.Fatalf("alpha entry changed by a beta claim: %+v -> %+v", a, a2)
	}
}

// TestMigrateV70ToV71CreatesBlackboard is the migration guard: an existing v70
// database gains the table (idempotently) and ends at the current version.
func TestMigrateV70ToV71CreatesBlackboard(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "v70.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	// Minimal v70-shape database: `tenants` marks it as an existing install
	// (Open only runs createSchema when it is absent), so Open must migrate it.
	if _, err := raw.Exec(`
		CREATE TABLE tenants (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			UNIQUE(channel, chat_id)
		);
		CREATE TABLE schema_version (version INTEGER PRIMARY KEY);
		INSERT INTO schema_version(version) VALUES (70);
	`); err != nil {
		t.Fatalf("build v70 fixture: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}

	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open v70 db: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.Conn().QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("version = %d, want %d", version, schemaVersion)
	}
	if ok, err := tableExists(db.Conn(), "blackboard_entries"); err != nil || !ok {
		t.Fatalf("blackboard_entries exists=%v err=%v, want true", ok, err)
	}

	// Idempotent: running the migration again is a no-op, not an error.
	if err := migrateV70ToV71(db); err != nil {
		t.Fatalf("re-run migrateV70ToV71: %v", err)
	}
}
