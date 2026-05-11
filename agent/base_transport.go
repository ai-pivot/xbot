package agent

import (
	"context"
	"encoding/json"
	"sync"

	log "xbot/logger"
	"xbot/protocol"
)

// subscription tracks a single event subscription.
type subscription struct {
	pattern protocol.EventPattern
	handler protocol.EventHandler
}

// baseTransport provides shared emit/dispatch/Subscribe implementation
// for both local and remote transports.
type baseTransport struct {
	mu           sync.RWMutex
	subs         map[string][]*subscription
	wildcardSubs []*subscription
}

func newBaseTransport() baseTransport {
	return baseTransport{subs: make(map[string][]*subscription)}
}

// Subscribe registers an event handler matching the given pattern.
// Returns a cancel function to unsubscribe.
func (t *baseTransport) Subscribe(pattern protocol.EventPattern, handler protocol.EventHandler) (cancel func()) {
	t.mu.Lock()
	defer t.mu.Unlock()

	sub := &subscription{pattern: pattern, handler: handler}
	if pattern.Type == "" {
		t.wildcardSubs = append(t.wildcardSubs, sub)
	} else {
		t.subs[pattern.Type] = append(t.subs[pattern.Type], sub)
	}

	var once sync.Once
	return func() { once.Do(func() { t.unsubscribe(sub) }) }
}

// unsubscribe removes a subscription by pointer identity.
func (t *baseTransport) unsubscribe(target *subscription) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if target.pattern.Type == "" {
		for i, s := range t.wildcardSubs {
			if s == target {
				t.wildcardSubs = append(t.wildcardSubs[:i], t.wildcardSubs[i+1:]...)
				return
			}
		}
	} else {
		list := t.subs[target.pattern.Type]
		for i, s := range list {
			if s == target {
				t.subs[target.pattern.Type] = append(list[:i], list[i+1:]...)
				return
			}
		}
	}
}

// emit serializes a TransportEvent and dispatches it to matching subscribers.
func (t *baseTransport) emit(ctx context.Context, event protocol.TransportEvent) {
	payload, err := json.Marshal(event)
	if err != nil {
		log.WithError(err).WithField("event_type", event.EventType()).Warn("emit: marshal failed, dropping event")
		return
	}
	env := protocol.EventEnvelope{
		Type:    event.EventType(),
		Version: event.EventVersion(),
		Payload: payload,
	}
	t.dispatch(env)
}

// dispatch delivers an EventEnvelope to all matching subscribers.
func (t *baseTransport) dispatch(env protocol.EventEnvelope) {
	t.mu.RLock()
	// Snapshot matching subscribers to avoid holding RLock during handler calls.
	var matched []*subscription
	for _, sub := range t.subs[env.Type] {
		if sub.pattern.Matches(env.Type, env.Version) {
			matched = append(matched, sub)
		}
	}
	for _, sub := range t.wildcardSubs {
		if sub.pattern.Matches(env.Type, env.Version) {
			matched = append(matched, sub)
		}
	}
	t.mu.RUnlock()

	for _, sub := range matched {
		sub.handler(env)
	}
}
