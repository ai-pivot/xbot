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

// ─── 单一归一化入口 ────────────────────────────────────────────────
//
// 全会话 key 只有两种合法形态：
//   1. 物理地址      `<channel>:<chatID>`           例: web:web-4 / cli:/home/x/repo
//   2. 交互子代理 key `<channel>:<chatID>/<role>:<instance>`  例: web:web-4/court.shoufu:main
// 子代理在投递/存储时的 channel 是 "agent"，chatID 就是交互 key 本身。
// `agent:` 只是投递层前缀，**不是 key 的一部分**；重复层属历史脏数据。
//
// 所有归一化必须走这里，禁止各包自行 TrimPrefix/Cut/Split。

// TrimAgentLayers 去掉投递层前缀 `agent:`（最多 3 层，防历史双前缀）。
func TrimAgentLayers(key string) string {
	k := strings.TrimSpace(key)
	for i := 0; i < 3; i++ {
		next, ok := strings.CutPrefix(k, AgentChannel+":")
		if !ok {
			break
		}
		k = next
		if k == "" {
			break
		}
	}
	return k
}

// Normalize 把任意历史形态的 key 归一到"物理地址或交互 key"（不带 agent: 前缀）。
func Normalize(key string) string { return TrimAgentLayers(key) }

// Physical 把 key 拆成投递/存储用的 (channel, chatID)。
//
// 交互子代理 key（含 `/`）一律投到 agent 通道，chatID 保留完整 key；其余按
// `<channel>:<chatID>` 解析。`agent:` 层只剥投递前缀，不参与 chatID。
func Physical(key string) (channel, chatID string, ok bool) {
	raw := strings.TrimSpace(key)
	if raw == "" {
		return "", "", false
	}
	trimmed := TrimAgentLayers(raw)
	if trimmed != raw {
		// 显式 agent 前缀 ⇒ 子代理目标，chatID 就是去掉前缀后的 key。
		// 剥完必须仍是合法地址（`agent:` / `agent:agent:` 这类残留一律拒绝）。
		if trimmed == "" {
			return "", "", false
		}
		if _, ok := Parse(trimmed); !ok && !IsChildKey(trimmed) {
			return "", "", false
		}
		return AgentChannel, trimmed, true
	}
	if IsChildKey(trimmed) {
		return AgentChannel, trimmed, true
	}
	if a, ok := Parse(trimmed); ok {
		return a.Channel, a.ChatID, true
	}
	return "", "", false
}

// AgentAddress 返回逻辑代理地址 `agent:<role>/<instance>`；输入可带/不带 agent: 前缀。
// 空输入返回空串（主会话没有代理地址）。
func AgentAddress(nameOrID string) string {
	n := TrimAgentLayers(nameOrID)
	if n == "" {
		return ""
	}
	return AgentChannel + ":" + n
}
