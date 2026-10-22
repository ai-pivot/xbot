package agent

import (
	"fmt"
	"strings"
)

// structured_route.go —— 结构化消息的**地址解析**（逻辑地址 → 物理会话 key）。
//
// 背景（2026-10-22 实测事故）：SendMessage 的地址 `agent:tester/court-verify` 是
// **逻辑地址**（Dispatcher 用），而子代理真正的会话 key 是
// interactiveKey(父channel, 父chatID, role, instance) =
// `web_web-4/tester:court-verify`。之前直接把逻辑地址当 chatID 传给
// GetOrCreateSession，于是每次批红都会**新建一个空壳会话**把消息写进去——
// 实测：`agent:1a/tester_court-verify-*.db` 里躺着 report+decision 两行，
// 而工部真实的会话一条都收不到。批红等于凭空蒸发。
//
// 因此这里做严格解析：内存注册表 → 持久化注册表（parent_channel/parent_chat_id
// + role + instance 重建）；解析不到就返回空串，由调用方**直接报错**，
// 绝不退回逻辑地址去建野会话。

// ResolveInteractiveChatID 把逻辑地址 "role/instance" 解析成子代理的实际会话 chatID。
// 找不到返回 ""（不猜、不新建）。
func (a *Agent) ResolveInteractiveChatID(roleInstance string) string {
	role, instance, ok := strings.Cut(roleInstance, "/")
	if !ok || role == "" {
		return ""
	}

	// 1) 内存里活着的实例最权威。
	found := ""
	a.interactiveSubAgents.Range(func(k, v any) bool {
		ia, isIA := v.(*interactiveAgent)
		if !isIA || ia == nil {
			return true
		}
		if ia.roleName == role && ia.instance == instance {
			found, _ = k.(string)
			return false
		}
		return true
	})
	if found != "" {
		return found
	}

	// 2) 进程重启后内存空了：用持久化注册表重建物理 key。
	if a.multiSession == nil {
		return ""
	}
	rec, err := a.interactiveAgentSvc().Get("agent:" + role + "/" + instance)
	if err != nil || rec == nil {
		return ""
	}
	if rec.ParentChannel == "" || rec.ParentChatID == "" {
		// 老版本写进来的空父键记录（2026-10-21 之前），无法重建。
		return ""
	}
	return interactiveKey(rec.ParentChannel, rec.ParentChatID, rec.RoleName, rec.Instance)
}

// ResolveStructuredTarget 解析结构化消息的目标：channel=="agent" 且 chatID 形如
// "role/instance" 时映射到物理会话；不是 agent 地址则原样返回。
func (a *Agent) ResolveStructuredTarget(channel, chatID string) (string, error) {
	if channel != "agent" {
		return chatID, nil
	}
	if !strings.Contains(chatID, "/") {
		return chatID, nil
	}
	// chatID 已经是物理 key（含 ":"，形如 web_web-4/tester:court-verify）时直接放行。
	if strings.Contains(chatID, ":") {
		return chatID, nil
	}
	real := a.ResolveInteractiveChatID(chatID)
	if real == "" {
		return "", fmt.Errorf("sub-agent %q is not registered (no in-memory instance and no registry row with parent) — refusing to create a stray session; spawn it first or address session:<channel>:<chatID>", chatID)
	}
	return real, nil
}
