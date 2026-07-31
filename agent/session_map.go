package agent

import "sync"

// SessionMap is a type-safe, concurrency-safe map keyed by "channel:chatID".
// It eliminates the type-assertion risk of bare sync.Map fields on Agent.
type SessionMap[T any] struct {
	m sync.Map
}

func (sm *SessionMap[T]) Load(channel, chatID string) (T, bool) {
	v, ok := sm.m.Load(channel + ":" + chatID)
	if !ok {
		var zero T
		return zero, false
	}
	return v.(T), true
}

func (sm *SessionMap[T]) Store(channel, chatID string, val T) {
	sm.m.Store(channel+":"+chatID, val)
}

func (sm *SessionMap[T]) Delete(channel, chatID string) {
	sm.m.Delete(channel + ":" + chatID)
}

func (sm *SessionMap[T]) LoadAndDelete(channel, chatID string) (T, bool) {
	v, loaded := sm.m.LoadAndDelete(channel + ":" + chatID)
	if !loaded {
		var zero T
		return zero, false
	}
	return v.(T), true
}

func (sm *SessionMap[T]) Range(fn func(channel, chatID string, val T) bool) {
	sm.m.Range(func(k, v any) bool {
		key := k.(string)
		// Split on first ":"
		idx := -1
		for i := 0; i < len(key); i++ {
			if key[i] == ':' {
				idx = i
				break
			}
		}
		if idx < 0 {
			return true
		}
		return fn(key[:idx], key[idx+1:], v.(T))
	})
}
