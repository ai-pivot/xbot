package config

import "testing"

// REGRESSION (2026-09-18 用户实机「目标机器 当前离线」根治):
//
// 铸给 runner 的地址必须指向**真正提供 runner 协议的那个监听** —— RemoteSandbox，
// 它监听 Sandbox.WSPort（默认 DefaultRunnerWSPort）。历史实现用 Server.Port 拼地址：
// 在 `xbot-cli serve` 部署里 Server.Port 可以是 8089 而**从无进程监听该端口**，于是
// runner 拨号的是一个死端口；隧道与鉴权都正确也永远连不上（现场：runner 日志
// `connect_to 127.0.0.1 port 8089: failed`，服务端永远 online=false，前端每次工具调用
// 都报「目标机器 X 当前离线」）。
func TestPublicWSAddr_UsesRunnerEndpointPortNotServerPort(t *testing.T) {
	c := &Config{}
	c.Server.Host = "10.0.0.5"
	c.Server.Port = 8089 // 该部署里无人监听 —— 绝不能出现在铸出的地址里
	c.Sandbox.WSPort = 0 // 未配置 ⇒ 用 runner 端点默认端口

	if got, want := c.PublicWSAddr(), "ws://10.0.0.5:8080"; got != want {
		t.Fatalf("PublicWSAddr() = %q, want %q (must use the runner endpoint port)", got, want)
	}

	// 显式配置的 runner 端口优先。
	c.Sandbox.WSPort = 9000
	if got, want := c.PublicWSAddr(), "ws://10.0.0.5:9000"; got != want {
		t.Fatalf("PublicWSAddr() = %q, want %q", got, want)
	}

	// 显式 PublicURL（NAT / 端口映射）永远最高优先级。
	c.Sandbox.PublicURL = "wss://runner.example.com/ws"
	if got, want := c.PublicWSAddr(), "wss://runner.example.com/ws"; got != want {
		t.Fatalf("PublicWSAddr() = %q, want %q (PublicURL wins)", got, want)
	}
}
