package plugin

// runtime_nil_test —— 用户 2026-10-17 panic 的回归判据。
//
// 现场栈（生产）：`plugin.(*StdioPluginProcess).Call` 在 `p.mu.Lock()` 上解引用 **nil** ——
// 插件停用/未启动时 `stdioPlugin.process == nil`，而它的 enricher 仍在注册表里 ⇒
// 每条消息经 EnricherRegistry.RunAll 都会 panic（被 clipanic recover ⇒ 回合直接失败）。
// 判据：未运行的插件必须**返回错误**，绝不许崩。

import (
	"context"
	"testing"
)

func TestCallOnNilProcessReturnsErrorNotPanic(t *testing.T) {
	var p *StdioPluginProcess // nil 接收者：正是生产现场的形态
	if _, err := p.Call(context.Background(), &PluginRequest{Method: "enrich"}); err == nil {
		t.Fatal("⛔ nil 插件进程必须返回错误（否则 RunAll 每轮 panic）")
	}
}

func TestCallOnStoppedProcessReturnsError(t *testing.T) {
	p := &StdioPluginProcess{} // running=false
	if _, err := p.Call(context.Background(), &PluginRequest{Method: "enrich"}); err == nil {
		t.Fatal("未运行的进程必须返回错误")
	}
}
