package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateStore_RoundTripAndAutoConnectFilter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	s := newStateStore(path)

	keep := supervisedTarget{
		SSH: "ssh a@h1", Name: "m1", ConnectCmd: "--server ws://s/ws --token t1",
		InstallDir: "/usr/local/bin", ConnMode: connModeTunnel, AutoConnect: true,
	}
	manual := supervisedTarget{
		SSH: "ssh b@h2", Name: "m2", ConnectCmd: "--server ws://s/ws --token t2",
		InstallDir: "/home/b/.local/bin", ConnMode: connModeDirect, AutoConnect: false,
	}
	if err := s.Put(keep); err != nil {
		t.Fatalf("put keep: %v", err)
	}
	if err := s.Put(manual); err != nil {
		t.Fatalf("put manual: %v", err)
	}

	// A fresh store must see exactly what was persisted.
	reloaded := newStateStore(path)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	auto := reloaded.AutoConnectTargets()
	if len(auto) != 1 || auto[0].Name != "m1" {
		t.Fatalf("auto-connect set = %+v, want only m1", auto)
	}
	if auto[0].ConnMode != connModeTunnel || auto[0].InstallDir != "/usr/local/bin" {
		t.Fatalf("entry fields lost in round trip: %+v", auto[0])
	}

	// Delete removes it from the persisted set too.
	if err := reloaded.Delete("m1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	again := newStateStore(path)
	if err := again.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := again.AutoConnectTargets(); len(got) != 0 {
		t.Fatalf("deleted target must not be re-armed: %+v", got)
	}
}

// Missing file is the first-run case, not an error.
func TestStateStore_MissingFileIsNotAnError(t *testing.T) {
	s := newStateStore(filepath.Join(t.TempDir(), "nope.json"))
	if err := s.Load(); err != nil {
		t.Fatalf("load of missing file: %v", err)
	}
	if len(s.AutoConnectTargets()) != 0 {
		t.Fatal("empty state must yield no targets")
	}
}

// The file carries connect tokens, so it must stay owner-only, and the write
// must be atomic (no partial file on crash, no leftover tmp).
func TestStateStore_PermissionsAndAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := newStateStore(path)
	if err := s.Put(supervisedTarget{Name: "m1", SSH: "ssh h", ConnectCmd: "--token secret"}); err != nil {
		t.Fatalf("put: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("state file mode = %o, want 600 (it carries a connect token)", perm)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file must be renamed away, got err=%v", err)
	}
}

// Corrupt state must be reported, not silently treated as "nothing to resume".
func TestStateStore_CorruptFileIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := newStateStore(path).Load(); err == nil {
		t.Fatal("corrupt state must surface an error")
	}
}
