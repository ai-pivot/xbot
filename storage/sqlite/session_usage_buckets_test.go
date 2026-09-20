package sqlite

import (
	"testing"
	"time"
)

// seedIterationAt inserts one iteration_history row with an EXPLICIT created_at
// (AppendIterationHistory relies on the DEFAULT CURRENT_TIMESTAMP, which the
// bucket tests cannot control).
func seedIterationAt(t *testing.T, db *DB, tenantID int64, createdAt time.Time, in, cached, out int64) {
	t.Helper()
	if _, err := db.Conn().Exec(
		`INSERT INTO iteration_history (message_id, tenant_id, turn_id, iteration, tokens, input_tokens, cached_tokens, created_at)
		 VALUES (0, ?, 1, 1, ?, ?, ?, ?)`,
		tenantID, out, in, cached, createdAt.UTC().Format(time.RFC3339),
	); err != nil {
		t.Fatalf("seed iteration_history: %v", err)
	}
}

// seedIterationRawAt inserts with a caller-supplied raw created_at string (used
// to prove offset-carrying RFC3339 timestamps are read as the same instant).
func seedIterationRawAt(t *testing.T, db *DB, tenantID int64, createdAt string, in, cached, out int64) {
	t.Helper()
	if _, err := db.Conn().Exec(
		`INSERT INTO iteration_history (message_id, tenant_id, turn_id, iteration, tokens, input_tokens, cached_tokens, created_at)
		 VALUES (0, ?, 1, 1, ?, ?, ?, ?)`,
		tenantID, out, in, cached, createdAt,
	); err != nil {
		t.Fatalf("seed iteration_history (raw): %v", err)
	}
}

func newUsageTestDB(t *testing.T, chatID string) (*DB, *SessionService, int64) {
	t.Helper()
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	tenantSvc := NewTenantService(db)
	sessionSvc := NewSessionService(db)
	tenantID, err := tenantSvc.GetOrCreateTenantID("test", chatID)
	if err != nil {
		t.Fatalf("Failed to create tenant: %v", err)
	}
	return db, sessionSvc, tenantID
}

// TestGetTenantUsageBuckets_SumsMatchTotals is the core equivalence: bucketing
// is a re-projection of the same rows, so Σ(bucket) MUST equal the flat totals
// minus whatever the requested window excludes — no row may be dropped or
// double counted by the SQL bucketing maths.
func TestGetTenantUsageBuckets_SumsMatchTotals(t *testing.T) {
	db, svc, tenantID := newUsageTestDB(t, "chat-bucket-sums")
	now := time.Now().UTC()

	// Three rows inside any reasonable window, one 3h back, one 25h back.
	seedIterationAt(t, db, tenantID, now.Add(-10*time.Minute), 5000, 3000, 100)
	seedIterationAt(t, db, tenantID, now.Add(-30*time.Minute), 6100, 4000, 50)
	seedIterationAt(t, db, tenantID, now.Add(-50*time.Minute), 1000, 0, 30)
	seedIterationAt(t, db, tenantID, now.Add(-3*time.Hour), 800, 400, 20)
	seedIterationAt(t, db, tenantID, now.Add(-25*time.Hour), 999, 111, 7)

	stats, err := svc.GetTenantUsageStats(tenantID, 0)
	if err != nil {
		t.Fatalf("GetTenantUsageStats: %v", err)
	}
	if stats.IterationCount != 5 {
		t.Fatalf("seed sanity: iteration_count = %d, want 5", stats.IterationCount)
	}
	// The 25h-old row (`outside`): inside the day window, outside the
	// minute/hour windows below (their windows are chosen to exclude it).
	const outIn, outCached, outOut, outCalls = 999, 111, 7, 1

	for _, tc := range []struct {
		name              string
		bucketSeconds     int64
		count             int
		expectWindowedOut bool // true ⇒ the 25h-old row falls outside this window
	}{
		// 1000 minutes = 16.6h ⇒ excludes the 25h-old row, includes the 3h one.
		{"minute", UsageBucketMinuteSeconds, 1000, true},
		// 24 hours ⇒ excludes the 25h-old row.
		{"hour", UsageBucketHourSeconds, 24, true},
		// 30 days ⇒ everything is inside.
		{"day", UsageBucketDaySeconds, 30, false},
	} {
		buckets, err := svc.GetTenantUsageBuckets(tenantID, tc.bucketSeconds, tc.count, 480)
		if err != nil {
			t.Fatalf("%s: GetTenantUsageBuckets: %v", tc.name, err)
		}
		if len(buckets) == 0 {
			t.Fatalf("%s: expected non-empty buckets", tc.name)
		}
		var in, cached, out, calls int64
		for i, b := range buckets {
			if (b.BucketStart+480*60)%tc.bucketSeconds != 0 {
				t.Errorf("%s: bucket_start %d is not aligned to the +08:00 wall clock (width %d)", tc.name, b.BucketStart, tc.bucketSeconds)
			}
			if i > 0 && b.BucketStart <= buckets[i-1].BucketStart {
				t.Errorf("%s: buckets must ascend, got %d after %d", tc.name, b.BucketStart, buckets[i-1].BucketStart)
			}
			in += b.InputTokens
			cached += b.CachedTokens
			out += b.OutputTokens
			calls += b.Calls
		}
		wantIn, wantCached, wantOut, wantCalls := stats.InputTokens, stats.CachedTokens, stats.OutputTokens, stats.IterationCount
		if tc.expectWindowedOut {
			wantIn -= outIn
			wantCached -= outCached
			wantOut -= outOut
			wantCalls -= outCalls
		}
		if in != wantIn {
			t.Errorf("%s: Σ input_tokens = %d, want %d (flat total %d)", tc.name, in, wantIn, stats.InputTokens)
		}
		if cached != wantCached {
			t.Errorf("%s: Σ cached_tokens = %d, want %d", tc.name, cached, wantCached)
		}
		if out != wantOut {
			t.Errorf("%s: Σ output_tokens = %d, want %d", tc.name, out, wantOut)
		}
		if calls != wantCalls {
			t.Errorf("%s: Σ calls = %d, want %d", tc.name, calls, wantCalls)
		}
	}

	// Distinct minutes ⇒ distinct buckets: 4 rows inside a 1000-minute window
	// (three within the hour + the 3h-old one), 25h-old excluded.
	minuteBuckets, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketMinuteSeconds, 1000, 480)
	if err != nil {
		t.Fatalf("minute: GetTenantUsageBuckets: %v", err)
	}
	if len(minuteBuckets) != 4 {
		t.Errorf("minute: expected 4 non-empty buckets, got %d (%+v)", len(minuteBuckets), minuteBuckets)
	}

	// A window wide enough to include EVERY row must reproduce the flat totals
	// exactly (48 hour buckets reach past the 25h-old row).
	wide, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 48, 480)
	if err != nil {
		t.Fatalf("48h: GetTenantUsageBuckets: %v", err)
	}
	var wideIn, wideCalls int64
	for _, b := range wide {
		wideIn += b.InputTokens
		wideCalls += b.Calls
	}
	if wideIn != stats.InputTokens || wideCalls != stats.IterationCount {
		t.Errorf("wide window must reproduce the flat totals: got in=%d calls=%d, want in=%d calls=%d", wideIn, wideCalls, stats.InputTokens, stats.IterationCount)
	}
}

// TestGetTenantUsageBuckets_EmptyTenant verifies a session with no history
// returns an EMPTY (non-nil) slice and no error — the panel renders a zero-state
// instead of an error.
func TestGetTenantUsageBuckets_EmptyTenant(t *testing.T) {
	_, svc, tenantID := newUsageTestDB(t, "chat-bucket-empty")

	buckets, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 24, 0)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets on empty tenant: %v", err)
	}
	if buckets == nil {
		t.Fatal("expected a non-nil empty slice, got nil")
	}
	if len(buckets) != 0 {
		t.Errorf("expected no buckets, got %d (%+v)", len(buckets), buckets)
	}

	// Cross-tenant isolation: another session's rows must not leak in.
	db, _, _ := newUsageTestDB(t, "chat-bucket-empty-2")
	seedIterationAt(t, db, tenantID+1000, time.Now(), 1, 2, 3) // different tenant id space
	other, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 24, 0)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets (other): %v", err)
	}
	if len(other) != 0 {
		t.Errorf("usage leaked across tenants: %+v", other)
	}
}

// TestGetTenantUsageBuckets_TimezoneAlignsBuckets pins the bucketing maths to a
// deterministic instant: an event at 16:30Z belongs to the UTC day bucket of
// that date, but to the NEXT local day's bucket at +08:00 — and the +08:00
// bucket must start at 16:00Z (local midnight), not at 00:00Z.
func TestGetTenantUsageBuckets_TimezoneAlignsBuckets(t *testing.T) {
	db, svc, tenantID := newUsageTestDB(t, "chat-bucket-tz")
	now := time.Now().UTC()
	dayFloor := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -2)
	at := dayFloor.Add(16*time.Hour + 30*time.Minute) // 16:30Z = 00:30 (+08:00) next local day

	seedIterationAt(t, db, tenantID, at, 100, 10, 5)
	// Same instant written as offset-carrying RFC3339 (local wall clock +08:00).
	seedIterationRawAt(t, db, tenantID, at.In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339), 100, 10, 5)

	utcBuckets, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketDaySeconds, 10, 0)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets(tz=0): %v", err)
	}
	if len(utcBuckets) != 1 {
		t.Fatalf("tz=0: expected 1 bucket, got %d (%+v)", len(utcBuckets), utcBuckets)
	}
	if utcBuckets[0].BucketStart != dayFloor.Unix() {
		t.Errorf("tz=0: bucket_start = %d, want %d (UTC midnight)", utcBuckets[0].BucketStart, dayFloor.Unix())
	}
	// Both storage shapes are the same instant ⇒ same bucket, same call count.
	if utcBuckets[0].Calls != 2 {
		t.Errorf("offset-carrying RFC3339 row not bucketed with its UTC form: calls = %d, want 2", utcBuckets[0].Calls)
	}

	localBuckets, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketDaySeconds, 10, 480)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets(tz=+08:00): %v", err)
	}
	if len(localBuckets) != 1 {
		t.Fatalf("tz=+08:00: expected 1 bucket, got %d (%+v)", len(localBuckets), localBuckets)
	}
	wantLocal := dayFloor.Add(16 * time.Hour).Unix() // local midnight in UTC+8
	if localBuckets[0].BucketStart != wantLocal {
		t.Errorf("tz=+08:00: bucket_start = %d, want %d (local midnight = 16:00Z)", localBuckets[0].BucketStart, wantLocal)
	}
	// The offset must actually MOVE the boundary (a tz-ignoring implementation
	// would return the UTC midnight here and fail this).
	if (utcBuckets[0].BucketStart-localBuckets[0].BucketStart)%UsageBucketDaySeconds == 0 {
		t.Errorf("timezone offset did not shift the day boundary: tz=0 start %d, tz=+08:00 start %d", utcBuckets[0].BucketStart, localBuckets[0].BucketStart)
	}

	// Hour granularity: a whole-hour offset keeps the same wall-clock hour
	// boundary as UTC for this instant, and alignment must hold at both offsets.
	hourUTC, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 72, 0)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets(hour, tz=0): %v", err)
	}
	hourLocal, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 72, 480)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets(hour, tz=+08:00): %v", err)
	}
	if len(hourUTC) != 1 || len(hourLocal) != 1 {
		t.Fatalf("hour buckets: got %d/%d, want 1/1", len(hourUTC), len(hourLocal))
	}
	if hourUTC[0].BucketStart%UsageBucketHourSeconds != 0 {
		t.Errorf("tz=0 hour bucket misaligned: %d", hourUTC[0].BucketStart)
	}
	if (hourLocal[0].BucketStart+480*60)%UsageBucketHourSeconds != 0 {
		t.Errorf("tz=+08:00 hour bucket misaligned to the local wall clock: %d", hourLocal[0].BucketStart)
	}
	if hourUTC[0].BucketStart != hourLocal[0].BucketStart {
		t.Errorf("whole-hour offset must not move an hour boundary: %d vs %d", hourUTC[0].BucketStart, hourLocal[0].BucketStart)
	}
}

// TestGetTenantUsageBuckets_WindowExcludesOlderBuckets verifies the window is
// bounded by the requested count (the SQL HAVING floor) — a long history must
// not push an unbounded series to the client.
func TestGetTenantUsageBuckets_WindowExcludesOlderBuckets(t *testing.T) {
	db, svc, tenantID := newUsageTestDB(t, "chat-bucket-window")
	now := time.Now().UTC()

	seedIterationAt(t, db, tenantID, now.Add(-40*time.Minute), 100, 0, 10) // outside a 10-minute window
	seedIterationAt(t, db, tenantID, now.Add(-2*time.Minute), 200, 50, 20) // inside

	buckets, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketMinuteSeconds, 10, 0)
	if err != nil {
		t.Fatalf("GetTenantUsageBuckets: %v", err)
	}
	if len(buckets) != 1 {
		t.Fatalf("expected only the in-window bucket, got %d (%+v)", len(buckets), buckets)
	}
	if buckets[0].InputTokens != 200 || buckets[0].Calls != 1 {
		t.Errorf("unexpected bucket content: %+v", buckets[0])
	}
}

// TestGetTenantUsageBuckets_Validation covers the defensive contract: unknown
// widths are rejected (never silently mis-bucketed) and the count is clamped.
func TestGetTenantUsageBuckets_Validation(t *testing.T) {
	_, svc, tenantID := newUsageTestDB(t, "chat-bucket-validate")

	if _, err := svc.GetTenantUsageBuckets(tenantID, 7, 10, 0); err == nil {
		t.Error("expected an error for an unsupported bucket width (7s)")
	}
	if _, err := svc.GetTenantUsageBuckets(tenantID, 0, 10, 0); err == nil {
		t.Error("expected an error for bucket width 0")
	}
	// count <= 0 must clamp (not error, not return everything).
	if _, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 0, 0); err != nil {
		t.Errorf("count=0 must clamp, got error: %v", err)
	}
	if _, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 99999, 0); err != nil {
		t.Errorf("huge count must clamp, got error: %v", err)
	}
	if _, err := svc.GetTenantUsageBuckets(tenantID, UsageBucketHourSeconds, 10, 99999); err != nil {
		t.Errorf("hostile tz offset must clamp, got error: %v", err)
	}
}

// TestUsageBucketSecondsForGranularity pins the granularity → width mapping and
// rejects unknown names (single source of truth for the RPC layer).
func TestUsageBucketSecondsForGranularity(t *testing.T) {
	for name, want := range map[string]int64{
		"minute": UsageBucketMinuteSeconds,
		"hour":   UsageBucketHourSeconds,
		"day":    UsageBucketDaySeconds,
	} {
		got, ok := UsageBucketSecondsForGranularity(name)
		if !ok || got != want {
			t.Errorf("UsageBucketSecondsForGranularity(%q) = (%d, %v), want (%d, true)", name, got, ok, want)
		}
	}
	if got, ok := UsageBucketSecondsForGranularity("week"); ok {
		t.Errorf("unknown granularity must be rejected, got (%d, true)", got)
	}
	if _, ok := UsageBucketSecondsForGranularity(""); ok {
		t.Error("empty granularity must be rejected")
	}
}
