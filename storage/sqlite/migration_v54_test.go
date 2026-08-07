package sqlite

import (
	"testing"
)

// TestMigrateV53ToV54_DuplicateIdentities verifies that the migration merges
// duplicate users created by the cross-channel identity bug. Before the fix,
// the same channel_user_id (e.g. "web-1") could be linked to different
// user_ids across channels. The migration picks the canonical user (most
// identities) and merges the others into it.
func TestMigrateV53ToV54_DuplicateIdentities(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	conn := db.Conn()

	// Simulate the bug: "web-1" registered under two channels with different user_ids.
	// user 100: admin (canonical — has 2 identities vs 1).
	// user 101: user (duplicate — will be merged into 100).
	conn.Exec(`INSERT INTO users (id, role) VALUES (100, 'admin')`)
	conn.Exec(`INSERT INTO users (id, role) VALUES (101, 'user')`)
	conn.Exec(`INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (100, 'web', 'web-1')`)
	conn.Exec(`INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (100, 'feishu', 'web-1')`)
	conn.Exec(`INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (101, 'cli', 'web-1')`)

	// Add asset referencing the duplicate user to verify migration.
	conn.Exec(`INSERT INTO tenants (channel, chat_id, owner_user_id, created_at, last_active_at)
		VALUES ('cli', '/home/test', 101, datetime('now'), datetime('now'))`)
	conn.Exec(`INSERT INTO user_chats (channel, chat_id, sender_id, user_id, label)
		VALUES ('cli', '/home/test', 'web-1', 101, 'Test')`)

	// Run migration.
	if err := migrateV53ToV54(conn); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// Source user should be deleted.
	var count int
	if err := conn.QueryRow("SELECT COUNT(*) FROM users WHERE id = 101").Scan(&count); err != nil {
		t.Fatalf("check source user: %v", err)
	}
	if count != 0 {
		t.Error("source user 101 should be deleted")
	}

	// Canonical user should still exist.
	if err := conn.QueryRow("SELECT COUNT(*) FROM users WHERE id = 100").Scan(&count); err != nil {
		t.Fatalf("check canonical user: %v", err)
	}
	if count != 1 {
		t.Error("canonical user 100 should still exist")
	}

	// Identity should be re-linked to canonical user.
	var linkedUID int64
	if err := conn.QueryRow(
		`SELECT user_id FROM user_identities WHERE channel = 'cli' AND channel_user_id = 'web-1'`,
	).Scan(&linkedUID); err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if linkedUID != 100 {
		t.Errorf("identity linked to %d, want 100", linkedUID)
	}

	// Tenant owner should be migrated.
	var ownerUID int64
	if err := conn.QueryRow(
		`SELECT COALESCE(owner_user_id, 0) FROM tenants WHERE channel = 'cli' AND chat_id = '/home/test'`,
	).Scan(&ownerUID); err != nil {
		t.Fatalf("read tenant owner: %v", err)
	}
	if ownerUID != 100 {
		t.Errorf("tenant owner = %d, want 100", ownerUID)
	}

	// user_chats should be migrated.
	var chatUID int64
	if err := conn.QueryRow(
		`SELECT COALESCE(user_id, 0) FROM user_chats WHERE channel = 'cli' AND chat_id = '/home/test'`,
	).Scan(&chatUID); err != nil {
		t.Fatalf("read user_chats: %v", err)
	}
	if chatUID != 100 {
		t.Errorf("user_chats user_id = %d, want 100", chatUID)
	}

	// Total users should have decreased by 1 (source deleted).
	var totalUsers int
	if err := conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&totalUsers); err != nil {
		t.Fatalf("count users: %v", err)
	}
	// Schema init may create default users; we inserted 2, deleted 1 → net +1.
}

// TestMigrateV53ToV54_NoDuplicates verifies the migration is a no-op when
// no duplicate identities exist.
func TestMigrateV53ToV54_NoDuplicates(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	conn := db.Conn()

	var beforeCount int
	conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&beforeCount)

	if err := migrateV53ToV54(conn); err != nil {
		t.Fatalf("migration: %v", err)
	}

	var afterCount int
	conn.QueryRow("SELECT COUNT(*) FROM users").Scan(&afterCount)
	if afterCount != beforeCount {
		t.Errorf("no-op migration changed user count: %d → %d", beforeCount, afterCount)
	}
}

// TestMigrateV53ToV54_RoleEscalation verifies that if the source user is an
// admin and the target is a regular user, the target is escalated to admin.
func TestMigrateV53ToV54_RoleEscalation(t *testing.T) {
	db, err := Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer db.Close()
	conn := db.Conn()

	// Source is admin (fewer identities), target is user (more identities).
	// Canonical = target (more identities), but source has admin role.
	// Target should be escalated to admin.
	conn.Exec(`INSERT INTO users (id, role) VALUES (200, 'user')`)
	conn.Exec(`INSERT INTO users (id, role) VALUES (201, 'admin')`)
	conn.Exec(`INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (200, 'web', 'escalation-test')`)
	conn.Exec(`INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (200, 'feishu', 'escalation-test')`)
	conn.Exec(`INSERT INTO user_identities (user_id, channel, channel_user_id) VALUES (201, 'cli', 'escalation-test')`)

	if err := migrateV53ToV54(conn); err != nil {
		t.Fatalf("migration: %v", err)
	}

	// Source (admin, 201) should be deleted.
	var count int
	conn.QueryRow("SELECT COUNT(*) FROM users WHERE id = 201").Scan(&count)
	if count != 0 {
		t.Error("admin source user 201 should be deleted")
	}

	// Target (user, 200) should now be admin.
	var role string
	if err := conn.QueryRow("SELECT role FROM users WHERE id = 200").Scan(&role); err != nil {
		t.Fatalf("read target role: %v", err)
	}
	if role != "admin" {
		t.Errorf("target role = %q, want 'admin'", role)
	}
}
