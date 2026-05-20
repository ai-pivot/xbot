package agent

import (
	"path/filepath"
	"testing"

	"xbot/storage/sqlite"
)

func TestSettingsServiceGetSettings(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// Set some values
	if err := svc.SetSetting("feishu", "user1", "context_mode", "phase1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get should return them
	settings, err := svc.GetSettings("feishu", "user1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if settings["context_mode"] != "phase1" {
		t.Errorf("expected 'phase1', got %q", settings["context_mode"])
	}
}

func TestSettingsServiceGetSettingsUI(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// No channelFinder set — should return "no settings" fallback
	ui, err := svc.GetSettingsUI("test", "user1")
	if err != nil {
		t.Fatalf("get settings ui: %v", err)
	}
	if ui != "当前渠道没有可配置的设置项。" {
		t.Errorf("expected no settings message, got %q", ui)
	}
}

func TestSettingsServiceSubmitSettingsTextMode(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// No channelFinder set — text mode fallback
	err = svc.SubmitSettings("cli", "user1", "key1=value1\nkey2=value2")
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	settings, _ := svc.GetSettings("cli", "user1")
	if settings["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %q", settings["key1"])
	}
	if settings["key2"] != "value2" {
		t.Errorf("expected key2=value2, got %q", settings["key2"])
	}

	// Test error on invalid format
	err = svc.SubmitSettings("cli", "user1", "invalid_line_no_equals")
	if err == nil {
		t.Error("expected error for invalid format")
	}
}

func TestGetEffectiveSetting(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// Case 1: No value in DB → returns schema default
	got := svc.GetEffectiveSetting("cli", "user1", "auto_worktree")
	if got != "false" {
		t.Errorf("expected default 'false', got %q", got)
	}

	// Case 2: Value set in DB → returns DB value
	if err := svc.SetSetting("cli", "user1", "auto_worktree", "true"); err != nil {
		t.Fatalf("set: %v", err)
	}
	got = svc.GetEffectiveSetting("cli", "user1", "auto_worktree")
	if got != "true" {
		t.Errorf("expected 'true', got %q", got)
	}

	// Case 3: Different user → no DB value, returns default
	got = svc.GetEffectiveSetting("cli", "user2", "auto_worktree")
	if got != "false" {
		t.Errorf("expected default 'false' for user2, got %q", got)
	}

	// Case 4: Unknown key → returns "" (no default)
	got = svc.GetEffectiveSetting("cli", "user1", "nonexistent_key_xyz")
	if got != "" {
		t.Errorf("expected empty for unknown key, got %q", got)
	}
}

func TestGetEffectiveSettingBool(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	store := sqlite.NewUserSettingsService(db)
	svc := NewSettingsService(store)

	// Default: "false" → bool false
	if svc.GetEffectiveSettingBool("cli", "user1", "auto_worktree") {
		t.Error("expected false for default auto_worktree")
	}

	// Set to "true"
	svc.SetSetting("cli", "user1", "auto_worktree", "true")
	if !svc.GetEffectiveSettingBool("cli", "user1", "auto_worktree") {
		t.Error("expected true after setting auto_worktree=true")
	}

	// Various falsy values
	for _, v := range []string{"", "false", "False", "0", "no"} {
		svc.SetSetting("cli", "user1", "auto_worktree", v)
		if svc.GetEffectiveSettingBool("cli", "user1", "auto_worktree") {
			t.Errorf("expected false for value %q", v)
		}
	}

	// Truthy: "1"
	svc.SetSetting("cli", "user1", "auto_worktree", "1")
	if !svc.GetEffectiveSettingBool("cli", "user1", "auto_worktree") {
		t.Error("expected true for value '1'")
	}
}

func TestGetEffectiveSetting_NilService(t *testing.T) {
	var svc *SettingsService
	// Nil service should return schema default, not panic
	got := svc.GetEffectiveSetting("cli", "user1", "auto_worktree")
	if got != "false" {
		t.Errorf("expected default 'false' from nil service, got %q", got)
	}
	if svc.GetEffectiveSettingBool("cli", "user1", "auto_worktree") {
		t.Error("expected false from nil service")
	}
}
