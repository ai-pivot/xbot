package config

import (
	"strings"
	"testing"
)

// 2026-09-18 用户实机事故（runner `websocket: bad handshake` 无限重连、状态却显示已连接）
// 的服务端自检判据：**宣告端口必须 == 真实绑定端口**，否则静默交出死地址。
// 变异自证：把 portOf 改成恒返回 0（或让 RunnerEndpointDrift 恒返回 ""）⇒ 漂移用例必红。

func TestPortOf(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"0.0.0.0:8080", 8080},
		{"127.0.0.1:16000", 16000},
		{"ws://example.com:8080", 8080},
		{"ws://example.com:8080/ws", 8080},
		{"wss://h:9443/ws/x", 9443},
		{"host-without-port", 0},
		{"", 0},
		{":abc", 0},
	}
	for _, c := range cases {
		if got := portOf(c.in); got != c.want {
			t.Errorf("portOf(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestRunnerEndpointDrift(t *testing.T) {
	// 一致：端点绑定 8080，宣告 8080（host 不同是正常的：绑定 0.0.0.0 / 宣告公网 host）。
	consistent := &Config{}
	consistent.Sandbox.WSPort = 8080
	consistent.Server.Host = "203.0.113.9"
	if warn := consistent.RunnerEndpointDrift("0.0.0.0:8080"); warn != "" {
		t.Fatalf("consistent ports must not warn, got: %s", warn)
	}

	// 漂移（用户事故现场）：宣告 16000（web 端口），端点实际在 8080。
	drift := &Config{}
	drift.Sandbox.WSPort = 16000
	drift.Server.Host = "203.0.113.9"
	warn := drift.RunnerEndpointDrift("0.0.0.0:8080")
	if warn == "" {
		t.Fatal("port drift MUST warn (this is the 2026-09-18 incident)")
	}
	for _, want := range []string{"16000", "8080", "sandbox.ws_port"} {
		if !strings.Contains(warn, want) {
			t.Errorf("drift warning must mention %q, got: %s", want, warn)
		}
	}

	// public_url 显式指定且端口漂移 —— 同样必须报（这是最容易踩的配置）。
	pub := &Config{}
	pub.Sandbox.PublicURL = "ws://example.com:16000"
	pub.Server.Host = "example.com"
	if w := pub.RunnerEndpointDrift("0.0.0.0:8080"); w == "" {
		t.Fatal("explicit public_url with a drifting port MUST warn")
	}

	// bound 未知（端点未启动 / 解析失败）⇒ 不误报。
	if w := consistent.RunnerEndpointDrift(""); w != "" {
		t.Fatalf("empty bound addr must not warn, got: %s", w)
	}
}
