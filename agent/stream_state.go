package agent

import (
	"sync/atomic"
	"xbot/protocol"
)

// updateStreamState atomically updates the live stream content for a chat.
// Called by stream callbacks (streamContentFunc etc.) on every LLM token chunk.
// Thread-safe via CAS loop on atomic.Pointer.
func (a *Agent) updateStreamState(key string, fn func(s *protocol.ProgressEvent)) {
	val, _ := a.streamState.LoadOrStore(key, &atomic.Pointer[protocol.ProgressEvent]{})
	ap := val.(*atomic.Pointer[protocol.ProgressEvent])
	for {
		old := ap.Load()
		newE := &protocol.ProgressEvent{}
		if old != nil {
			*newE = *old
		}
		fn(newE)
		if ap.CompareAndSwap(old, newE) {
			return
		}
	}
}

// clearStreamState removes the live stream state for a chat.
// Called when a structured progress event arrives (the structured snapshot
// is authoritative — stream fields are no longer needed).
func (a *Agent) clearStreamState(key string) {
	a.streamState.Delete(key)
}

// mergeStreamState merges live stream fields into a progress snapshot.
// Called by GetActiveProgress to include live streaming content in the
// returned snapshot — the client reads this via tick pull.
func (a *Agent) mergeStreamState(key string, result *protocol.ProgressEvent) {
	val, ok := a.streamState.Load(key)
	if !ok {
		return
	}
	ap := val.(*atomic.Pointer[protocol.ProgressEvent])
	ss := ap.Load()
	if ss == nil {
		return
	}
	// Only fill fields that are empty in the structured snapshot —
	// structured state is authoritative when present.
	if result.StreamContent == "" && ss.StreamContent != "" {
		result.StreamContent = ss.StreamContent
	}
	if result.ReasoningStreamContent == "" && ss.ReasoningStreamContent != "" {
		result.ReasoningStreamContent = ss.ReasoningStreamContent
	}
	if len(result.StreamingTools) == 0 && len(ss.StreamingTools) > 0 {
		result.StreamingTools = ss.StreamingTools
	}
	if result.StreamTokens == 0 && ss.StreamTokens > 0 {
		result.StreamTokens = ss.StreamTokens
	}
}
