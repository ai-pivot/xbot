package channel

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"xbot/protocol"
)

// cliTodoManager provides in-memory + file-backed TODO persistence for the CLI TUI.
// It stores per-session TODO lists and can save/load them from disk so todos
// survive session switches and TUI restarts.
//
// This is a local copy of the same logic from tools.TodoManager, using
// protocol.TodoItem instead of tools.TodoItem to avoid importing the tools package.
type cliTodoManager struct {
	mu    sync.RWMutex
	todos map[string][]protocol.TodoItem // sessionKey -> todos
}

func newCliTodoManager() *cliTodoManager {
	return &cliTodoManager{
		todos: make(map[string][]protocol.TodoItem),
	}
}

// cliTodoDir returns the base directory for TODO persistence files.
func cliTodoDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".xbot", "todos")
}

// cliTodoFilePath returns the file path for a given sessionKey.
func cliTodoFilePath(sessionKey string) string {
	h := sha256.Sum256([]byte(sessionKey))
	return filepath.Join(cliTodoDir(), fmt.Sprintf("%x.json", h[:16]))
}

// SaveToFile persists the TODO list for a session to a JSON file.
func (m *cliTodoManager) SaveToFile(sessionKey string) error {
	m.mu.RLock()
	items, ok := m.todos[sessionKey]
	if !ok {
		m.mu.RUnlock()
		_ = os.Remove(cliTodoFilePath(sessionKey))
		return nil
	}
	saved := make([]protocol.TodoItem, len(items))
	copy(saved, items)
	m.mu.RUnlock()

	dir := cliTodoDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	return os.WriteFile(cliTodoFilePath(sessionKey), data, 0o600)
}

// LoadFromFile loads the TODO list for a session from a JSON file.
func (m *cliTodoManager) LoadFromFile(sessionKey string) error {
	data, err := os.ReadFile(cliTodoFilePath(sessionKey))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []protocol.TodoItem
	if err := json.Unmarshal(data, &items); err != nil {
		return err
	}
	if len(items) == 0 {
		return nil
	}
	m.mu.Lock()
	m.todos[sessionKey] = items
	m.mu.Unlock()
	return nil
}

// SetTodos writes/updates the TODO list for a given session.
func (m *cliTodoManager) SetTodos(sessionKey string, items []protocol.TodoItem) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(items) == 0 {
		delete(m.todos, sessionKey)
		return
	}
	m.todos[sessionKey] = items
}

// GetTodos returns the TODO list for a given session.
func (m *cliTodoManager) GetTodos(sessionKey string) []protocol.TodoItem {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]protocol.TodoItem, len(m.todos[sessionKey]))
	copy(result, m.todos[sessionKey])
	return result
}
