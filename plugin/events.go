package plugin

import (
	"fmt"
	"reflect"
	"sync"
	"time"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Plugin Event Notifier — lifecycle state change notification system
// ---------------------------------------------------------------------------

// PluginEventType represents the type of plugin lifecycle event.
type PluginEventType string

const (
	PluginEventActivated   PluginEventType = "activated"
	PluginEventDeactivated PluginEventType = "deactivated"
	PluginEventInstalled   PluginEventType = "installed"
	PluginEventUninstalled PluginEventType = "uninstalled"
	PluginEventReloaded    PluginEventType = "reloaded"
	PluginEventError       PluginEventType = "error"
)

// PluginEvent represents a single plugin lifecycle event.
type PluginEvent struct {
	Type      PluginEventType
	PluginID  string
	Timestamp time.Time
	Error     error
	Data      any // optional; recommended: map[string]any
}

// PluginEventCallback is a function that receives plugin lifecycle events.
type PluginEventCallback func(event PluginEvent)

// PluginEventNotifier manages plugin state change notifications.
// It is a lightweight, topic-free mechanism for external consumers (CLI, channels)
// that need lifecycle notifications — distinct from the topic-based PluginEventBus
// used for plugin-to-plugin communication.
type PluginEventNotifier struct {
	mu        sync.RWMutex
	callbacks []PluginEventCallback
}

// NewPluginEventNotifier creates a new PluginEventNotifier.
func NewPluginEventNotifier() *PluginEventNotifier {
	return &PluginEventNotifier{
		callbacks: make([]PluginEventCallback, 0),
	}
}

// Subscribe registers a callback for plugin lifecycle events.
// Returns an error if callback is nil.
func (n *PluginEventNotifier) Subscribe(callback PluginEventCallback) error {
	if callback == nil {
		return fmt.Errorf("event notifier: callback must not be nil")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.callbacks = append(n.callbacks, callback)
	return nil
}

// Unsubscribe removes a previously registered callback.
// Uses function pointer comparison (reflect.ValueOf.Pointer).
// Returns an error if callback is nil or not found.
func (n *PluginEventNotifier) Unsubscribe(callback PluginEventCallback) error {
	if callback == nil {
		return fmt.Errorf("event notifier: callback must not be nil")
	}
	n.mu.Lock()
	defer n.mu.Unlock()

	for i, cb := range n.callbacks {
		if reflect.ValueOf(cb).Pointer() == reflect.ValueOf(callback).Pointer() {
			n.callbacks = append(n.callbacks[:i], n.callbacks[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("event notifier: callback not found")
}

// Notify sends an event to all registered callbacks.
// Uses copy-on-read pattern so callbacks can safely subscribe/unsubscribe during iteration.
// Each callback invocation is wrapped in panic recovery — a panicking callback
// does not affect other callbacks or the caller.
func (n *PluginEventNotifier) Notify(event PluginEvent) {
	n.mu.RLock()
	callbacks := make([]PluginEventCallback, len(n.callbacks))
	copy(callbacks, n.callbacks)
	n.mu.RUnlock()

	for _, cb := range callbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Glob(log.CatPlugin).WithField("plugin", event.PluginID).
						WithField("event_type", string(event.Type)).
						Warn("Plugin lifecycle callback panicked: ", r)
				}
			}()
			cb(event)
		}()
	}
}
