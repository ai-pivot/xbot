package agent

import (
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// Tool call identity (STATE-MODEL P2 / P3′ / C-8)
// ---------------------------------------------------------------------------
//
// Tool identity must be stable for the WHOLE life of a tool call, including the
// "still generating arguments" phase. Before this file, a generating tool had
// NO call id at all (only name + label, and the label grows while arguments
// stream), which forced every client to invent a fallback key like
// `name` / `name#label` — two consecutive same-named shells then killed each
// other in the UI (R1 in the model document).
//
// The server now assigns the id BEFORE the tool first appears in any frame:
//
//	call-<turn>-<iteration>-<index>
//
// Deterministic in (turn, iteration, index), so every frame of the same tool
// call — generating, pending, running, done — carries exactly the same value.
// The provider's id (the model's own tool_call id) is carried as a FIELD
// (protocol.ToolProgress.ProviderCallID) and never used as identity, because it
// arrives late (fragments) or not at all.
//
// The scope is per chat: when a turn/iteration advances, the previous mapping is
// dropped so the map stays bounded (one small map per active chat).
type toolIdentity struct {
	mu     sync.Mutex
	scopes map[string]*toolIDScope
	// chatOrd assigns each chat a process-unique ordinal so ids can never
	// collide across chats (contract C-8: "opaque unique id, never reused").
	chatOrd    uint64
	chatOrdFor map[string]uint64
}

type toolIDScope struct {
	turnID  uint64
	iter    int
	chatOrd uint64
	ids     map[int]string
}

func newToolIdentity() *toolIdentity {
	return &toolIdentity{
		scopes:     make(map[string]*toolIDScope),
		chatOrdFor: make(map[string]uint64),
	}
}

// ID returns the stable server-assigned call id for the tool at (turn,
// iteration, index) of a chat, assigning it on first use.
func (t *toolIdentity) ID(chatKey string, turnID uint64, iteration, index int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.scopes == nil {
		t.scopes = make(map[string]*toolIDScope)
	}
	if t.chatOrdFor == nil {
		t.chatOrdFor = make(map[string]uint64)
	}
	ord, ok := t.chatOrdFor[chatKey]
	if !ok {
		t.chatOrd++
		ord = t.chatOrd
		t.chatOrdFor[chatKey] = ord
	}
	sc, ok := t.scopes[chatKey]
	if !ok || sc.turnID != turnID || sc.iter != iteration {
		// Iteration/turn advanced: the previous index→id mapping is dead
		// (indices restart at 0 for every LLM call). Replacing the scope also
		// bounds memory to one map per active chat.
		sc = &toolIDScope{turnID: turnID, iter: iteration, chatOrd: ord, ids: make(map[int]string)}
		t.scopes[chatKey] = sc
	}
	if id, ok := sc.ids[index]; ok {
		return id
	}
	id := fmt.Sprintf("call-%d-%d-%d-%d", ord, turnID, iteration, index)
	sc.ids[index] = id
	return id
}
