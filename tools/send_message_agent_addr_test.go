package tools

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// 2026-09-16 用户报告：subagent 用 `agent:recov-e8e9/ws` 发消息失败 ——
//   agent send failed: unknown channel: agent:recov-e8e9/ws
//
// 根因：agent 地址是**作为频道名**投递的（sendMessageWithCtx(addr)），而该频道只在
// "同进程 spawn 且未卸载"期间存在（tools/subagent.go 注册 / unload 注销 / TTL 淘汰）
// ⇒ 查不到时 dispatcher 只回 "unknown channel: agent:…"，对调用方毫无指向性。
//
// 契约（本文件钉死）：
//  ① role/instance 写反（`agent:A/B` 查不到、`agent:B/A` 在）→ **自愈投递**；
//  ② 确实不可达 → 错误必须是**可操作指引**（含正确地址形式），而不是原文照搬。

type addrRecordingSender struct {
	known map[string]string
	calls []string
}

func (s *addrRecordingSender) SendMessage(channelName, chatID, content string) (string, error) {
	s.calls = append(s.calls, channelName)
	if reply, ok := s.known[channelName]; ok {
		return reply, nil
	}
	return "", fmt.Errorf("unknown channel: %s", channelName)
}

// ① 写反自愈：agent:recov-e8e9/ws 查不到，但 agent:ws/recov-e8e9 在。
func TestSendToAgent_SwappedRoleInstance_SelfHeals(t *testing.T) {
	sender := &addrRecordingSender{known: map[string]string{"agent:ws/recov-e8e9": "pong"}}
	tool := &SendMessageTool{}

	_, err := tool.sendToAgent(
		&ToolContext{MessageSender: sender, Ctx: context.Background()},
		"agent:recov-e8e9/ws", "hi",
	)
	if err != nil {
		t.Fatalf("role/instance 写反时必须自愈投递，实际报错: %v", err)
	}
	if len(sender.calls) != 2 || sender.calls[0] != "agent:recov-e8e9/ws" || sender.calls[1] != "agent:ws/recov-e8e9" {
		t.Fatalf("应先试原地址再用颠倒地址重试一次，实际调用序列: %v", sender.calls)
	}
}

// ② 不可达：错误必须可操作（而不是 "unknown channel: agent:..."）。
func TestSendToAgent_Unreachable_ActionableError(t *testing.T) {
	sender := &addrRecordingSender{known: map[string]string{}}
	tool := &SendMessageTool{}

	_, err := tool.sendToAgent(
		&ToolContext{MessageSender: sender, Ctx: context.Background()},
		"agent:recov-e8e9/ws", "hi",
	)
	if err == nil {
		t.Fatal("不可达时必须报错")
	}
	msg := err.Error()
	for _, want := range []string{"不可达", "agent:<role>/<instance>", "unknown channel"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("错误信息应含 %q（可操作指引 + 原始原因），实际: %s", want, msg)
		}
	}
}

// ③ 非两段式地址不做颠倒（避免误投）：swapAgentAddress 只处理 agent:A/B。
func TestSwapAgentAddress(t *testing.T) {
	cases := map[string]string{
		"agent:recov-e8e9/ws": "agent:ws/recov-e8e9",
		"agent:explore/x":     "agent:x/explore",
		"agent:rolename":      "",
		"agent:a/a":           "",
		"group:g1":            "",
		"session:cli:/p":      "",
	}
	for in, want := range cases {
		if got := swapAgentAddress(in); got != want {
			t.Fatalf("swapAgentAddress(%q) = %q, want %q", in, got, want)
		}
	}
}
