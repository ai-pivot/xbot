package runner

import (
	"fmt"
	"sync"
	"time"

	"xbot/tools"
)

// Manager tracks all runner instances and session-to-runner bindings.
//
// It is the single source of truth for which runner a session uses.
// Resolution order: session binding → DB binding → default local runner.
//
// Thread-safe: all exported methods acquire the appropriate lock.
type Manager struct {
	mu       sync.RWMutex
	runners  map[string]*Instance // ID → runner
	sessions map[string]string    // "channel:chatID" → runnerID

	localRunner *Instance // default runner, always present, never removed
}

// NewManager creates a Manager with the local runner pre-registered.
// The local runner ID is always "local" and starts with an empty tool set.
// Tools are added via SetLocalTools after construction.
func NewManager() *Manager {
	local := &Instance{
		ID:        "local",
		Name:      "Local Runner",
		Type:      Local,
		Status:    StatusConnected,
		Tools:     make(map[string]tools.Tool),
		CreatedAt: time.Now(),
	}
	return &Manager{
		runners:     map[string]*Instance{"local": local},
		sessions:    make(map[string]string),
		localRunner: local,
	}
}

// SetLocalTools populates the local runner's tool set.
func (m *Manager) SetLocalTools(toolList []tools.Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.localRunner.Tools = make(map[string]tools.Tool, len(toolList))
	for _, t := range toolList {
		m.localRunner.Tools[t.Name()] = t
	}
}

// Local returns the local runner instance (always available).
func (m *Manager) Local() *Instance {
	return m.localRunner
}

// Get looks up a runner by ID.
func (m *Manager) Get(id string) (*Instance, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runners[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return r, nil
}

// List returns all registered runners.
func (m *Manager) List() []*Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]*Instance, 0, len(m.runners))
	for _, r := range m.runners {
		result = append(result, r)
	}
	return result
}

// ResolveSession returns the runner bound to a session, falling back to local.
// key is "channel:chatID" format.
func (m *Manager) ResolveSession(key string) *Instance {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if runnerID, ok := m.sessions[key]; ok {
		if r, ok := m.runners[runnerID]; ok {
			return r
		}
	}
	return m.localRunner
}

// BindSession binds a session to a specific runner.
// Returns an error if the runner does not exist.
func (m *Manager) BindSession(key, runnerID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[runnerID]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, runnerID)
	}
	m.sessions[key] = runnerID
	return nil
}

// UnbindSession removes a session's runner binding (falls back to local).
func (m *Manager) UnbindSession(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, key)
}

// Add registers a new runner instance. Returns an error if the ID already exists.
func (m *Manager) Add(r *Instance) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[r.ID]; ok {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, r.ID)
	}
	m.runners[r.ID] = r
	return nil
}

// Remove removes a runner by ID. The local runner cannot be removed.
func (m *Manager) Remove(id string) error {
	if id == "local" {
		return fmt.Errorf("%w: cannot remove local runner", ErrLocalOnly)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runners[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(m.runners, id)
	// Clean up session bindings to this runner
	for k, v := range m.sessions {
		if v == id {
			delete(m.sessions, k)
		}
	}
	return nil
}

// SetStatus updates a runner's connection status.
func (m *Manager) SetStatus(id string, s Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runners[id]; ok {
		r.Status = s
		r.LastSeenAt = time.Now()
	}
}
