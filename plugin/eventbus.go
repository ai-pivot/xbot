package plugin

import (
	"context"
	"fmt"
	"reflect"
	"sync"
)

// PluginEventHandler is a function that handles events published to the bus.
type PluginEventHandler func(ctx context.Context, topic string, data any) error

// PluginEventBus is an in-process publish/subscribe event bus for plugins.
// It supports subscribe, publish (with panic recovery per handler), and unsubscribe.
type PluginEventBus struct {
	mu       sync.RWMutex
	handlers map[string][]PluginEventHandler
}

// NewPluginEventBus creates a new PluginEventBus.
func NewPluginEventBus() *PluginEventBus {
	return &PluginEventBus{
		handlers: make(map[string][]PluginEventHandler),
	}
}

// Subscribe registers a handler for the given topic.
// Returns an error if handler is nil.
func (b *PluginEventBus) Subscribe(topic string, handler PluginEventHandler) error {
	if handler == nil {
		return fmt.Errorf("event bus: handler must not be nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[topic] = append(b.handlers[topic], handler)
	return nil
}

// Publish sends data to all handlers subscribed to the topic.
// Uses copy-on-read pattern so handlers can safely subscribe/unsubscribe during iteration.
// Each handler invocation is wrapped in panic recovery.
// Returns a slice of errors from all handlers (including recovered panics).
func (b *PluginEventBus) Publish(ctx context.Context, topic string, data any) []error {
	b.mu.RLock()
	handlers := make([]PluginEventHandler, len(b.handlers[topic]))
	copy(handlers, b.handlers[topic])
	b.mu.RUnlock()

	var errs []error
	for _, handler := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					errs = append(errs, fmt.Errorf("event bus: handler panic: %v", r))
				}
			}()
			if err := handler(ctx, topic, data); err != nil {
				errs = append(errs, err)
			}
		}()
	}
	return errs
}

// Unsubscribe removes a handler from the given topic.
// Uses function pointer comparison. If the topic has no remaining handlers, the entry is deleted.
func (b *PluginEventBus) Unsubscribe(topic string, handler PluginEventHandler) error {
	if handler == nil {
		return fmt.Errorf("event bus: handler must not be nil")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	handlers, ok := b.handlers[topic]
	if !ok {
		return fmt.Errorf("event bus: no handlers for topic %q", topic)
	}

	for i, h := range handlers {
		if funcEqual(h, handler) {
			b.handlers[topic] = append(handlers[:i], handlers[i+1:]...)
			if len(b.handlers[topic]) == 0 {
				delete(b.handlers, topic)
			}
			return nil
		}
	}

	return fmt.Errorf("event bus: handler not found for topic %q", topic)
}

// funcEqual compares two function values for identity.
func funcEqual(a, b PluginEventHandler) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}
