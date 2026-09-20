package agent

import (
	"testing"

	"xbot/channel"
)

// REPRO（2026-09-17 用户报告："我输入 !pwd 没有输出啊"）——根因 4。
//
// 命令回复（`!cmd` bang / slash）**没有 turn**（后端命令分发按设计不分配 turn），
// 但 sendMessage 在 TurnID==0 时会回落 getActiveTurnID。于是当该会话里正有 turn
// 在跑（用户等 agent 输出时插了条 `!pwd`），命令回复被打上那个 turn 的 id；
// 前端 M4 状态机把它当作该 turn 的最终回复提交 → 随后被该 turn 的真回复覆盖 →
// 命令输出**静默消失**。
//
// 探针实证（自己开 SSE + 在 turn 进行中发 !echo）：
//
//	{"type":"text","seq":54,"content":"```\nDURING_TURN\n```",...,"turn_id":1}  ← 继承了正在跑的 turn
//
// 修复：命令回复带 command_reply 标记，sendMessage 见到就绝不回落 activeTurn。
func TestSendCommandReply_DoesNotInheritActiveTurn(t *testing.T) {
	a := &Agent{}
	ss := &bgSessionState{}
	ss.activeTurnID.Store(7) // 该会话里正有一个 turn 在跑
	a.bgSessionStates.Store("web:chat-1", ss)

	var got channel.OutboundMsg
	a.directSend = func(m channel.OutboundMsg) (string, error) {
		got = m
		return "msg-id", nil
	}

	// 命令回复（turn-less）→ 不得继承 activeTurn，且必须带显式标记。
	if err := a.sendCommandReply("web", "chat-1", "```\n/root\n```", nil); err != nil {
		t.Fatalf("sendCommandReply: %v", err)
	}
	if got.TurnID != 0 {
		t.Fatalf("命令回复 TurnID = %d，期望 0（命令没有 turn —— 否则前端会把输出并进正在跑的 turn）", got.TurnID)
	}
	if got.Metadata["command_reply"] != "true" {
		t.Fatalf("命令回复缺少 command_reply 标记: %#v", got.Metadata)
	}

	// 对照：非命令回复（工具中途发消息）仍按 activeTurn 打戳 —— 既有行为不回归。
	if err := a.sendMessage("web", "chat-1", "mid-turn notice"); err != nil {
		t.Fatalf("sendMessage: %v", err)
	}
	if got.TurnID != 7 {
		t.Fatalf("非命令回复 TurnID = %d，期望 7（activeTurn 回落行为保留）", got.TurnID)
	}
}
