---
title: "Web 插件"
weight: 10
---

Web 插件是由前端插件运行时直接加载的 TypeScript ESM 模块——**无服务端 VM、无沙箱**。设计文档见 `docs/agent/web-plugin-system.md`（"类型即契约"）；类型包在 `web/src/plugin-api/`；运行时在 `web/src/plugin-runtime/`；生产示例是 `plugins/xbot-git-fancy/` 与内置插件管理面板（`web/src/plugins/manager/`）。

## 架构

```
┌─ Web 前端 ──────────────────────────────┐
│  PluginRuntime：loader / registry /     │
│  events / rpc / state / ui / ViewSlot   │
│  插件模块直接 import 进宿主 React 树      │
│  崩溃隔离 = ErrorBoundary                │
└──────────┬──────────────────────────────┘
           │ web_plugin_* 消息（WS/SSE）
┌──────────▼──────────────────────────────┐
│ Go 后端：清单下发 + EventBridge +        │
│ WebPluginRPC 路由 + 静态文件托管          │
└─────────────────────────────────────────┘
```

信任模型（同 VSCode 扩展）：安装插件 = 信任插件。类型系统保证**契约正确性**，不做安全边界。

## 清单（编译期类型检查）

```ts
import type { PluginManifest } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.git-fancy',
  name: 'Git Fancy',
  version: '0.3.2',
  permissions: ['rpc', 'ui', 'events'] as const,
  contributes: [
    { kind: 'view', id: 'xbot.git-fancy.panel', container: 'right_sidebar',
      title: 'Git', icon: 'git-branch', entry: 'index.js' },
    { kind: 'view', id: 'xbot.git-fancy.commit', container: 'main',
      title: 'Commit', icon: 'git-commit-horizontal', entry: 'commit.js',
      dynamic: true },
  ] as const,
} satisfies PluginManifest
```

`Contribution` 联合（`web/src/plugin-api/manifest.ts`）覆盖 `ViewContribution`、`CommandContribution`、`MessageRendererContribution`、`ToolbarContribution`、`ContextMenuContribution`、`SettingContribution`、`EventHandlerContribution`、`ThemeContribution`。`satisfies` 提供编译期形状检查，同时保留字面量类型。

- **`container: 'right_sidebar'`** —— 右侧边栏面板 tab。
- **`container: 'main'`** —— 主编辑区 tab（VSCode editor 语义），全宽渲染。
- **`dynamic: true`** —— 动态视图不进 activity bar/侧栏/布局注册表；只能经 `ctx.ui.openViewTab(...)` 打开。

## 能力即类型：权限决定上下文形状

`PluginContext<P>`（`web/src/plugin-api/context.ts`）是声明权限上的类型函数：

```ts
export type Permission = 'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins' | 'config'
export type PluginContext<P extends readonly Permission[]> = {
  readonly [K in Permission]: K extends P[number] ? PermissionAPI[K] : never
} & { readonly meta: PluginMeta; readonly contributes: ContributionAPI }
```

访问未声明能力是**编译错误**（`never`）。运行时 `buildContext` 严格按声明注入——未声明的 API 是 `undefined`。

## 核心 API

### RPC（类型化方法表）

`ctx.rpc.call(method, params)` 对照 `BackendRPC`（`web/src/plugin-api/rpc.ts`）检查——方法名、参数、返回值：

```ts
const status = await ctx.rpc.call('git.status', { channel: 'web', chatID })
// status: { branch: string; changes: ...; is_repo: boolean }  ← 自动推断

ctx.rpc.notify('plugin.set_config', { id, key, value })  // 单向通知
```

后端插件通过**声明合并**扩展方法表——发布 `.d.ts` 增强 `BackendRPC`，并在 `web_plugin_rpc` 实现方法（见 [Stdio 插件](../stdio-plugins/)）。

### 事件（EventMap 定型）

```ts
ctx.events.on('message.committed', (p) => { /* p: { turnID; message: SafeMessage } */ })
ctx.events.on('turn.started', (p) => { /* p: { turnID; trigger } */ })
ctx.events.once('session.switched', (p) => { /* p: { session: SessionSummary } */ })
```

核心 `EventMap`（`web/src/plugin-api/events.ts`）：`message.committed`、`message.streaming`、`turn.started`、`turn.ended`、`session.switched`、`progress.iteration`、`context.compressed`、`command.executed`。同样可声明合并扩展。

### UI（VSCode 式语义操作，不暴露 DOM）

```ts
ctx.ui.showToast('Done', 'success')                       // toast
ctx.ui.openPanel('right_sidebar')                         // 打开容器
ctx.ui.openViewTab({ viewId: 'xbot.git-fancy.commit',     // 编辑器 tab
  title: 'abc1234', key: 'commit-abc1234', params: { hash: 'abc1234' } })
const editor = ctx.ui.openFileTab('/path/to/file.go', { line: 42, highlight: { startLine: 40, endLine: 50 } })
editor.revealLine(100, { center: true }); editor.setSelection(1, 0, 1, 10)
const diff = ctx.ui.openDiffTab({ title: 'a.go', original: oldText, modified: newText, path: 'a.go' })
diff.nextDiff(); diff.setRenderSideBySide(false)
```

`OpenViewTabOptions.key` 去重 tab（同 key 聚焦、不同 key 新开 tab）；`params` 成为视图组件的 props。

### State / Config / Plugins / Commands

- `ctx.state` —— 键值存储（`StateAPI`，`web/src/plugin-api/state.ts`）。
- `ctx.config` —— 读写插件自身配置（schema 在 `contributes.configuration` 声明）。
- `ctx.plugins` —— 插件间注册表。
- `ctx.commands` —— `register(id, handler)`、`execute(id, args)`、`registerKeybinding(keybinding, commandId)`。

## 激活与热重载

```ts
export function activate<P extends readonly string[]>(ctx: PluginContext<P>) {
  const disposables: Disposable[] = []
  disposables.push(ctx.events.on('turn.ended', () => {}))
  return disposables  // deactivate 时按逆序执行
}
```

热重载（`usePluginRuntimeHost.ts`）：卸载旧实例（逆序 disposables + 移除贡献点）→ 激活新实例。第三方插件经版本化 URL（`?v=<version>`）加载；内置插件静态导入。

## 消息渲染器

`MessageRendererContribution` 声明插件工具在聊天消息中的渲染方式：`PluginRuntime.renderTool` 调度器按 `{tool}`/`{uiMode}`/`{role}`/`{}` 匹配，按优先级降序。内置 `builtinGenuiRenderer` 匹配 `{uiMode:'genui'}`；`builtinLegacyDisplayHtmlRenderer` 匹配 `{tool:'display_html'}` 兜底旧历史消息。

## 清单 → 后端

后端 `PluginManifest.Web *WebPluginDecl`（`plugin/plugin.go:131`）只携带 `Entry` + 不透明 `Contributes` JSON——**后端不做语义校验**（唯一门禁 = 前端 `registry.validate()`；双门禁是 dsh/cordis 的教训）。`web_plugin_list` RPC 下发带 Web 声明的清单；静态文件托管在 `/plugins/<id>/web/*`，带插件 ID 正则 + 路径清理守卫。

## 避坑清单（全部来自生产事故）

1. ⚠️ **权限必须包含实际用到的每个能力。** 漏 `"ui"` → `ctx.ui` undefined → `openViewTab` 点击静默失效。Go 白名单（`plugin/permissions.go allPermissions`）也必须包含该权限——它编译进服务端二进制。
2. ⚠️ **内置视图必须静态导入**（`builtinViews` map），绝不动态 `import()`——动态导入让 rolldown 把 React hooks 错误绑定到错误 vendor chunk（React #311，黑屏）。
3. ⚠️ **条件提前 return 之后禁止加 hook**——`return null` 加载分支后的 `useState` 会改变 hook 数量（React #310，整面板崩溃）。所有 hook 放在条件 return 之前。
4. ⚠️ **多入口插件必须 `esbuild --splitting` 构建**——每个 view 入口是独立模块；不拆包时注入的共享单例（rpc/ui）跨入口不可见。
5. ⚠️ **插件 view tab 必须用 `component: viewId`**——`renderPluginView` 按 `view.id === component` 查找；泛型组件名永远查不到。
6. ⚠️ **测试里 mock `usePluginRuntime` 必须返回稳定引用**——每次渲染新对象会让 `AsyncPluginView` 的 `useEffect [runtime]` 无限循环（vitest 挂死）。
7. ⚠️ **`filterPanels` 必须保持 root 为 branch**——把单子 branch 提升为叶子会持久化非法布局（`fromJSON` 断言 "root must be of type branch"）→ 每次切回该会话 CrashBoundary 崩溃。
