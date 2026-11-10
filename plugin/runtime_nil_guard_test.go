package plugin

// runtime_nil_guard_test —— 用户 2026-10-17 进程 panic 栈的判据。
//
// 栈：plugin.(*StdioPluginProcess).Call → makeRemoteEnricher → EnricherRegistry.RunAll
//     → pluginEnricherMiddleware.Process → buildPrompt → processMessage（panic recovered）
// 根因：插件**停用/启动失败**后 `stdioPlugin.process == nil`，而它的 enricher/hook 依旧留在
// 注册表里（`EnricherRegistry` 无注销路径）⇒ 之后每条消息都会在 `p.mu.Lock()` 上解引用 nil。

import (
	"context"
	"testing"
)

func TestCallOnNilProcessReturnsError(t *testing.T) {
	var p *StdioPluginProcess // nil 接收者（插件未启动）
	if _, err := p.Call(context.Background(), &PluginRequest{Method: "status"}); err == nil {
		t.Fatal("⛔ nil 接收者必须返回错误（fail-soft），不许 panic")
	}
}
