package tools

import (
	"sync"

	"xbot/protocol"
)

// structured.go —— 结构化消息（report / ask_user / answer / decision）的落地通道。
//
// 背景（2026-10-21，用户设计）：批红不是新工具，而是"一次特殊的 send"。
// SendMessage 已经能寻址任意会话（agent: / session: / IM），缺的只是把
// kind=decision/answer 的消息**落库并完成 CAS**，而不是当普通文本投递。
//
// 依赖方向：agent → tools（tools 不能 import agent）。所以落地实现由上层
// （serverapp 的 structured_callbacks）通过 SetStructuredAppend 注入，工具层
// 只依赖这个函数值。禁止在 tools 里另写一套 CAS——两份实现迟早分叉。

// StructuredAppendFunc 提交一条结构化消息。
//
// senderID 是**发起人**（通常是终端用户，用于审计：谁批的）；channel/chatID 是
// 目标会话（批红要落到部门会话，不是首辅自己的会话）。返回 messageID、
// duplicate（幂等命中已有行）与错误。
type StructuredAppendFunc func(senderID, channel, chatID string, in protocol.StructuredMessageInput) (int64, bool, error)

var (
	structuredMu sync.RWMutex
	structuredFn StructuredAppendFunc
)

// SetStructuredAppend 注入落地实现（serverapp 在装配 callbacks 时调用一次）。
// 传 nil 表示注销（测试用）。
func SetStructuredAppend(fn StructuredAppendFunc) {
	structuredMu.Lock()
	structuredFn = fn
	structuredMu.Unlock()
}

// StructuredAppendInstalled 报告当前是否已注入落地实现。
func StructuredAppendInstalled() bool {
	structuredMu.RLock()
	defer structuredMu.RUnlock()
	return structuredFn != nil
}

// structuredAppend 取出落地实现（未注入时为 nil）。
func structuredAppend() StructuredAppendFunc {
	structuredMu.RLock()
	defer structuredMu.RUnlock()
	return structuredFn
}
