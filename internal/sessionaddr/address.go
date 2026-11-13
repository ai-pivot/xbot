// Package sessionaddr owns the wire format of session addresses. Agent roles
// and call-chain paths are NOT session addresses: they omit root and instance.
package sessionaddr

import "strings"

const AgentChannel = "agent"

type Address struct {
	Channel string
	ChatID  string
}

// Key is the sole channel/chat serialization boundary. ChatID is opaque and may
// contain colons and slashes (CLI paths and nested interactive sessions).
func Key(channel, chatID string) string { return channel + ":" + chatID }

func Parse(key string) (Address, bool) {
	ch, id, ok := strings.Cut(key, ":")
	if !ok || ch == "" || id == "" || strings.ContainsAny(ch, "/\x00 \t\r\n") || strings.ContainsRune(id, '\x00') {
		return Address{}, false
	}
	return Address{Channel: ch, ChatID: id}, true
}

func (a Address) String() string { return Key(a.Channel, a.ChatID) }

// Agent qualifies a RAW interactive map key. It deliberately adds one layer:
// a nested map key can itself start with agent:, and dropping that layer would
// change the owner. Parsing a qualified address is a separate operation.
func Agent(mapKey string) string { return Key(AgentChannel, mapKey) }

func Child(channel, chatID, role, instance string) string {
	key := Key(channel, chatID) + "/" + role
	if instance != "" {
		key += ":" + instance
	}
	return key
}

func SplitChild(key string) (channel, chatID, role, instance string) {
	a, ok := Parse(key)
	if !ok {
		return
	}
	i := strings.LastIndex(a.ChatID, "/")
	if i < 1 || i == len(a.ChatID)-1 {
		return
	}
	channel, chatID = a.Channel, a.ChatID[:i]
	role, instance, _ = strings.Cut(a.ChatID[i+1:], ":")
	return
}

func IsSubAgent(agentID string) bool {
	id := strings.TrimSpace(agentID)
	return id != "" && id != "main"
}

// Scope is shared by background tasks and TODO readers/writers.
//
// An interactive session's own key (CallerSessionKey) is authoritative and is
// checked FIRST, before AgentID. Rationale (2026-11-13, live bug): a restored
// interactive session whose run config had degraded to a non-sub-agent AgentID
// still took the RootSessionKey branch, so tasks the CHILD created were
// registered on the PARENT session — the child's task silently became the
// parent's task. Identity labels are display/call-chain data; the owner key is
// physical. Never let a label outrank it.
func Scope(agentID, callerSessionKey, sessionKey, rootKey, channel, chatID string) string {
	if callerSessionKey != "" {
		return Agent(callerSessionKey)
	}
	if IsSubAgent(agentID) {
		if sessionKey != "" {
			return sessionKey
		}
		return agentID
	}
	if rootKey != "" {
		return rootKey
	}
	if sessionKey != "" {
		return sessionKey
	}
	if channel != "" && chatID != "" {
		return Key(channel, chatID)
	}
	return ""
}

// DegradedIdentity reports whether a config still describes itself as the main
// agent even though an interactive owner key is present — the exact shape that
// leaked child task completions into the parent session.
func DegradedIdentity(agentID, callerSessionKey string) bool {
	return callerSessionKey != "" && !IsSubAgent(agentID)
}

// IsChildKey and Context are compatibility-boundary helpers for stored agent
// mirror addresses. New task/report routes already carry explicit exact keys.
func IsChildKey(key string) bool {
	rest := strings.TrimPrefix(key, "agent:")
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return false
	}
	ch, chat, ok := strings.Cut(rest[:slash], ":")
	return ok && ch != "" && chat != ""
}

func MirrorTarget(chatID string) (string, bool) {
	a, ok := Parse(chatID)
	if !ok || IsChildKey(chatID) {
		return "", false
	}
	return a.String(), true
}

func Context(channel, chatID string) (string, bool) {
	if channel == "" || chatID == "" {
		return "", false
	}
	if channel == AgentChannel {
		if target, ok := MirrorTarget(chatID); ok {
			return target, true
		}
	}
	return Key(channel, chatID), true
}

func NormalizeStoredParent(key string) string {
	for i := 0; i < 4; i++ {
		a, ok := Parse(key)
		if !ok || a.Channel != AgentChannel {
			break
		}
		next, mirror := MirrorTarget(a.ChatID)
		if !mirror {
			break
		}
		key = next
	}
	return key
}
