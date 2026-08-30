package sqlite

import (
	"testing"
)

// TestRemoveSubscriptionCleansUserDefaultModel (m8) verifies Remove deletes
// ALL user_default_model rows pointing at the removed subscription — across
// every sender dimension. Previously the cleanup was
// `sender_id = <sub's sender_id>`, leaving other senders' rows dangling →
// GetDefault resolved a NULL subscription.
// Since v62 (system subscription removed), GetDefault returns (nil, nil) when
// the user has no default — the caller falls back to the in-memory
// defaultLLM built from cfg.LLM. Post-v63: one operator (sender collapse),
// the test guards the single sender dimension.
func TestRemoveSubscriptionCleansUserDefaultModel(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	const sender = "cli_user"
	sub := &LLMSubscription{
		ID: "sub-m8", Name: "m8", Provider: "openai",
		BaseURL: "http://m8", APIKey: "k", Model: "m-1", SenderID: sender,
	}
	if err := svc.Add(sub); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Default-model row pointing at the subscription.
	if err := svc.SetUserDefaultModel(sender, sub.ID, "m-1"); err != nil {
		t.Fatalf("SetUserDefaultModel: %v", err)
	}

	if err := svc.Remove(sub.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// No dangling subscription_id references may survive.
	var n int
	if err := db.Conn().QueryRow(
		"SELECT COUNT(*) FROM user_default_model WHERE subscription_id = ?",
		sub.ID,
	).Scan(&n); err != nil {
		t.Fatalf("count residual rows: %v", err)
	}
	if n != 0 {
		t.Errorf("%d user_default_model rows still reference removed subscription %s", n, sub.ID)
	}

	// GetDefault returns (nil, nil) after the default subscription was removed —
	// there is no system fallback anymore; the caller falls back to defaultLLM.
	got, err := svc.GetDefault(sender)
	if err != nil {
		t.Fatalf("GetDefault after removal: %v", err)
	}
	if got != nil {
		t.Errorf("GetDefault = %+v, want nil (no system fallback since v62)", got)
	}
}

// TestGetDefaultDanglingSubscriptionNil (m8 part 2) simulates the
// dangling-reference read path WITHOUT Remove: user_default_model still points
// at a deleted subscription (legacy dangling row from before the cleanup fix).
// Since v62, GetDefault returns (nil, nil) — no system subscription fallback.
func TestGetDefaultDanglingSubscriptionNil(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

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
	if got != nil {
		t.Errorf("GetDefault = %+v, want nil for a dangling subscription reference (no system fallback since v62)", got)
	}
}
