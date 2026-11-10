package web

import (
	"encoding/json"
	"strings"
	"testing"
)

// canonicalChatID 必须把**同一 channel 的任意层前缀**剥掉，且不影响其它
// channel 的 id / 子代理 id（`agent:role/inst` 里含冒号但是不同 channel）。
func TestCanonicalChatID(t *testing.T) {
	cases := []struct {
		name    string
		channel string
		in      string
		want    string
	}{
		{name: "bare", channel: "web", in: "chat_X", want: "chat_X"},
		{name: "one prefix", channel: "web", in: "web:chat_X", want: "chat_X"},
		{name: "repeated prefix", channel: "web", in: "web:web:chat_X", want: "chat_X"},
		{name: "other channel untouched", channel: "web", in: "cli:chat_X", want: "cli:chat_X"},
		{name: "subagent id untouched", channel: "web", in: "agent:role/inst", want: "agent:role/inst"},
		{name: "empty channel", channel: "", in: "web:chat_X", want: "web:chat_X"},
		{name: "whitespace", channel: "web", in: "  web:chat_X  ", want: "chat_X"},
		{name: "empty", channel: "web", in: "", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := canonicalChatID(c.channel, c.in); got != c.want {
				t.Fatalf("canonicalChatID(%q, %q) = %q, want %q", c.channel, c.in, got, c.want)
			}
		})
	}
}

// `/api/session-tree` 的 running 必须**总是**出现（false 也要在）。
// 客户端用它对账"会话是否在跑"；omitempty 会让 false 缺字段 ⇒ 只能猜。
func TestSessionTreeRunningAlwaysSerialized(t *testing.T) {
	b, err := json.Marshal(UserChatWithPreview{ChatID: "chat_X", Running: false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"running":false`) {
		t.Fatalf("running=false must be serialized, got %s", string(b))
	}
	b2, err := json.Marshal(UserChatWithPreview{ChatID: "chat_X", Running: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b2), `"running":true`) {
		t.Fatalf("running=true missing, got %s", string(b2))
	}
}
