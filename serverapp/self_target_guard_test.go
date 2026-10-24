package serverapp

import (
	"strings"
	"testing"

	"xbot/tools"
)

// self_target_guard_test.go — D-07 自激守卫的**判别力测试**（2026-10-22）。
//
// 背景：守卫原先在 tools 层比较「逻辑地址」与「物理 key」，永不相等 ⇒ 形同虚设；
// 上一版修复又把守卫整段删掉，导致「主会话向自己会话发 report」从被拒变放行。
// 现在守卫收敛到 refuseSelfTarget 单点，这里钉死四种必拒 + 四种必放行的组合。
func TestRefuseSelfTarget(t *testing.T) {
	subagentKey := "web:web-4/team.data:main"

	cases := []struct {
		name      string
		kind      string
		caller    tools.CallerScope
		channel   string
		realChat  string
		wantError bool
	}{
		{
			name:      "interactive 子代理向自己的会话发 report：必拒",
			kind:      "report",
			caller:    tools.CallerScope{Channel: "agent", ChatID: subagentKey, SessionKey: subagentKey},
			channel:   "agent",
			realChat:  subagentKey,
			wantError: true,
		},
		{
			name:      "interactive 子代理向父会话发 report：放行（不是自己的会话）",
			kind:      "report",
			caller:    tools.CallerScope{Channel: "agent", ChatID: subagentKey, SessionKey: subagentKey},
			channel:   "web",
			realChat:  "web-4",
			wantError: false,
		},
		{
			name:      "interactive 子代理发往**别的**子代理会话：放行",
			kind:      "report",
			caller:    tools.CallerScope{Channel: "agent", ChatID: subagentKey, SessionKey: subagentKey},
			channel:   "agent",
			realChat:  "web:web-4/team.ops:main",
			wantError: false,
		},
		{
			name:      "主会话发往 agent 目标：放行（SessionKey 为空，不算「自己的会话」）",
			kind:      "decision",
			caller:    tools.CallerScope{Channel: "web", ChatID: "web-4"},
			channel:   "agent",
			realChat:  subagentKey,
			wantError: false,
		},
		{
			name:      "主会话向自己的会话发 report：必拒（旧版删守卫后这里被放行 = 净回归）",
			kind:      "report",
			caller:    tools.CallerScope{Channel: "web", ChatID: "web-4"},
			channel:   "web",
			realChat:  "web-4",
			wantError: true,
		},
		{
			name:      "主会话向别的会话发 report：放行",
			kind:      "report",
			caller:    tools.CallerScope{Channel: "web", ChatID: "web-4"},
			channel:   "web",
			realChat:  "web-5",
			wantError: false,
		},
		{
			name:      "同 chatID 不同 channel：放行（不是同一个会话）",
			kind:      "answer",
			caller:    tools.CallerScope{Channel: "web", ChatID: "web-4"},
			channel:   "feishu",
			realChat:  "web-4",
			wantError: false,
		},
		{
			name:      "子代理 key 与目标只差前缀：放行（不做模糊匹配，只比全等）",
			kind:      "report",
			caller:    tools.CallerScope{Channel: "agent", ChatID: subagentKey, SessionKey: subagentKey},
			channel:   "agent",
			realChat:  "web:web-4/team.data:main2",
			wantError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseSelfTarget(tc.kind, tc.caller, tc.channel, tc.realChat)
			if tc.wantError {
				if err == nil {
					t.Fatalf("want refusal, got nil (kind=%s target=%s:%s)", tc.kind, tc.channel, tc.realChat)
				}
				if !strings.Contains(err.Error(), "own session") {
					t.Fatalf("refusal must say it targets the caller's own session, got %v", err)
				}
				if !strings.Contains(err.Error(), tc.kind) {
					t.Fatalf("refusal must name the kind (%s) for triage, got %v", tc.kind, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("want pass, got %v", err)
			}
		})
	}
}
