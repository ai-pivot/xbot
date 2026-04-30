package plugin

import (
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Plugin Profiler — aggregate performance metrics per plugin
// ---------------------------------------------------------------------------

// PluginProfile records aggregate performance metrics for a single plugin.
type PluginProfile struct {
	PluginID         string
	ToolCalls        int64
	ToolCallTime     time.Duration
	HookCalls        int64
	HookCallTime     time.Duration
	EnricherCalls    int64
	EnricherCallTime time.Duration
	LastToolCall     time.Time
	LastHookCall     time.Time
	LastEnricherCall time.Time
}

// Profiler collects performance profiles for plugins.
// It is safe for concurrent use.
type Profiler struct {
	mu       sync.Mutex
	profiles map[string]*PluginProfile
}

// NewProfiler creates a new Profiler.
func NewProfiler() *Profiler {
	return &Profiler{
		profiles: make(map[string]*PluginProfile),
	}
}

// RecordToolCall records a tool invocation for the given plugin.
func (p *Profiler) RecordToolCall(pluginID string, duration time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	prof := p.getOrCreate(pluginID)
	prof.ToolCalls++
	prof.ToolCallTime += duration
	prof.LastToolCall = time.Now()
}

// RecordHookCall records a hook invocation for the given plugin.
func (p *Profiler) RecordHookCall(pluginID string, duration time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	prof := p.getOrCreate(pluginID)
	prof.HookCalls++
	prof.HookCallTime += duration
	prof.LastHookCall = time.Now()
}

// RecordEnricherCall records a context enricher invocation for the given plugin.
func (p *Profiler) RecordEnricherCall(pluginID string, duration time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	prof := p.getOrCreate(pluginID)
	prof.EnricherCalls++
	prof.EnricherCallTime += duration
	prof.LastEnricherCall = time.Now()
}

// GetProfile returns the profile for the given plugin.
// If the plugin has no recorded metrics, a zero-value PluginProfile is returned.
func (p *Profiler) GetProfile(pluginID string) PluginProfile {
	p.mu.Lock()
	defer p.mu.Unlock()

	if prof, ok := p.profiles[pluginID]; ok {
		return *prof
	}
	return PluginProfile{PluginID: pluginID}
}

// GetAllProfiles returns a copy of all plugin profiles.
func (p *Profiler) GetAllProfiles() map[string]PluginProfile {
	p.mu.Lock()
	defer p.mu.Unlock()

	result := make(map[string]PluginProfile, len(p.profiles))
	for id, prof := range p.profiles {
		result[id] = *prof
	}
	return result
}

// Reset clears the profile for a single plugin.
func (p *Profiler) Reset(pluginID string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	delete(p.profiles, pluginID)
}

// ResetAll clears all plugin profiles.
func (p *Profiler) ResetAll() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.profiles = make(map[string]*PluginProfile)
}

// getOrCreate returns the profile for pluginID, creating it if necessary.
// Caller must hold p.mu.
func (p *Profiler) getOrCreate(pluginID string) *PluginProfile {
	prof, ok := p.profiles[pluginID]
	if !ok {
		prof = &PluginProfile{PluginID: pluginID}
		p.profiles[pluginID] = prof
	}
	return prof
}
