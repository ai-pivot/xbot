package plugin

import "sync"

// WebWidgetSpan is a structured styled text segment for web rendering.
// Unlike the ANSI-oriented WidgetSpan, it carries semantic fields that the
// web frontend maps to Tailwind classes / icons directly.
//
// Style values match StyleClass constants (normal|dim|accent|success|warning|
// error|info|muted). "raw" style is intentionally NOT transmitted to the web
// channel (raw content may contain terminal escape sequences).
type WebWidgetSpan struct {
	Text  string `json:"text"`
	Style string `json:"style,omitempty"`
	Icon  string `json:"icon,omitempty"`
	Href  string `json:"href,omitempty"`
}

// WebWidgetZones maps a widget zone name to its structured spans.
type WebWidgetZones map[string][]WebWidgetSpan

// RenderSessionWebWidgets renders all widget zones for a session using its CWD,
// returning structured spans instead of ANSI strings. This is the web-channel
// counterpart of RenderSessionWidgets (which renders ANSI for CLI/TUI).
//
// The getCWD callback resolves the working directory for the given chatID.
func RenderSessionWebWidgets(wr *WidgetRegistry, getCWD func(string) string, chatID string) WebWidgetZones {
	cwd := ""
	if getCWD != nil {
		cwd = getCWD(chatID)
	}
	zones := make(WebWidgetZones)
	for _, z := range []string{"titleBarLeft", "titleBarRight", "statusBarLeft", "statusBarRight", "infoBar", "footer", "toolHint"} {
		zones[z] = wr.RenderZoneForWorkDirWeb(z, cwd)
	}
	return zones
}

// RenderZoneForWorkDirWeb returns structured spans for a zone rendered against a
// specific workDir WITHOUT modifying the shared PluginContext. Providers that
// implement WorkDirRenderer get the workDir passed directly; others fall back
// to the global pctx (same semantics as RenderZoneForWorkDir).
func (r *WidgetRegistry) RenderZoneForWorkDirWeb(zone, workDir string) []WebWidgetSpan {
	r.mu.RLock()
	slots := r.byZone[zone]
	slotsCopy := make([]*widgetSlot, len(slots))
	copy(slotsCopy, slots)
	r.mu.RUnlock()
	if len(slotsCopy) == 0 {
		return nil
	}
	var out []WebWidgetSpan
	for _, s := range slotsCopy {
		var spans []WidgetSpan
		if wdr, ok := s.provider.(WorkDirRenderer); ok {
			spans = SanitizeSpans(wdr.RenderForWorkDir(0, workDir))
		} else {
			spans = SanitizeSpans(s.provider.Render(0))
		}
		for _, sp := range spans {
			if sp.Style == StyleRaw {
				continue // raw ANSI content is not safe for web rendering
			}
			out = append(out, WebWidgetSpan{
				Text:  sp.Text,
				Style: string(sp.Style),
			})
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Web UI component declarations (web_ui protocol)
// ---------------------------------------------------------------------------

// WebComponent is a declarative web UI component (type + props). The `type`
// field names a component in the frontend registry (badge|progress|metric|
// sparkline|table|list|markdown|code|custom). Unknown types degrade to a
// markdown/badge fallback on the frontend.
type WebComponent struct {
	Type  string         `json:"type"`
	Props map[string]any `json:"props,omitempty"`
}

// validWebSlots lists the web UI layout slots accepted from plugins.
var validWebSlots = map[string]bool{
	"title_bar_left":   true,
	"title_bar_right":  true,
	"status_bar_left":  true,
	"status_bar_right": true,
	"info_bar":         true,
	"right_sidebar":    true,
	"panel":            true,
	"tool_hint":        true,
}

// WebUIComponent is a web UI declaration from a channel plugin.
//
// Exactly one of Component / Code / Src should be set:
//   - Component: declarative component (type + props).
//   - Code:      free-form TSX/JS source compiled in an iframe sandbox.
//   - Src:       external URL rendered in a sandboxed iframe.
type WebUIComponent struct {
	WidgetID  string        `json:"widget_id"`
	Title     string        `json:"title,omitempty"`
	Slot      string        `json:"slot"`
	Refresh   string        `json:"refresh,omitempty"`
	Triggers  []string      `json:"triggers,omitempty"`
	Component *WebComponent `json:"component,omitempty"`
	Code      string        `json:"code,omitempty"`
	Src       string        `json:"src,omitempty"`

	// owner is the channel name that declared this component (not serialized).
	owner string `json:"-"`
}

// WebUIRegistry stores web UI component declarations per channel plugin.
// Thread-safe. Channel plugins declare components via the "web_ui" protocol
// message; each declaration replaces the previous set for that channel
// (hot-update semantics, mirroring channel_tools / channel_prompt).
type WebUIRegistry struct {
	mu         sync.RWMutex
	components map[string]*WebUIComponent // key: widgetID
}

// NewWebUIRegistry creates an empty web UI registry.
func NewWebUIRegistry() *WebUIRegistry {
	return &WebUIRegistry{components: make(map[string]*WebUIComponent)}
}

// SetChannel replaces all web UI declarations for a channel (hot-update).
func (r *WebUIRegistry) SetChannel(channel string, decls []WebUIComponent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Remove existing declarations owned by this channel.
	for id, c := range r.components {
		if c.owner == channel {
			delete(r.components, id)
		}
	}
	for i := range decls {
		c := &decls[i]
		if c.WidgetID == "" {
			continue
		}
		if !validWebSlots[c.Slot] {
			continue
		}
		cp := *c
		cp.owner = channel
		r.components[cp.WidgetID] = &cp
	}
}

// RemoveChannel removes all declarations owned by a channel.
func (r *WebUIRegistry) RemoveChannel(channel string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.components {
		if c.owner == channel {
			delete(r.components, id)
		}
	}
}

// Components returns a snapshot of all declarations sorted by widgetID.
func (r *WebUIRegistry) Components() []WebUIComponent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]WebUIComponent, 0, len(r.components))
	for _, c := range r.components {
		out = append(out, *c)
	}
	return out
}

// Count returns the number of registered components.
func (r *WebUIRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.components)
}

// Owner returns the channel name that declared the given widgetID.
func (r *WebUIRegistry) Owner(widgetID string) (channel string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, exists := r.components[widgetID]
	if !exists {
		return "", false
	}
	return c.owner, true
}
