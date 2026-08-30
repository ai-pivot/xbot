package sqlite

import "testing"

// TestSetUserDefaultModelByUserID_MultiUser verifies that multiple distinct
// canonical users can each persist their default model (M3).
//
// Reproduction: the INSERT fallback hardcoded sender_id=” — the PK of
// user_default_model is sender_id (single column), so the second user's
// UPDATE (0 rows) → INSERT hit "UNIQUE constraint failed: user_default_model.sender_id".
func TestSetUserDefaultModelByUserID_MultiUser(t *testing.T) {
	db := openTestDB(t)
	svc := NewLLMSubscriptionService(db)

	users := []int64{1, 2, 3}
	for _, userID := range users {
		if err := svc.SetUserDefaultModelByUserID(userID, "sub-multi", "gpt-x"); err != nil {
			t.Fatalf("SetUserDefaultModelByUserID(user %d) failed: %v", userID, err)
		}
		udm, err := svc.GetUserDefaultModelByUserID(userID)
		if err != nil {
			t.Fatalf("GetUserDefaultModelByUserID(user %d) failed: %v", userID, err)
		}
		if udm == nil {
			t.Fatalf("user %d: default model not persisted", userID)
		}
		if udm.SubscriptionID != "sub-multi" || udm.Model != "gpt-x" {
			t.Errorf("user %d: got (%s, %s), want (sub-multi, gpt-x)", userID, udm.SubscriptionID, udm.Model)
		}
	}
}
