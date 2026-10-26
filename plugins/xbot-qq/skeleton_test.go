package main

import (
	"testing"
	"time"
)

// TestBackoff 覆盖重连退避：1s 起指数增长、封顶 60s、可配放弃点。
func TestBackoff(t *testing.T) {
	r := DefaultReconnectPolicy()
	want := map[int]time.Duration{
		1: time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 32 * time.Second,
		7: time.Minute, // 64s 封顶到 60s
		8: time.Minute,
	}
	for attempt, exp := range want {
		if got := r.Backoff(attempt); got != exp {
			t.Errorf("Backoff(%d) = %v, want %v", attempt, got, exp)
		}
	}
	if got := r.Backoff(0); got != 0 {
		t.Errorf("Backoff(0) = %v, want 0 (invalid attempt)", got)
	}

	capped := ReconnectPolicy{Min: time.Second, Max: 10 * time.Second}
	if got := capped.Backoff(9); got != 10*time.Second {
		t.Errorf("capped Backoff(9) = %v, want 10s (Max)", got)
	}

	limited := ReconnectPolicy{Min: time.Second, Max: 10 * time.Second, MaxAttempts: 2}
	if got := limited.Backoff(2); got != 2*time.Second {
		t.Errorf("limited Backoff(2) = %v, want 2s", got)
	}
	if got := limited.Backoff(3); got != 0 {
		t.Errorf("limited Backoff(3) = %v, want 0 (past MaxAttempts)", got)
	}
}

// TestSlashGateIsSlash 只做语法判断，不含任何放行策略。
func TestSlashGateIsSlash(t *testing.T) {
	g := DefaultSlashGate()
	for _, s := range []string{"/help", "  /model gpt", "/"} {
		if !g.IsSlash(s) {
			t.Errorf("IsSlash(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "help", "a/b", "(/help)", "／全角斜杠"} {
		if g.IsSlash(s) {
			t.Errorf("IsSlash(%q) = true, want false", s)
		}
	}
}

// TestIdentityValid 身份齐全才允许送进 agent。
func TestIdentityValid(t *testing.T) {
	tests := []struct {
		id   Identity
		want bool
	}{
		{Identity{SenderID: "10001", ChatID: "10001"}, true},
		{Identity{SenderID: "10001", ChatID: "group:1", ChatType: "group"}, true},
		{Identity{SenderID: "", ChatID: "group:1"}, false},
		{Identity{SenderID: "10001", ChatID: ""}, false},
		{Identity{}, false},
	}
	for _, tc := range tests {
		if got := tc.id.Valid(); got != tc.want {
			t.Errorf("Identity%+v.Valid() = %v, want %v", tc.id, got, tc.want)
		}
	}
}

// TestInboundCandidateParams 参数名必须与 host 侧 send_inbound 的字段名一致
// （serverapp/rpc_table.go 的 registerSettingsHandlers）。
func TestInboundCandidateParams(t *testing.T) {
	c := InboundCandidate{
		Identity: Identity{
			SenderID:   "10001",
			SenderName: "鲸鱼娘",
			ChatID:     "group:20002",
			ChatType:   "group",
		},
		Content:   "你好",
		Admin:     true,
		RequestID: "req-1",
	}
	p := c.Params()

	for _, k := range []string{"channel", "chat_id", "content", "sender_id", "sender_name", "chat_type", "request_id"} {
		if _, ok := p[k]; !ok {
			t.Errorf("params missing %q", k)
		}
	}
	if p["channel"] != "qq" {
		t.Errorf("params[channel] = %v, want qq", p["channel"])
	}
	if p["chat_id"] != "group:20002" || p["sender_id"] != "10001" {
		t.Errorf("params identity mismatch: %v", p)
	}
	meta, ok := p["metadata"].(map[string]string)
	if !ok || meta["admin"] != "true" {
		t.Errorf("params[metadata] = %v, want map with admin=true", p["metadata"])
	}
}
