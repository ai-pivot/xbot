package agent

import "strings"

// fatherParentKey 解析 interactive session key 的**直接父会话** key，
// 供 SendMessage(to="father") 使用（tools 侧经 ToolContext.FatherSessionKeyFn 注入）。
//
// 解析本体**复用 parseInteractiveKeyParent**（agent/interactive.go）——
// 不在这里写第二套 key 解析，否则两套实现必然漂移。
//
// 为什么还要在它外面加两道**合法性**闸门：parseInteractiveKeyParent 是宽松的，
// 它只要求"第一个冒号在最后一个斜杠之前"，于是**顶层 CLI 会话**的物理 key
// "cli:/home/smith/src/xbot" 会被它解成 channel="cli"、chatID="/home/smith/src"
// —— 那是另一个用户的会话（同级会话）。to="father" 若照此投递就是**静默误投**，
// 直接违背"顶层会话必须明确报错、绝不静默降级"。
//
// 闸门（都是形状校验，不是解析）：
//  1. 尾段必须是 "<role>:<instance>" 形态（含冒号且 role 非空）—— interactive key
//     恒由 resolveInteractiveSessionKey 拼成这个形状，普通会话 key 不会；
//  2. parseInteractiveKeyParent 解出的 channel / chatID 都必须非空。
//
// 返回值 ok=false 表示"没有父会话"（顶层会话 / 非 interactive key），
// 调用方必须报错，不许降级成 IM。
func fatherParentKey(sessionKey string) (string, bool) {
	key := strings.TrimSpace(sessionKey)
	if key == "" {
		return "", false
	}
	slashIdx := strings.LastIndex(key, "/")
	if slashIdx <= 0 || slashIdx == len(key)-1 {
		return "", false
	}
	tail := key[slashIdx+1:] // "<role>:<instance>"
	colonIdx := strings.Index(tail, ":")
	if colonIdx <= 0 {
		return "", false
	}
	channel, chatID := parseInteractiveKeyParent(key)
	if channel == "" || chatID == "" {
		return "", false
	}
	return qualifyChatID(channel, chatID), true
}
