package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// ============================================================================
// Supervision state — plugin-side self-healing
//
// The pipes live in this process, so a plugin restart (server restart, or the
// host killing us after a 30s RPC overrun) drops every connection. To make the
// model self-healing WITHOUT requiring a browser to be open, the plugin persists
// the targets it was told to keep connected and re-arms them on activation —
// "every connection automatically opens an SSH pipe".
//
// The file lives in the plugin's own directory (the host runs the plugin process
// with its directory as CWD) and is written 0600: connect_cmd carries the runner
// connect token, which already exists in the server DB on the same host. Only
// targets explicitly marked auto_connect are re-armed, so this is opt-in.
// ============================================================================

const supervisionStateFile = "state.json"

// supervisedTarget is one persisted supervision entry.
type supervisedTarget struct {
	SSH         string `json:"ssh"`
	Name        string `json:"name"`
	ConnectCmd  string `json:"connect_cmd"`
	InstallDir  string `json:"install_dir"`
	ConnMode    string `json:"connection_mode"`
	AutoConnect bool   `json:"auto_connect"`
}

// stateStore persists the supervision set.
type stateStore struct {
	mu    sync.Mutex
	path  string
	items map[string]supervisedTarget // name → entry
}

func newStateStore(path string) *stateStore {
	return &stateStore{path: path, items: map[string]supervisedTarget{}}
}

// defaultStatePath resolves the state file inside the plugin's own directory.
func defaultStatePath() string {
	exe, err := os.Executable()
	if err != nil {
		return supervisionStateFile
	}
	return filepath.Join(filepath.Dir(exe), "..", supervisionStateFile)
}

// Load reads the state file (missing file is not an error: first run).
func (s *stateStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read supervision state: %w", err)
	}
	var items []supervisedTarget
	if err := json.Unmarshal(data, &items); err != nil {
		return fmt.Errorf("parse supervision state: %w", err)
	}
	s.items = make(map[string]supervisedTarget, len(items))
	for _, it := range items {
		if it.Name != "" {
			s.items[it.Name] = it
		}
	}
	return nil
}

// Put records (or replaces) an entry and persists atomically.
func (s *stateStore) Put(t supervisedTarget) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[t.Name] = t
	return s.saveLocked()
}

// Delete removes an entry and persists.
func (s *stateStore) Delete(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, name)
	return s.saveLocked()
}

// AutoConnectTargets returns the entries that must be re-armed on start.
func (s *stateStore) AutoConnectTargets() []supervisedTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]supervisedTarget, 0, len(s.items))
	for _, it := range s.items {
		if it.AutoConnect {
			out = append(out, it)
		}
	}
	return out
}

// saveLocked writes tmp+rename so a crash can never leave a truncated file.
func (s *stateStore) saveLocked() error {
	items := make([]supervisedTarget, 0, len(s.items))
	for _, it := range s.items {
		items = append(items, it)
	}
	data, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("encode supervision state: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("prepare supervision state dir: %w", err)
	}
	tmp := s.path + ".tmp"
	// 0600: the file carries connect tokens; keep it owner-only.
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write supervision state: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("commit supervision state: %w", err)
	}
	return nil
}
