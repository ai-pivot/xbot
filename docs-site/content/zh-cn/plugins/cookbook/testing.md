---
title: "测试插件"
weight: 20
---

用内置的 `TestKit` 与 Mock 隔离测试插件——无需运行中的 xbot 实例。助手在 `plugin/testkit.go` 与 `plugin/mock.go`；真实示例自带测试：`plugins/xbot-genui/main_test.go`、`plugins/xbot-git-fancy/main_test.go`。

## TestKit —— 全上下文测试架

`TestKit` 提供完整的内存 `PluginContext`（map 存储、测试日志、注册表）：

```go
func TestMyPlugin(t *testing.T) {
	tk := plugin.NewTestKit(t)
	defer tk.Clear()

	p := NewMyPlugin()
	if err := tk.Activate(p); err != nil {
		t.Fatalf("activate: %v", err)
	}

	// 断言声明的能力确实注册了
	tk.AssertToolRegistered("hello")
	tk.AssertHookRegistered(plugin.HookPostToolUse)
	tk.AssertEnricherRegistered("hello_status")

	// 调用工具并检查结果
	result, err := tk.CallTool("hello", `{"name":"Alice"}`)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(result.Content, "Hello, Alice") {
		t.Errorf("unexpected result: %s", result.Content)
	}
}
```

其他成员：`tk.Context`（PluginContext）、`tk.Deactivate(p)`、`tk.Debug`/`tk.Debugf`（写入捕获日志）。测试日志记录一切并格式化结构化字段。

## MockPlugin / MockTool —— 组合测试替身

`plugin/mock.go` —— 可链式构建器（每个 `With*` 原地修改并返回同一指针）：

```go
mp := plugin.NewMockPlugin("xbot.mock").
	WithManifest(func(m *plugin.PluginManifest) {
		m.Name = "Mock"
	}).
	WithActivate(func(ctx plugin.PluginContext) error { return nil }).
	WithDeactivate(func(ctx plugin.PluginContext) error { return nil })

mt := plugin.NewMockTool("mock_tool").
	WithDefinition(func(d *plugin.ToolDef) { d.Description = "..." }).
	WithExecute(func(ctx context.Context, input string) (*plugin.ToolResult, error) {
		return plugin.NewToolResult("mocked"), nil
	})
```

⚠️ **不要在并行测试间共享同一个 mock**——按测试克隆（链式 API 原地修改）。

## 测什么 —— 检查清单

1. **清单有效性** —— ID 格式、权限字符串、版本 semver：
   ```go
   if !plugin.IsValidPermission("tools.register") { t.Fatal(...) }
   ```
   git-fancy 模式（`plugins/xbot-git-fancy/main_test.go TestManifestPermissions`）从磁盘读 `plugin.json`，断言每个声明的权限已知——捕获"后端白名单漂移"故障模式。

2. **激活幂等性** —— `Activate` 调用两次；第二次必须成功或干净地 no-op。

3. **工具契约** —— 每个声明的工具名存在、能解析输入、返回结构化输出。测试正常路径与畸形输入（缺参数 → 优雅默认或错误结果）。

4. **Hook 决策** —— `PreToolUse` 拒绝生效；`PostToolUse` 用正确载荷字段观察。

5. **存储往返** —— `Set` → `Get` → 重启（同目录新存储）→ `Get`。

6. **反激活** —— 资源释放；`Deactivate` 调用两次安全。

## Stdio 插件测试

stdio 后端直接测处理器（git-fancy 模式）：

```go
func TestGitStatus(t *testing.T) {
	// 用临时 git 仓库调用 handleWebPluginRPC / gitStatus
	dir := t.TempDir()
	runGit(t, dir, "init")
	result := gitStatus(dir)
	if !result.is_repo { t.Fatal("expected repo") }
}
```

再加一个**协议级测试**：把 JSON 行喂进进程并检查响应（用 `protocol.Run` 对注入的 reader/writer 拉起二进制——`protocol.run` 接受注入的 `io.Reader`/`io.Writer`）。

## 集成模式

- `plugin/integration.go` 把插件接入完整 agent 做端到端测试——`WireAll` 连接工具/Hook/注入器到注册表；`Wire*` 函数支持部分接线。
- Channel 插件测 `handleActivate` 的声明 JSON、每个工具的 `handleExecuteTool`，以及用合成的 `channel_config` 消息测 `handle_xbot_event` 路由（镜像 `echo-channel/main.py` 的处理器）。
- 限流器与配额管理器（`plugin/ratelimit.go`）有测试钩子（`SetRetryInterval` 等）——用它们加速测试。

## 前端插件测试（vitest）

`web/src/plugin-api/types.test.ts` 用 `@ts-expect-error` 编译期断言钉住类型契约。视图组件：

- mock `usePluginRuntime` 用**稳定引用**（每次渲染新对象会挂死 worker——`vi.hoisted` 模式）。
- 在插件模块**动态 import 之前**注入 `window.React`（静态 import 被提升到注入之上——见 git-fancy index 测试）。
- 断言 loading → loaded 转换期间 hook 数量稳定（React #310 回归：条件提前 return 之后的 hook）。
