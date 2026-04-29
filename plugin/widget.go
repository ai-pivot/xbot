package plugin

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	log "xbot/logger"
)

// RenderFunc converts WidgetSpan slices to a styled string for display.
// The TUI layer provides this to apply lipgloss/theme styling.
type RenderFunc func(spans []WidgetSpan, width int) string

// WidgetRegistry manages all plugin UI widget contributions. Thread-safe.
//
// Lifecycle:
//  1. PluginManager creates WidgetRegistry
//  2. On plugin Activate, widgets register via ContributeUI
//  3. TUI calls SetDefaultRenderFn to set lipgloss-based styling
//  4. TUI calls RefreshWidget(width) to render and cache
//  5. View() calls RenderZone(zone) to read cached strings (atomic, zero alloc)
type WidgetRegistry struct {
	mu              sync.RWMutex
	slots           map[string]*widgetSlot
	byZone          map[string][]*widgetSlot
	defaultRenderFn RenderFunc // set by TUI; used for push-based updates
}

type widgetSlot struct {
	pluginID string
	widgetID string
	zone     string
	priority int
	provider UIWidget
	content  atomic.Pointer[string]
}

func NewWidgetRegistry() *WidgetRegistry {
	return &WidgetRegistry{
		slots:  make(map[string]*widgetSlot),
		byZone: make(map[string][]*widgetSlot),
	}
}

func slotKey(pluginID, widgetID string) string {
	return pluginID + "/" + widgetID
}

func (r *WidgetRegistry) Register(pluginID, widgetID, zone string, provider UIWidget, priority int) error {
	if pluginID == "" || widgetID == "" {
		return fmt.Errorf("pluginID and widgetID are required")
	}
	if provider == nil {
		return fmt.Errorf("UIWidget provider must not be nil")
	}
	if !validUISlots[zone] {
		return fmt.Errorf("invalid zone %q", zone)
	}
	key := slotKey(pluginID, widgetID)
	r.mu.Lock()
	if _, exists := r.slots[key]; exists {
		r.mu.Unlock()
		return fmt.Errorf("widget %q already registered", key)
	}
	slot := &widgetSlot{
		pluginID: pluginID,
		widgetID: widgetID,
		zone:     zone,
		priority: priority,
		provider: provider,
	}
	r.slots[key] = slot
	r.byZone[zone] = append(r.byZone[zone], slot)
	sort.Slice(r.byZone[zone], func(i, j int) bool {
		return r.byZone[zone][i].priority < r.byZone[zone][j].priority
	})
	r.mu.Unlock()
	log.WithField("plugin", pluginID).WithField("widget", widgetID).
		WithField("zone", zone).Debug("Widget registered")
	return nil
}

func (r *WidgetRegistry) Unregister(pluginID, widgetID string) {
	key := slotKey(pluginID, widgetID)
	r.mu.Lock()
	slot, exists := r.slots[key]
	if !exists {
		r.mu.Unlock()
		return
	}
	delete(r.slots, key)
	zoneSlots := r.byZone[slot.zone]
	for i, s := range zoneSlots {
		if s == slot {
			r.byZone[slot.zone] = append(zoneSlots[:i], zoneSlots[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
	log.WithField("plugin", pluginID).WithField("widget", widgetID).Debug("Widget unregistered")
}

func (r *WidgetRegistry) UnregisterAll(pluginID string) {
	r.mu.RLock()
	var toRemove []*widgetSlot
	for _, s := range r.slots {
		if s.pluginID == pluginID {
			toRemove = append(toRemove, s)
		}
	}
	r.mu.RUnlock()
	for _, s := range toRemove {
		r.Unregister(s.pluginID, s.widgetID)
	}
}

// SetDefaultRenderFn sets the render function used for push-based updates.
// Called by TUI at startup with a lipgloss-based function.
func (r *WidgetRegistry) SetDefaultRenderFn(fn RenderFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultRenderFn = fn
}

// RefreshWidget renders the widget and caches the result. If renderFn is nil,
// uses the default render function (set by TUI via SetDefaultRenderFn).
func (r *WidgetRegistry) RefreshWidget(pluginID, widgetID string, width int, renderFn RenderFunc) error {
	r.mu.RLock()
	key := slotKey(pluginID, widgetID)
	slot, exists := r.slots[key]
	defaultFn := r.defaultRenderFn
	r.mu.RUnlock()
	if !exists {
		return fmt.Errorf("widget %q not found", key)
	}
	fn := renderFn
	if fn == nil {
		fn = defaultFn
	}
	spans := SanitizeSpans(slot.provider.Render(width))
	if fn != nil {
		content := fn(spans, width)
		slot.content.Store(&content)
	} else {
		// No render function yet — store plain text
		text := joinWidgetSpans(spans)
		slot.content.Store(&text)
	}
	return nil
}

// RefreshAllWidgets re-renders all widgets (e.g. on resize).
func (r *WidgetRegistry) RefreshAllWidgets(width int, renderFn RenderFunc) {
	r.mu.RLock()
	slots := make([]*widgetSlot, 0, len(r.slots))
	for _, s := range r.slots {
		slots = append(slots, s)
	}
	fn := renderFn
	if fn == nil {
		fn = r.defaultRenderFn
	}
	r.mu.RUnlock()
	for _, s := range slots {
		spans := SanitizeSpans(s.provider.Render(width))
		if fn != nil {
			content := fn(spans, width)
			s.content.Store(&content)
		} else {
			text := joinWidgetSpans(spans)
			s.content.Store(&text)
		}
	}
}

// RenderZone returns pre-cached styled strings for a zone, joined with separators.
func (r *WidgetRegistry) RenderZone(zone string) string {
	r.mu.RLock()
	slots := r.byZone[zone]
	slotsCopy := make([]*widgetSlot, len(slots))
	copy(slotsCopy, slots)
	r.mu.RUnlock()
	if len(slotsCopy) == 0 {
		return ""
	}
	parts := make([]string, 0, len(slotsCopy))
	for _, s := range slotsCopy {
		if cached := s.content.Load(); cached != nil && *cached != "" {
			parts = append(parts, *cached)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return joinWidgetParts(parts)
}

func (r *WidgetRegistry) WidgetInfo() []WidgetInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var infos []WidgetInfo
	for _, s := range r.slots {
		infos = append(infos, WidgetInfo{
			PluginID: s.pluginID,
			WidgetID: s.widgetID,
			Zone:     s.zone,
			Priority: s.priority,
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].Zone != infos[j].Zone {
			return infos[i].Zone < infos[j].Zone
		}
		return infos[i].Priority < infos[j].Priority
	})
	return infos
}

type WidgetInfo struct {
	PluginID string `json:"pluginId"`
	WidgetID string `json:"widgetId"`
	Zone     string `json:"zone"`
	Priority int    `json:"priority"`
}

func (r *WidgetRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.slots)
}

func joinWidgetParts(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += "  " + p
	}
	return result
}

func joinWidgetSpans(spans []WidgetSpan) string {
	var s string
	for _, sp := range spans {
		s += sp.Text
	}
	return s
}
