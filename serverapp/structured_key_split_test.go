package serverapp

import "testing"

// 回流 key 的拆分必须与 agent 通道的存储形态一致：交互 key 的 chatID 是**完整 key**，
// 且 `agent:` 只是投递前缀 —— 不能被当成 chatID 的一部分（旧实现会投到
// `agent:agent:web:chat/p:1` 这种畸形会话，答复自然落不到发起的子代理）。
func TestSplitPhysicalSessionKeyHandlesEveryForm(t *testing.T) {
	cases := []struct{ in, wantCh, wantChat string }{
		{"web:web-4", "web", "web-4"},
		{"web:chat_9C/taizi:fedtail-x3", "agent", "web:chat_9C/taizi:fedtail-x3"},
		{"agent:web:chat_9C/taizi:fedtail-x3", "agent", "web:chat_9C/taizi:fedtail-x3"},
		{"agent:agent:web:chat_9C/taizi:x", "agent", "web:chat_9C/taizi:x"},
		{"cli:/home/smith/src/repo", "cli", "/home/smith/src/repo"},
	}
	for _, c := range cases {
		ch, chat := splitPhysicalSessionKey(c.in)
		if ch != c.wantCh || chat != c.wantChat {
			t.Errorf("splitPhysicalSessionKey(%q)=(%q,%q) want (%q,%q)", c.in, ch, chat, c.wantCh, c.wantChat)
		}
	}
	for _, bad := range []string{"", "nocolon", "web:"} {
		if ch, chat := splitPhysicalSessionKey(bad); ch != "" || chat != "" {
			t.Errorf("splitPhysicalSessionKey(%q)=(%q,%q), want empty", bad, ch, chat)
		}
	}
}
