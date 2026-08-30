package sqlite

import (
	"testing"
)

// seedSystemSubscription creates the shared system row that
// reconcileSystemSubscription writes at boot in production — GetDefault's
// dangling/empty fallback resolves to it.
func seedSystemSubscription(t *testing.T, svc *LLMSubscriptionService) {
	t.Helper()
	if err := svc.UpsertSystemSubscription(&LLMSubscription{
		ID: "system", Name: "system", Provider: "openai",
		BaseURL: "http://system", APIKey: "k", Model: "sys-m", SenderID: "__system__",
	}); err != nil {
		t.Fatalf("seed system subscription: %v", err)
	}
}

// TestRemoveSubscriptionCleansUserDefaultModel (m8) verifies Remove deletes
// ALL user_default_model rows pointing at the removed subscription — across
// every sender dimension, including the canonical "user-%d" rows written by
// SetUserDefaultModelByUserID. Previously the cleanup was
// `sender_id = <sub's sender_id>`, leaving "user-%d" rows dangling → GetDefault
// resolved a NULL subscription for every canonical-user read path.
func TestRemoveSubscriptionCleansUserDefaultModel(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)
	seedSystemSubscription(t, svc)

	const sender = "subowner-m8"
	sub := &LLMSubscription{
		ID: "sub-m8", Name: "m8", Provider: "openai",
		BaseURL: "http://m8", APIKey: "k", Model: "m-1", SenderID: sender,
	}
	if err := svc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Canonical-user dimension row (SetUserDefaultModelByUserID writes user-%d).
	const userID int64 = 42
	if _, err := db.Conn().Exec("INSERT INTO users (id, display_name, role) VALUES (42, 'u42', 'user')"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := svc.SetUserDefaultModelByUserID(userID, sub.ID, "m-1"); err != nil {
		t.Fatalf("SetUserDefaultModelByUserID: %v", err)
	}
	// Legacy sender-dimension row.
	if err := svc.SetUserDefaultModel(sender, sub.ID, "m-1"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}

	if err := svc.Remove(sub.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Both dimensions must be clean — no dangling subscription_id references.
	for _, q := range []struct {
		label string
		sql   string
		args  []any
	}{
		{"canonical user-%d row", "SELECT COUNT(*) FROM user_default_model WHERE subscription_id = ?", []any{sub.ID}},
		{"any residual row", "SELECT COUNT(*) FROM user_default_model WHERE subscription_id = ?", []any{sub.ID}},
	} {
		var n int
		if err := db.Conn().QueryRow(q.sql, q.args...).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", q.label, err)
		}
		if n != 0 {
			t.Errorf("%s: %d user_default_model rows still reference removed subscription %s", q.label, n, sub.ID)
		}
	}

	// GetDefault must fall back to the system subscription instead of nil.
	got, err := svc.GetDefault(sender)
	if err != nil {
		t.Fatalf("GetDefault after removal: %v", err)
	}
	if got == nil {
		t.Fatal("GetDefault returned nil after the default subscription was removed — must fall back to the system subscription")
	}
	if got.ID != "system" {
		t.Errorf("GetDefault = %q, want system subscription fallback (id=system)", got.ID)
	}
}

// TestGetDefaultDanglingSubscriptionFallsBack (m8 part 2) simulates the
// dangling-reference read path WITHOUT Remove: user_default_model still points
// at a deleted subscription (legacy dangling row from before the cleanup fix).
// GetDefault must return the system subscription fallback, not nil.
func TestGetDefaultDanglingSubscriptionFallsBack(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)
	seedSystemSubscription(t, svc)

	const sender = "dangler-m8"
	if _, err := db.Conn().Exec(
		"INSERT INTO user_default_model (sender_id, subscription_id, model, updated_at) VALUES (?, 'ghost-sub', 'm-1', datetime('now'))",
		sender,
	); err != nil {
		t.Fatalf("seed dangling user_default_model: %v", err)
	}

	got, err := svc.GetDefault(sender)
	if err != nil {
		t.Fatalf("GetDefault with dangling subscription_id: %v", err)
	}
	if got == nil {
		t.Fatal("GetDefault returned nil for a dangling subscription reference — must fall back to the system subscription")
	}
	if got.ID != "system" {
		t.Errorf("GetDefault = %q, want system subscription fallback", got.ID)
	}
}
