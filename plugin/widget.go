package plugin

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "xbot/logger"
)

// RenderFunc converts WidgetSpan slices to a styled string for display.
type RenderFunc func(spans []WidgetSpan, width int) string

// WorkDirRenderer is an optional interface for widget providers that can render
// for a specific working directory without modifying the shared PluginContext.
// Used by the plugin_widgets RPC to avoid cross-session workDir races.
type WorkDirRenderer interface {
	RenderForWorkDir(width int, workDir string) []WidgetSpan
}

// WidgetRegistry manages all plugin UI widget contributions. Thread-safe.
type WidgetRegistry struct {
	mu              sync.RWMutex
	slots           map[string]*widgetSlot
	byZone          map[string][]*widgetSlot
	defaultRenderFn RenderFunc
	onUpdated       func() // called after widget content update

	// Debounce: coalesce rapid widget updates into a single notification.
	debounceMu    sync.Mutex
	debounceTimer *time.Timer
	debounceDur   time.Duration // 0 = no debounce (immediate notify)
}

// SetDebounce configures the debounce interval for OnUpdated notifications.
// Multiple rapid updates within this interval are coalesced into a single
// notification. Pass 0 to disable debounce (default behavior).
func (r *WidgetRegistry) SetDebounce(d time.Duration) {
	r.mu.Lock()
	r.debounceDur = d
	r.mu.Unlock()
}

// NotifyUpdated triggers the OnUpdated callback (with debounce if configured)
// without modifying any slot cache. Use this when the underlying data source
// has changed but the global cache should not be written (e.g. per-workDir
// output cache in script plugins).
func (r *WidgetRegistry) NotifyUpdated() {
	r.mu.RLock()
	fn := r.onUpdated
	dur := r.debounceDur
	r.mu.RUnlock()
	log.Infof("[NotifyUpdated] fn=%v debounce=%v", fn != nil, dur)
	r.notifyUpdated()
}

// FireUpdated immediately fires the onUpdated callback WITHOUT debounce.
// Use this after direct CWD changes (e.g. Cd) where widget content was already
// regenerated via RefreshWorkDir → OnWorkDirChanged and should be pushed now.
func (r *WidgetRegistry) FireUpdated() {
	r.mu.RLock()
	fn := r.onUpdated
	r.mu.RUnlock()
	if fn != nil {
		fn()
	}
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

// BasicANSIRender maps StyleClass to simple ANSI escape codes.
// Used as the default render function for server-side widget rendering
// (e.g., plugin_widgets RPC). The TUI overrides this with lipgloss rendering
// via SetDefaultRenderFn in CLIChannel.
func BasicANSIRender(spans []WidgetSpan, _ int) string {
	var sb strings.Builder
	for _, sp := range spans {
		if sp.Style == StyleRaw {
			// Raw pass-through: text contains its own ANSI escapes (e.g. diff output)
			sb.WriteString(sp.Text)
			continue
		}
		color := ""
		switch sp.Style {
		case StyleDim:
			color = "\033[2m" // dim
		case StyleAccent:
			color = "\033[36m" // cyan
		case StyleSuccess:
			color = "\033[32m" // green
		case StyleWarning:
			color = "\033[33m" // yellow
		case StyleError:
			color = "\033[31m" // red
		case StyleInfo:
			color = "\033[34m" // blue
		case StyleMuted:
			color = "\033[37;2m" // white dim
		default:
			color = "\033[0m"
		}
		sb.WriteString(color)
		sb.WriteString(sp.Text)
		sb.WriteString("\033[0m")
	}
	return sb.String()
}

// OnUpdated sets a callback that fires after any widget content is updated.
func (r *WidgetRegistry) OnUpdated(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onUpdated = fn
}

func (r *WidgetRegistry) notifyUpdated() {
	r.mu.RLock()
	fn := r.onUpdated
	dur := r.debounceDur
	r.mu.RUnlock()
	log.Infof("[notifyUpdated] fn=%v debounce=%v", fn != nil, dur)
	if fn == nil {
		return
	}

	if dur <= 0 {
		// No debounce — fire immediately
		log.Infof("[notifyUpdated] firing immediately")
		fn()
		return
	}

	// Debounce: reset timer, fire callback after dur of silence
	r.debounceMu.Lock()
	if r.debounceTimer != nil {
		r.debounceTimer.Stop()
	}
	r.debounceTimer = time.AfterFunc(dur, func() {
		log.Infof("[notifyUpdated] debounce fired after %v", dur)
		fn()
	})
	r.debounceMu.Unlock()
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

func (r *WidgetRegistry) SetDefaultRenderFn(fn RenderFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultRenderFn = fn
}

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
		text := joinWidgetSpans(spans)
		slot.content.Store(&text)
	}
	r.notifyUpdated()
	return nil
}

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
	r.notifyUpdated()
}

func (r *WidgetRegistry) RenderZone(zone string) string {
	return r.renderZone(zone, true)
}

// RenderZoneForContext renders zone content by calling providers' Render()
// directly, bypassing the global slot cache. Each call re-renders from the
// provider, which reads pctx.WorkingDir() for per-session output.
func (r *WidgetRegistry) RenderZoneForContext(zone string) string {
	return r.renderZone(zone, false)
}

func (r *WidgetRegistry) renderZone(zone string, useCache bool) string {
	r.mu.RLock()
	slots := r.byZone[zone]
	slotsCopy := make([]*widgetSlot, len(slots))
	copy(slotsCopy, slots)
	fn := r.defaultRenderFn
	r.mu.RUnlock()
	if len(slotsCopy) == 0 {
		return ""
	}
	parts := make([]string, 0, len(slotsCopy))
	for _, s := range slotsCopy {
		if useCache {
			if cached := s.content.Load(); cached != nil && *cached != "" {
				parts = append(parts, *cached)
			}
		} else {
			// Render on-the-fly from provider
			spans := SanitizeSpans(s.provider.Render(0))
			if fn != nil {
				parts = append(parts, fn(spans, 0))
			} else {
				parts = append(parts, BasicANSIRender(spans, 0))
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return joinWidgetParts(parts)
}

// RenderZoneForWorkDir renders zone content for a specific workDir WITHOUT
// modifying the shared PluginContext. Providers that implement WorkDirRenderer
// get the workDir passed directly. Others fall back to the global pctx.
func (r *WidgetRegistry) RenderZoneForWorkDir(zone, workDir string) string {
	r.mu.RLock()
	slots := r.byZone[zone]
	slotsCopy := make([]*widgetSlot, len(slots))
	copy(slotsCopy, slots)
	fn := r.defaultRenderFn
	r.mu.RUnlock()
	if len(slotsCopy) == 0 {
		return ""
	}
	parts := make([]string, 0, len(slotsCopy))
	for _, s := range slotsCopy {
		var spans []WidgetSpan
		if wdr, ok := s.provider.(WorkDirRenderer); ok {
			spans = SanitizeSpans(wdr.RenderForWorkDir(0, workDir))
		} else {
			spans = SanitizeSpans(s.provider.Render(0))
		}
		if fn != nil {
			parts = append(parts, fn(spans, 0))
		} else {
			parts = append(parts, BasicANSIRender(spans, 0))
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
