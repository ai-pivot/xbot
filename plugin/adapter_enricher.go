package plugin

import (
	"context"
	"fmt"
	"sort"
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Context Enricher Adapter — bridges plugin enrichers to MessageMiddleware
// ---------------------------------------------------------------------------

// EnricherRegistry holds all plugin-registered context enrichers.
// It is used by the middleware integration to inject dynamic content
// into the system prompt.
type EnricherRegistry struct {
	mu        sync.RWMutex
	enrichers []enricherEntry
}

type enricherEntry struct {
	pluginID string
	name     string
	enricher ContextEnricher
	priority int // lower = earlier in prompt
}

// NewEnricherRegistry creates a new registry.
func NewEnricherRegistry() *EnricherRegistry {
	return &EnricherRegistry{}
}

// Register adds a plugin context enricher.
func (r *EnricherRegistry) Register(pluginID, name string, enricher ContextEnricher, priority int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enrichers = append(r.enrichers, enricherEntry{
		pluginID: pluginID,
		name:     name,
		enricher: enricher,
		priority: priority,
	})
	sort.Slice(r.enrichers, func(i, j int) bool {
		return r.enrichers[i].priority < r.enrichers[j].priority
	})
	log.Glob(log.CatPlugin).WithField("plugin", pluginID).WithField("name", name).
		Debug("Context enricher registered")
}

// RunAll executes all registered enrichers and returns concatenated content.
// Errors from individual enrichers are logged and skipped.
func (r *EnricherRegistry) RunAll(ctx context.Context) string {
	r.mu.RLock()
	enrichers := make([]enricherEntry, len(r.enrichers))
	copy(enrichers, r.enrichers)
	r.mu.RUnlock()

	var result string
	for _, entry := range enrichers {
		content, err := entry.enricher(ctx)
		if err != nil {
			log.Glob(log.CatPlugin).WithField("plugin", entry.pluginID).WithField("enricher", entry.name).
				Warn("Context enricher error: ", err)
			continue
		}
		if content != "" {
			result += fmt.Sprintf("\n## %s (%s)\n%s\n", entry.name, entry.pluginID, content)
		}
	}
	return result
}

// List returns all registered enricher names.
func (r *EnricherRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.enrichers))
	for _, e := range r.enrichers {
		names = append(names, fmt.Sprintf("%s (%s)", e.name, e.pluginID))
	}
	return names
}

// Count returns the number of registered enrichers.
func (r *EnricherRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.enrichers)
}
