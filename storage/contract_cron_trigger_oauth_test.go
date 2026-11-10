package storage

import (
	"testing"
	"time"

	"xbot/event"
	"xbot/storage/sqlite"
)

// testCronStoreContract exercises CronStore on every backend.
//
// These methods were previously reachable only through *sqlite.CronService, which
// is exactly why cron silently disappeared on PG (the wiring was guarded by a nil
// main-DB handle). The contract pins the semantics the scheduler relies on:
// ordering by next_run, nil-on-missing, and the one-shot/last_trigger bookkeeping.
func testCronStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()
	cron := s.Cron()

	// uniqueJobID keeps subtests isolated without a truncate (the store has no
	// tenant scoping — cron_jobs is global by design, single operator).
	uniqueJobID := func(t *testing.T, suffix string) string {
		return "job_test_" + suffix + "_" + time.Now().Format("150405.000000000")
	}

	t.Run("AddGetRemove", func(t *testing.T) {
		id := uniqueJobID(t, "add")
		now := time.Now().UTC().Truncate(time.Second)
		job := &sqlite.CronJob{
			ID: id, Message: "ping", Channel: "web", ChatID: "chat_cron_1",
			SenderID: "web-1", EverySeconds: 60,
			CreatedAt: now, NextRun: now.Add(time.Minute),
		}
		if err := cron.AddJob(job); err != nil {
			t.Fatalf("AddJob: %v", err)
		}
		got, err := cron.GetJob(id)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if got == nil {
			t.Fatal("GetJob returned nil for a job that was just added")
		}
		if got.Message != "ping" || got.Channel != "web" || got.EverySeconds != 60 {
			t.Errorf("round-trip mismatch: %+v", got)
		}
		if got.OneShot {
			t.Error("one_shot must default to false")
		}

		if err := cron.RemoveJob(id); err != nil {
			t.Fatalf("RemoveJob: %v", err)
		}
		gone, err := cron.GetJob(id)
		if err != nil {
			t.Fatalf("GetJob after remove: %v", err)
		}
		if gone != nil {
			t.Errorf("GetJob returned a removed job: %+v", gone)
		}
	})

	t.Run("GetMissingReturnsNil", func(t *testing.T) {
		// Not-found is a normal business result on both backends (never an error) —
		// the Cron tool relies on this to report "job not found".
		got, err := cron.GetJob(uniqueJobID(t, "missing"))
		if err != nil {
			t.Fatalf("GetJob(missing) returned an error: %v", err)
		}
		if got != nil {
			t.Errorf("GetJob(missing) = %+v, want nil", got)
		}
	})

	t.Run("ListAllOrdersByNextRun", func(t *testing.T) {
		base := time.Now().UTC().Truncate(time.Second)
		later := uniqueJobID(t, "later")
		sooner := uniqueJobID(t, "sooner")
		for _, j := range []struct {
			id   string
			next time.Time
		}{{later, base.Add(2 * time.Hour)}, {sooner, base.Add(time.Minute)}} {
			if err := cron.AddJob(&sqlite.CronJob{
				ID: j.id, Message: "m", Channel: "web", ChatID: "chat_cron_order",
				CreatedAt: base, NextRun: j.next,
			}); err != nil {
				t.Fatalf("AddJob(%s): %v", j.id, err)
			}
			t.Cleanup(func(id string) func() { return func() { _ = cron.RemoveJob(id) } }(j.id))
		}

		jobs, err := cron.ListAllJobs()
		if err != nil {
			t.Fatalf("ListAllJobs: %v", err)
		}
		// The scheduler iterates in next_run order; assert our two jobs appear in
		// that relative order rather than asserting absolute positions (other jobs
		// may exist in a shared database).
		pos := map[string]int{}
		for i, j := range jobs {
			pos[j.ID] = i
		}
		if pos[sooner] > pos[later] {
			t.Errorf("ListAllJobs is not ordered by next_run: sooner=%d later=%d", pos[sooner], pos[later])
		}
	})

	t.Run("ListByChannelChatIDIsScoped", func(t *testing.T) {
		base := time.Now().UTC().Truncate(time.Second)
		mine := uniqueJobID(t, "mine")
		other := uniqueJobID(t, "other")
		for _, j := range []struct{ id, chat string }{{mine, "chat_scope_a"}, {other, "chat_scope_b"}} {
			if err := cron.AddJob(&sqlite.CronJob{
				ID: j.id, Message: "m", Channel: "web", ChatID: j.chat,
				CreatedAt: base, NextRun: base,
			}); err != nil {
				t.Fatalf("AddJob(%s): %v", j.id, err)
			}
			t.Cleanup(func(id string) func() { return func() { _ = cron.RemoveJob(id) } }(j.id))
		}

		jobs, err := cron.ListJobsByChannelChatID("web", "chat_scope_a")
		if err != nil {
			t.Fatalf("ListJobsByChannelChatID: %v", err)
		}
		for _, j := range jobs {
			if j.ChatID != "chat_scope_a" {
				t.Errorf("scoped query returned a job for %q", j.ChatID)
			}
			if j.ID == other {
				t.Error("scoped query leaked another session's job")
			}
		}
		found := false
		for _, j := range jobs {
			if j.ID == mine {
				found = true
			}
		}
		if !found {
			t.Error("scoped query did not return the job belonging to that session")
		}
	})

	t.Run("UpdateNextRunAndLastTrigger", func(t *testing.T) {
		id := uniqueJobID(t, "update")
		base := time.Now().UTC().Truncate(time.Second)
		if err := cron.AddJob(&sqlite.CronJob{
			ID: id, Message: "m", Channel: "web", ChatID: "chat_cron_upd",
			CreatedAt: base, NextRun: base,
		}); err != nil {
			t.Fatalf("AddJob: %v", err)
		}
		t.Cleanup(func() { _ = cron.RemoveJob(id) })

		next := base.Add(90 * time.Minute)
		if err := cron.UpdateNextRun(id, next); err != nil {
			t.Fatalf("UpdateNextRun: %v", err)
		}
		fired := base.Add(30 * time.Minute)
		if err := cron.UpdateLastTrigger(id, fired); err != nil {
			t.Fatalf("UpdateLastTrigger: %v", err)
		}

		got, err := cron.GetJob(id)
		if err != nil || got == nil {
			t.Fatalf("GetJob: %v (job=%v)", err, got)
		}
		if !got.NextRun.Equal(next) {
			t.Errorf("NextRun = %v, want %v", got.NextRun, next)
		}
		if got.LastTrigger == nil {
			t.Fatal("LastTrigger is nil after UpdateLastTrigger")
		}
		if !got.LastTrigger.Equal(fired) {
			t.Errorf("LastTrigger = %v, want %v", *got.LastTrigger, fired)
		}
	})

	t.Run("ListJobsIncludesAdded", func(t *testing.T) {
		id := uniqueJobID(t, "list")
		base := time.Now().UTC().Truncate(time.Second)
		if err := cron.AddJob(&sqlite.CronJob{
			ID: id, Message: "m", Channel: "web", ChatID: "chat_cron_list",
			CreatedAt: base, NextRun: base,
		}); err != nil {
			t.Fatalf("AddJob: %v", err)
		}
		t.Cleanup(func() { _ = cron.RemoveJob(id) })

		jobs, err := cron.ListJobs()
		if err != nil {
			t.Fatalf("ListJobs: %v", err)
		}
		found := false
		for _, j := range jobs {
			if j.ID == id {
				found = true
			}
		}
		if !found {
			t.Error("ListJobs did not include a job that was just added")
		}
	})

	t.Run("MigrateFromJSONIsIdempotent", func(t *testing.T) {
		// The legacy import must be safe on every start: once jobs exist (or no
		// cron.json is present) it is a no-op, never an error.
		before, err := cron.ListAllJobs()
		if err != nil {
			t.Fatalf("ListAllJobs: %v", err)
		}
		if err := cron.MigrateFromJSON(t.TempDir()); err != nil {
			t.Fatalf("MigrateFromJSON on an empty dir must be a no-op, got: %v", err)
		}
		after, err := cron.ListAllJobs()
		if err != nil {
			t.Fatalf("ListAllJobs: %v", err)
		}
		if len(after) != len(before) {
			t.Errorf("MigrateFromJSON changed the job count: %d → %d", len(before), len(after))
		}
	})
}

// testTriggerStoreContract exercises the webhook trigger store on every backend.
// Same rationale as cron: the event router used to hold a SQLite service, so
// webhooks would have been dead on PG.
func testTriggerStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()
	store := s.Trigger()

	uniqueID := func(t *testing.T, suffix string) string {
		return "trg_test_" + suffix + "_" + time.Now().Format("150405.000000000")
	}

	t.Run("AddGetRemove", func(t *testing.T) {
		id := uniqueID(t, "add")
		now := time.Now().UTC().Truncate(time.Second)
		trg := &event.Trigger{
			ID: id, Name: "deploy hook", EventType: "webhook",
			Channel: "web", ChatID: "chat_trg_1", SenderID: "web-1",
			MessageTpl: "deployed {{.payload.tag}}", Secret: "s3cret",
			Enabled: true, CreatedAt: now,
		}
		if err := store.AddTrigger(trg); err != nil {
			t.Fatalf("AddTrigger: %v", err)
		}
		got, err := store.GetTrigger(id)
		if err != nil {
			t.Fatalf("GetTrigger: %v", err)
		}
		if got == nil {
			t.Fatal("GetTrigger returned nil for a trigger that was just added")
		}
		if got.Name != "deploy hook" || got.EventType != "webhook" || !got.Enabled {
			t.Errorf("round-trip mismatch: %+v", got)
		}
		if got.OneShot {
			t.Error("one_shot must default to false")
		}

		if err := store.RemoveTrigger(id); err != nil {
			t.Fatalf("RemoveTrigger: %v", err)
		}
		gone, err := store.GetTrigger(id)
		if err != nil {
			t.Fatalf("GetTrigger after remove: %v", err)
		}
		if gone != nil {
			t.Errorf("GetTrigger returned a removed trigger: %+v", gone)
		}
	})

	t.Run("GetMissingReturnsNil", func(t *testing.T) {
		got, err := store.GetTrigger(uniqueID(t, "missing"))
		if err != nil {
			t.Fatalf("GetTrigger(missing) returned an error: %v", err)
		}
		if got != nil {
			t.Errorf("GetTrigger(missing) = %+v, want nil", got)
		}
	})

	t.Run("ListByEventTypeOnlyEnabled", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		enabled := uniqueID(t, "enabled")
		disabled := uniqueID(t, "disabled")
		for _, c := range []struct {
			id      string
			enabled bool
		}{{enabled, true}, {disabled, false}} {
			if err := store.AddTrigger(&event.Trigger{
				ID: c.id, EventType: "webhook", Channel: "web", ChatID: "chat_trg_evt",
				MessageTpl: "m", Enabled: c.enabled, CreatedAt: now,
			}); err != nil {
				t.Fatalf("AddTrigger(%s): %v", c.id, err)
			}
			t.Cleanup(func(id string) func() { return func() { _ = store.RemoveTrigger(id) } }(c.id))
		}

		triggers, err := store.ListByEventType("webhook")
		if err != nil {
			t.Fatalf("ListByEventType: %v", err)
		}
		for _, trg := range triggers {
			if !trg.Enabled {
				t.Errorf("ListByEventType returned a disabled trigger %q", trg.ID)
			}
		}
		found := false
		for _, trg := range triggers {
			if trg.ID == enabled {
				found = true
			}
		}
		if !found {
			t.Error("ListByEventType did not return the enabled trigger")
		}
	})

	t.Run("ListBySenderIsScoped", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		mine := uniqueID(t, "mine")
		other := uniqueID(t, "other")
		for _, c := range []struct{ id, sender string }{{mine, "sender_a"}, {other, "sender_b"}} {
			if err := store.AddTrigger(&event.Trigger{
				ID: c.id, EventType: "webhook", Channel: "web", ChatID: "chat_trg_snd",
				SenderID: c.sender, MessageTpl: "m", Enabled: true, CreatedAt: now,
			}); err != nil {
				t.Fatalf("AddTrigger(%s): %v", c.id, err)
			}
			t.Cleanup(func(id string) func() { return func() { _ = store.RemoveTrigger(id) } }(c.id))
		}

		triggers, err := store.ListBySender("sender_a")
		if err != nil {
			t.Fatalf("ListBySender: %v", err)
		}
		for _, trg := range triggers {
			if trg.SenderID != "sender_a" {
				t.Errorf("ListBySender leaked a trigger owned by %q", trg.SenderID)
			}
		}
	})

	t.Run("UpdateEnabledToggles", func(t *testing.T) {
		id := uniqueID(t, "toggle")
		now := time.Now().UTC().Truncate(time.Second)
		if err := store.AddTrigger(&event.Trigger{
			ID: id, EventType: "webhook", Channel: "web", ChatID: "chat_trg_toggle",
			MessageTpl: "m", Enabled: true, CreatedAt: now,
		}); err != nil {
			t.Fatalf("AddTrigger: %v", err)
		}
		t.Cleanup(func() { _ = store.RemoveTrigger(id) })

		if err := store.UpdateEnabled(id, false); err != nil {
			t.Fatalf("UpdateEnabled(false): %v", err)
		}
		got, _ := store.GetTrigger(id)
		if got == nil || got.Enabled {
			t.Errorf("enabled did not become false: %+v", got)
		}
		if err := store.UpdateEnabled(id, true); err != nil {
			t.Fatalf("UpdateEnabled(true): %v", err)
		}
		got, _ = store.GetTrigger(id)
		if got == nil || !got.Enabled {
			t.Errorf("enabled did not become true: %+v", got)
		}
	})

	// RecordFire only records. Disabling a one-shot trigger belongs to the ROUTER
	// (event.Router.dispatchOne calls UpdateEnabled after RecordFire) — putting it
	// in the store would make the two backends diverge, since only one of them
	// would do it. This test pins the split so it cannot silently move back.
	t.Run("RecordFireRecordsWithoutTouchingEnabled", func(t *testing.T) {
		oneShot := uniqueID(t, "oneshot")
		now := time.Now().UTC().Truncate(time.Second)
		if err := store.AddTrigger(&event.Trigger{
			ID: oneShot, EventType: "webhook", Channel: "web", ChatID: "chat_trg_fire",
			MessageTpl: "m", Enabled: true, OneShot: true, CreatedAt: now,
		}); err != nil {
			t.Fatalf("AddTrigger: %v", err)
		}
		t.Cleanup(func() { _ = store.RemoveTrigger(oneShot) })

		firedAt := now.Add(time.Minute)
		if err := store.RecordFire(oneShot, firedAt); err != nil {
			t.Fatalf("RecordFire: %v", err)
		}
		got, err := store.GetTrigger(oneShot)
		if err != nil || got == nil {
			t.Fatalf("GetTrigger: %v (trigger=%v)", err, got)
		}
		if got.FireCount != 1 {
			t.Errorf("fire_count = %d, want 1", got.FireCount)
		}
		if got.LastFired == nil || !got.LastFired.Equal(firedAt) {
			t.Errorf("last_fired = %v, want %v", got.LastFired, firedAt)
		}
		if !got.Enabled {
			t.Error("RecordFire must NOT disable the trigger — the router owns that decision")
		}
	})
}

// testOAuthTokenStoreContract exercises the OAuth credential store on every
// backend. OAuth previously required a SQLite file and was silently disabled on
// PG; this contract keeps both backends honest about the credential semantics.
func testOAuthTokenStoreContract(t *testing.T, factory StorageFactory) {
	s := factory(t)
	defer s.Close()
	store := s.OAuth()

	// Unique keys per subtest: oauth_tokens is keyed by (provider, channel, chat_id).
	key := func(t *testing.T, suffix string) (string, string, string) {
		stamp := time.Now().Format("150405.000000000")
		return "prov_" + suffix, "chan_" + suffix, "chat_" + suffix + "_" + stamp
	}

	t.Run("SetGetRoundTrip", func(t *testing.T) {
		prov, ch, chat := key(t, "rt")
		expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		if err := store.SetOAuthToken(prov, ch, chat, &OAuthToken{
			AccessToken:  "access-abc",
			RefreshToken: "refresh-xyz",
			ExpiresAt:    expires,
			Scopes:       []string{"contact:read", "calendar:write"},
			Raw:          map[string]any{"tenant": "t1"},
		}); err != nil {
			t.Fatalf("SetOAuthToken: %v", err)
		}
		t.Cleanup(func() { _ = store.DeleteOAuthToken(prov, ch, chat) })

		got, err := store.GetOAuthToken(prov, ch, chat)
		if err != nil {
			t.Fatalf("GetOAuthToken: %v", err)
		}
		if got == nil {
			t.Fatal("GetOAuthToken returned nil for a token that was just stored")
		}
		if got.AccessToken != "access-abc" || got.RefreshToken != "refresh-xyz" {
			t.Errorf("credential round-trip mismatch: %+v", got)
		}
		if !got.ExpiresAt.Equal(expires) {
			t.Errorf("ExpiresAt = %v, want %v", got.ExpiresAt, expires)
		}
		if len(got.Scopes) != 2 || got.Scopes[0] != "contact:read" {
			t.Errorf("Scopes = %v, want the two stored scopes in order", got.Scopes)
		}
		if got.Raw["tenant"] != "t1" {
			t.Errorf("Raw = %v, want tenant=t1", got.Raw)
		}
	})

	t.Run("GetMissingReturnsNil", func(t *testing.T) {
		prov, ch, chat := key(t, "missing")
		got, err := store.GetOAuthToken(prov, ch, chat)
		if err != nil {
			t.Fatalf("GetOAuthToken(missing) returned an error: %v", err)
		}
		if got != nil {
			t.Errorf("GetOAuthToken(missing) = %+v, want nil", got)
		}
	})

	t.Run("SetOverwrites", func(t *testing.T) {
		prov, ch, chat := key(t, "overwrite")
		expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
		for _, access := range []string{"first", "second"} {
			if err := store.SetOAuthToken(prov, ch, chat, &OAuthToken{
				AccessToken: access, ExpiresAt: expires,
			}); err != nil {
				t.Fatalf("SetOAuthToken(%s): %v", access, err)
			}
		}
		t.Cleanup(func() { _ = store.DeleteOAuthToken(prov, ch, chat) })

		got, err := store.GetOAuthToken(prov, ch, chat)
		if err != nil {
			t.Fatalf("GetOAuthToken: %v", err)
		}
		if got == nil || got.AccessToken != "second" {
			t.Errorf("second SetOAuthToken did not replace the first: %+v", got)
		}
	})

	t.Run("DeleteRemoves", func(t *testing.T) {
		prov, ch, chat := key(t, "delete")
		if err := store.SetOAuthToken(prov, ch, chat, &OAuthToken{
			AccessToken: "to-be-deleted", ExpiresAt: time.Now().UTC().Add(time.Hour),
		}); err != nil {
			t.Fatalf("SetOAuthToken: %v", err)
		}
		if err := store.DeleteOAuthToken(prov, ch, chat); err != nil {
			t.Fatalf("DeleteOAuthToken: %v", err)
		}
		got, err := store.GetOAuthToken(prov, ch, chat)
		if err != nil {
			t.Fatalf("GetOAuthToken after delete: %v", err)
		}
		if got != nil {
			t.Errorf("deleted token is still readable: %+v", got)
		}
	})

	t.Run("CleanupExpiredRemovesOnlyExpired", func(t *testing.T) {
		provOld, chOld, chatOld := key(t, "expired")
		provNew, chNew, chatNew := key(t, "valid")
		past := time.Now().UTC().Add(-2 * time.Hour)
		future := time.Now().UTC().Add(2 * time.Hour)
		if err := store.SetOAuthToken(provOld, chOld, chatOld, &OAuthToken{AccessToken: "old", ExpiresAt: past}); err != nil {
			t.Fatalf("SetOAuthToken(expired): %v", err)
		}
		if err := store.SetOAuthToken(provNew, chNew, chatNew, &OAuthToken{AccessToken: "new", ExpiresAt: future}); err != nil {
			t.Fatalf("SetOAuthToken(valid): %v", err)
		}
		t.Cleanup(func() { _ = store.DeleteOAuthToken(provNew, chNew, chatNew) })

		if err := store.CleanupExpiredOAuthTokens(time.Now().UTC()); err != nil {
			t.Fatalf("CleanupExpiredOAuthTokens: %v", err)
		}
		gone, _ := store.GetOAuthToken(provOld, chOld, chatOld)
		if gone != nil {
			t.Errorf("expired token survived cleanup: %+v", gone)
		}
		kept, _ := store.GetOAuthToken(provNew, chNew, chatNew)
		if kept == nil {
			t.Error("cleanup removed a token that had not expired")
		}
	})
}
