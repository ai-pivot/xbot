package sqlite

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestUserSettingsCRUD(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := NewUserSettingsService(db)

	// Get empty
	settings, err := svc.Get("feishu", "user1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(settings) != 0 {
		t.Errorf("expected empty settings, got %v", settings)
	}

	// Set a value
	if err := svc.Set("feishu", "user1", "reply_style", "concise"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get should return it
	settings, err = svc.Get("feishu", "user1")
	if err != nil {
		t.Fatalf("get after set: %v", err)
	}
	if settings["reply_style"] != "concise" {
		t.Errorf("expected 'concise', got %q", settings["reply_style"])
	}

	// Update (upsert)
	if err := svc.Set("feishu", "user1", "reply_style", "detailed"); err != nil {
		t.Fatalf("set update: %v", err)
	}
	settings, _ = svc.Get("feishu", "user1")
	if settings["reply_style"] != "detailed" {
		t.Errorf("expected 'detailed', got %q", settings["reply_style"])
	}

	// Set another key
	if err := svc.Set("feishu", "user1", "language", "zh"); err != nil {
		t.Fatalf("set language: %v", err)
	}
	settings, _ = svc.Get("feishu", "user1")
	if len(settings) != 2 {
		t.Errorf("expected 2 settings, got %d", len(settings))
	}

	// Delete
	if err := svc.Delete("feishu", "user1", "reply_style"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	settings, _ = svc.Get("feishu", "user1")
	if _, ok := settings["reply_style"]; ok {
		t.Error("expected reply_style to be deleted")
	}
	if settings["language"] != "zh" {
		t.Error("language should still exist")
	}

	// Different channel should have separate settings
	settings2, _ := svc.Get("cli", "user1")
	if len(settings2) != 0 {
		t.Errorf("expected empty settings for different channel, got %v", settings2)
	}
}

func TestUserSettingsDifferentSenders(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	svc := NewUserSettingsService(db)

	// SINGLE OPERATOR (v63 removed the multi-user system): the sender dimension
	// collapsed to one operator, so both sender ids address the SAME settings
	// row and the last write wins. This previously asserted per-user isolation.
	if err := svc.Set("feishu", "user1", "style", "a"); err != nil {
		t.Fatalf("set user1: %v", err)
	}
	if err := svc.Set("feishu", "user2", "style", "b"); err != nil {
		t.Fatalf("set user2: %v", err)
	}

	s1, _ := svc.Get("feishu", "user1")
	s2, _ := svc.Get("feishu", "user2")
	if s1["style"] != "b" || s2["style"] != "b" {
		t.Errorf("single operator: both sender ids read the same row, got user1=%q user2=%q", s1["style"], s2["style"])
	}
}
func TestUserSettingsNilDB(t *testing.T) {
	// Regression test: UserSettingsService with nil DB should return error, not panic.
	// This can happen if the service is created before DB is initialized (e.g. SubAgent path).
	svc := &UserSettingsService{db: nil}

	// Get should return error, not panic
	_, err := svc.Get("feishu", "user1")
	if err == nil {
		t.Fatal("expected error from Get with nil db")
		return
	}

	// Set should return error, not panic
	err = svc.Set("feishu", "user1", "key", "value")
	if err == nil {
		t.Fatal("expected error from Set with nil db")
		return
	}

	// Delete should return error, not panic
	err = svc.Delete("feishu", "user1", "key")
	if err == nil {
		t.Fatal("expected error from Delete with nil db")
		return
	}
}

// Generic settings are single-operator, so private per-Web-user group rows
// must be excluded at the store boundary (including namespace-based RPCs).
func TestUserSettingsSessionGroupsPrivate(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "private.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewUserSettingsService(db)
	for _, namespace := range []string{"web", "cli", "custom"} {
		for _, sender := range []string{"web-1", "web-2", "cli_user"} {
			_, err := db.Conn().Exec(`INSERT INTO user_settings(channel,sender_id,key,value,updated_at) VALUES(?,?,?,?,0)`, namespace, sender, SessionGroupsSettingKey, "private-"+sender)
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := svc.Set(namespace, "web-1", "ordinary", "public"); err != nil {
			t.Fatal(err)
		}
		for _, sender := range []string{"web-1", "web-2", "cli_user", "unknown"} {
			got, err := svc.Get(namespace, sender)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := got[SessionGroupsSettingKey]; ok {
				t.Fatalf("private groups leaked to %s/%s", namespace, sender)
			}
			if got["ordinary"] != "public" {
				t.Fatalf("unrelated setting changed: %v", got)
			}
			if err := svc.Set(namespace, sender, SessionGroupsSettingKey, "overwrite"); !errors.Is(err, ErrPrivateSetting) {
				t.Fatalf("private write not blocked: %v", err)
			}
			if err := svc.Delete(namespace, sender, SessionGroupsSettingKey); !errors.Is(err, ErrPrivateSetting) {
				t.Fatalf("private delete not blocked: %v", err)
			}
		}
		rows, err := db.Conn().Query(`SELECT sender_id,value FROM user_settings WHERE channel=? AND key=?`, namespace, SessionGroupsSettingKey)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for rows.Next() {
			var sender, value string
			if err := rows.Scan(&sender, &value); err != nil {
				t.Fatal(err)
			}
			if value != "private-"+sender {
				t.Fatal("private document overwritten")
			}
			count++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if count != 3 {
			t.Fatalf("private documents removed: %d", count)
		}
		if err := svc.Delete(namespace, "web-2", "ordinary"); err != nil {
			t.Fatal(err)
		}
		got, err := svc.Get(namespace, "web-1")
		if err != nil || len(got) != 0 {
			t.Fatalf("ordinary deletion changed: %v %v", got, err)
		}
	}
}
