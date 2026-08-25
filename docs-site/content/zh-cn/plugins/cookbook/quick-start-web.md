---
title: "快速上手：Web 插件"
weight: 5
---

用 TypeScript 构建出现在 Web UI 侧边栏的前端视图面板。本篇基于内置的 `xbot.git-fancy` 插件（`plugins/xbot-git-fancy/plugin.json`）与 `@xbot/plugin-api` 类型包（`web/src/plugin-api/`）。

Web 插件是编译后的 **ESM 模块**，由前端插件运行时直接加载。后端没有 JS 虚拟机——只负责静态文件托管与 RPC 路由。类型安全即契约：清单在编译期对照 `@xbot/plugin-api` 校验。

## 第一步：编写前端模块

`~/.xbot/plugins/demo-web/web/index.ts`：

```ts
// 构建命令：esbuild index.ts --bundle --format=esm --outdir=dist
import type { PluginContext, PluginManifest, Disposable } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.demo-web',
  name: 'Demo Web',
  version: '0.1.0',
  permissions: ['rpc', 'events', 'ui'] as const,
  contributes: [
    {
      kind: 'view',
      id: 'demo.panel',
      container: 'right_sidebar',
      title: 'Demo',
      icon: 'sparkles',
      entry: 'index.js',
    },
  ] as const,
} satisfies PluginManifest   // ❌ 任何字段写错都是编译错误

export function activate<P extends readonly string[]>(ctx: PluginContext<P>) {
  // ctx 由声明的权限定型：
  ctx.rpc.call('plugin.list', {}).then((plugins) => {
    ctx.ui.showToast(`Loaded ${plugins.length} plugins`, 'info')
  })
  const sub = ctx.events.on('message.committed', (p) => {
    ctx.ui.showToast(`turn ${p.turnID} committed`, 'success')
  })
  return sub // 返回的 disposables 在 deactivate 时清理
}
```

要点：

- **`manifest` 是唯一真相源** —— `satisfies PluginManifest` 让错误的 `kind`/`container`/字段形状变成编译错误。
- **权限决定上下文形状**：`PluginContext<P>` 把每个已声明权限映射为对应 API；访问未声明的权限类型为 `never`（`web/src/plugin-api/context.ts`）。
- **`activate(ctx)` 返回 disposables** —— 热重载/卸载时自动注销。
- **`ctx.rpc.call('plugin.list', {})`** 由 `BackendRPC` 方法表完整定型（`web/src/plugin-api/rpc.ts`）——方法名、参数、返回值全部编译期检查。

## 第二步：编写后端清单

```json
{
  "id": "xbot.demo-web",
  "name": "Demo Web",
  "version": "0.1.0",
  "description": "A demo web plugin",
  "runtime": "native",
  "activationEvents": ["onStart"],
  "web": {
    "entry": "index.js",
    "contributes": [
      {
        "kind": "view",
        "id": "demo.panel",
        "container": "right_sidebar",
        "title": "Demo",
        "icon": "sparkles",
        "entry": "index.js"
      }
    ]
  }
}
```

注意：

- `runtime` 随意——前端部分是独立的。`xbot.git-fancy` 用 `"stdio"` 搭配 Go 后端进程；纯 UI 插件可以用 `"native"`，完全不用写后端代码。
- `web.entry` 是相对插件 `web/` 目录的模块路径，静态托管在 `/plugins/<id>/web/<entry>`。
- `web.contributes` 是**原样透传给前端运行时的不透明 JSON**——后端不做语义校验（`plugin/plugin.go:131 WebPluginDecl`）。前端 `registry.validate()` 是唯一的权威门禁。

## 第三步：构建并托管

把构建产物放到 `~/.xbot/plugins/demo-web/web/index.js`。后端静态托管它（`channel/web/web.go handlePluginStatic`，插件 ID 受正则 `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` 守卫）。重启 xbot，"Demo" 面板即出现在右侧边栏。

## 内置 vs 第三方加载

运行时区分两条激活路径（`web/src/plugin-runtime/`）：

- **`activateBuiltin`** —— 内置插件静态 import 进主 bundle。⚠️ 内置视图必须静态导入，**禁止动态 `import()`**——动态导入会让打包器把 React hooks 错误绑定到错误的 vendor chunk（React #311，整页黑屏）。
- **`activate`** —— 第三方插件经版本化 URL（`?v=<version>`）加载，绕过浏览器模块缓存。

热重载 = 卸载（逆序执行 disposables + 移除贡献点）→ 激活新实例。

## 添加后端数据源

视图需要后端数据时，搭配一个 stdio 后端，通过类型化 RPC 桥调用：

```json
// plugin.json —— 权限必须包含实际用到的每个能力
{ "permissions": ["rpc", "ui"] }
```

```go
// 后端：plugins/xbot-git-fancy/main.go 模式
func handleWebPluginRPC(p *protocol.WebPluginRPCParams) *protocol.WebPluginRPCResult {
	switch p.Method {
	case "git.status":
		return rpcOK(gitStatus(cwd))
	// ...
	}
	return rpcErr("unknown method")
}
```

```ts
// 前端：经 BackendRPC 声明合并定型
const status = await ctx.rpc.call('git.status', { channel: 'web', chatID })
```

⚠️ **权限必须与实际使用一致**：`buildContext` 严格按声明注入 API——漏掉 `"ui"` 则 `ctx.ui` 运行时为 `undefined`，`openViewTab` 点击**静默失效**（无报错无日志）。后端白名单（`plugin/permissions.go allPermissions`）也必须包含用到的每个权限——它编译进 Go 二进制。

下一篇：[Web 插件](../web-plugins/) 完整运行时指南。
