package agent

import (
	"strings"
	"testing"
)

// TestSessionAddressNormalization 锁住 2026-11-03 P0 的根因：
// (channel="agent", chatID="web:web-4") 必须归一化成 "web:web-4"，
// 而不是拼成镜像地址 "agent:web:web-4"。
//
// 变异自证：把 sessionAddress 的 agent 分支删掉（回滚到
// `return channel + ":" + chatID, true`），本测试立刻变红。
func TestSessionAddressNormalization(t *testing.T) {
	cases := []struct {
		name       string
		channel    string
		chatID     string
		wantAddr   string
		wantOK     bool
		wantMirror bool
	}{
		{"mirror collapses to real session", "agent", "web:web-4", "web:web-4", true, false},
		{"web session untouched", "web", "web-4", "web:web-4", true, false},
		{"cli session untouched", "cli", "/home/smith/repo", "cli:/home/smith/repo", true, false},
		{"agent chatID without inner colon is a real agent session", "agent", "web-4", "agent:web-4", true, false},
		{"empty channel", "", "web-4", "", false, false},
		{"empty chatID", "web", "", "", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := sessionAddress(c.channel, c.chatID)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if got != c.wantAddr {
				t.Fatalf("sessionAddress(%q,%q) = %q, want %q", c.channel, c.chatID, got, c.wantAddr)
			}
			if c.wantOK && isMirrorSessionAddress(got) != c.wantMirror {
				t.Fatalf("isMirrorSessionAddress(%q) = %v, want %v", got, !c.wantMirror, c.wantMirror)
			}
		})
	}
}

// TestParentKeyIsNeverMirror 是上面那条的加强版：无论 origin 是什么形态，
// parentKey 归一化后都不能是执行上下文镜像 —— 镜像地址一出现，子代理的完成
// 通知就会投进一个独立会话，父会话永远收不到。
func TestParentKeyIsNeverMirror(t *testing.T) {
	origins := [][2]string{
		{"agent", "web:web-4"},
		{"agent", "web:chat_434275EC2D39"},
		{"web", "web-4"},
		{"cli", "/home/smith/src/xbot-exp"},
	}
	for _, o := range origins {
		addr, ok := sessionAddress(o[0], o[1])
		if !ok {
			t.Fatalf("sessionAddress(%q,%q) rejected a valid origin", o[0], o[1])
		}
		if isMirrorSessionAddress(addr) {
			t.Fatalf("parentKey %q is still a mirror address for origin (%q,%q) — "+
				"notifications would land in an isolated session", addr, o[0], o[1])
		}
		if strings.HasPrefix(addr, "agent:") && strings.Contains(strings.TrimPrefix(addr, "agent:"), ":") {
			t.Fatalf("parentKey %q carries the agent mirror prefix", addr)
		}
	}
}
