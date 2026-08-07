package agent

import (
	"testing"

	"xbot/storage/sqlite"
)

// TestIdentityResolver_CrossChannelResolve verifies that when the same
// channel_user_id (e.g. "web-1") appears in a different channel than it was
// originally registered under, Resolve reuses the existing canonical user
// instead of auto-creating a duplicate.
//
// Reproduces the root cause of "tenant belongs to another user" errors: a web
// user (web-1, user_id=2 under channel="web") accesses a CLI session, causing
// Resolve("cli","web-1") to create a NEW user (user_id=6) because the
// channel-scoped lookup misses the existing (web, web-1) identity.
func TestIdentityResolver_CrossChannelResolve(t *testing.T) {
	db, err := sqlite.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	resolver := NewIdentityResolver(db.Conn())

	// Schema init may create default users; capture the baseline.
	var initialUserCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM users").Scan(&initialUserCount); err != nil {
		t.Fatalf("count initial users: %v", err)
	}

	// 1. First resolve: "web-1" under "web" channel → creates a new user.
	webUserID, webRole, err := resolver.Resolve("web", "web-1")
	if err != nil {
		t.Fatalf("resolve web/web-1: %v", err)
	}
	if webUserID == 0 {
		t.Fatal("expected non-zero user_id for web/web-1")
	}

	// 2. Second resolve: same channel_user_id "web-1" under "cli" channel.
	//    BEFORE FIX: creates a new user (different user_id).
	//    AFTER FIX:  reuses the existing user_id via cross-channel lookup.
	cliUserID, _, err := resolver.Resolve("cli", "web-1")
	if err != nil {
		t.Fatalf("resolve cli/web-1: %v", err)
	}
	if cliUserID != webUserID {
		t.Errorf("cross-channel resolve: cli/web-1 → user_id=%d, want %d (same as web/web-1)",
			cliUserID, webUserID)
	}

	// 3. No orphan users should be created — cross-channel lookup must reuse,
	//    not auto-create. Count users before and after the second resolve.
	var userCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount); err != nil {
		t.Fatalf("count users: %v", err)
	}
	if userCount != initialUserCount+1 {
		t.Errorf("orphan users: got %d, want %d (initial %d + 1 auto-created)",
			userCount, initialUserCount+1, initialUserCount)
	}

	// 4. The identity should be linked under the "cli" channel.
	var linkedUserID int64
	if err := db.Conn().QueryRow(
		`SELECT user_id FROM user_identities WHERE channel = 'cli' AND channel_user_id = 'web-1'`,
	).Scan(&linkedUserID); err != nil {
		t.Fatalf("read cli/web-1 identity: %v", err)
	}
	if linkedUserID != webUserID {
		t.Errorf("cli/web-1 linked to user_id=%d, want %d", linkedUserID, webUserID)
	}

	// 5. Role should be preserved.
	if webRole != "user" {
		t.Errorf("expected role 'user', got %q", webRole)
	}
}

// TestIdentityResolver_SameChannelReresolve verifies that repeated calls to
// Resolve with the same (channel, channel_user_id) return the same user_id
// without creating duplicates.
func TestIdentityResolver_SameChannelReresolve(t *testing.T) {
	db, err := sqlite.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	resolver := NewIdentityResolver(db.Conn())

	uid1, _, err := resolver.Resolve("feishu", "ou_abc123")
	if err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	uid2, _, err := resolver.Resolve("feishu", "ou_abc123")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if uid1 != uid2 {
		t.Errorf("repeated resolve: got %d then %d, want same", uid1, uid2)
	}

	var initialCount, afterCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM users").Scan(&initialCount); err != nil {
		t.Fatalf("count before: %v", err)
	}
	// A repeated resolve should not create any new user.
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM users").Scan(&afterCount); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if afterCount != initialCount {
		t.Errorf("repeated resolve created users: %d → %d", initialCount, afterCount)
	}
}

// TestIdentityResolver_TrulyNewIdentity verifies that a channel_user_id that
// doesn't exist in ANY channel still auto-creates a new user.
func TestIdentityResolver_TrulyNewIdentity(t *testing.T) {
	db, err := sqlite.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()

	resolver := NewIdentityResolver(db.Conn())

	var initialCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM users").Scan(&initialCount); err != nil {
		t.Fatalf("count before: %v", err)
	}

	uid1, _, err := resolver.Resolve("web", "web-1")
	if err != nil {
		t.Fatalf("resolve web/web-1: %v", err)
	}
	// A completely different identity — should create a new user.
	uid2, _, err := resolver.Resolve("feishu", "ou_different")
	if err != nil {
		t.Fatalf("resolve feishu/ou_different: %v", err)
	}
	if uid1 == uid2 {
		t.Error("expected different user_ids for different channel_user_ids")
	}

	// Two truly new identities → 2 new users.
	var afterCount int
	if err := db.Conn().QueryRow("SELECT COUNT(*) FROM users").Scan(&afterCount); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if afterCount != initialCount+2 {
		t.Errorf("expected %d users (initial %d + 2), got %d", initialCount+2, initialCount, afterCount)
	}
}
