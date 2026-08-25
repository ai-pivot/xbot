---
title: "Web 插件系统"
weight: 1
---

xbot Web 插件系统（v2）让插件可以**随意强化 Web UI**。三条核心原则：

1. **无服务器端 VM** —— Web 插件是编译后的 ESM 模块，直接在前端加载执行；后端插件模型（Go 原生 / stdio 进程）保持不变。
2. **无沙箱** —— 插件是可信代码。安装插件即信任插件（同 VSCode 扩展 / 浏览器扩展模型）。类型系统约束的是**契约正确性**，不假装做安全边界。
3. **类型即契约（Type-as-Contract）** —— 用真正的类型系统（判别联合、能力即类型、条件类型精化、声明合并）在**编译期**约束插件能做的一切：贡献点形状、能力 API、事件载荷、RPC 方法、渲染器匹配-参数关联。

## 架构

```
┌─ xbot Web 前端（插件运行的地方）────────────────────────────┐
│                                                            │
│  PluginRuntime（web/src/plugin-runtime/）                   │
│  ├── loader.ts    ESM 动态 import（版本化 URL）              │
│  ├── registry.ts  类型化贡献点注册表（views/…/themes）        │
│  ├── events.ts    类型化事件总线（EventMap 索引访问）         │
│  ├── rpc.ts       类型化 RPC 桥（BackendRPC 方法表）          │
│  ├── commands.ts  命令注册表 + 快捷键                        │
│  ├── state.ts     只读快照（structuredClone）                │
│  ├── config.ts    按插件隔离的配置服务                       │
│  ├── layoutRegistry.ts  VSCode 式布局槽位                    │
│  ├── editorTabs.ts      模块级 editor-view 打开器            │
│  └── PluginView.tsx     ErrorBoundary 隔离的视图宿主         │
│                                                            │
│  插件模块直接 import 进宿主 React 运行时                     │
│  崩溃隔离 = React ErrorBoundary + 贡献点级回滚               │
└──────────────┬─────────────────────────────────────────────┘
               │ web_plugin_* 消息（复用现有 WS/SSE）
┌──────────────▼─────────────────────────────────────────────┐
│ xbot 后端（Go）                                             │
│  ├── 插件激活管理：下发清单（贡献点 + 模块 URL + 权限）        │
│  ├── EventBridge：agent 生命周期事件 → 前端插件事件          │
│  │     （web_plugin_event）                                  │
│  └── WebPluginRPC：把 pluginId.method 调用路由到             │
│        后端插件进程（web_plugin_rpc）                        │
└────────────────────────────────────────────────────────────┘
```

## 信任模型（替代沙箱）

- 安装插件 = 信任插件（VSCode 扩展模型）。
- 类型系统保证契约正确性（API 形状、载荷类型、参数关联）。
- 运行时只做最小防御：贡献点 ID 冲突检测、版本检查、ErrorBoundary 崩溃隔离、插件级启用/禁用开关。
- 生态治理：市场审核 + 发布者签名 + 插件页明示权限清单（用户知情）。
- LLM 生成的动态代码（GenUI）保留 iframe 渲染隔离——那是视觉隔离 + 动态代码卫生，不是安全边界。

## 核心概念

| 概念 | 文件 | 作用 |
|---|---|---|
| `PluginManifest` | `web/src/plugin-api/manifest.ts` | 类型化贡献点声明 + 权限 |
| `PluginContext<P>` | `web/src/plugin-api/context.ts` | 能力即类型：权限决定 ctx 形状 |
| `EventMap` | `web/src/plugin-api/events.ts` | 事件名 ↔ 载荷索引访问 |
| `BackendRPC` | `web/src/plugin-api/rpc.ts` | 方法表驱动的类型化 RPC |
| `MatchedMessage<M>` | `web/src/plugin-api/renderer.ts` | 匹配条件精化渲染参数类型 |
| `ComponentDecl` | `web/src/plugin-api/components.ts` | L1 声明式 UI 组件 |
| `PluginExportsMap` | `web/src/plugin-api/plugins.ts` | 声明合并的插件间导出 |
| `LayoutSlotId` | `web/src/plugin-runtime/layoutTypes.ts` | 命名布局槽位（手机底部导航、桌面侧栏……） |
| `ContributionRegistry` | `web/src/plugin-runtime/registry.ts` | 单一启动门控 + 热加载 |

## 快速开始

```ts
// 插件源码（编译为 ESM，托管在 /plugins/<id>/web/）
import type { PluginContext, PluginManifest } from '@xbot/plugin-api'

export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  permissions: ['events', 'rpc', 'ui'] as const,
  contributes: [
    { kind: 'view', id: 'demo.panel', container: 'right_sidebar',
      title: 'Demo', entry: './panel' },
  ] as const,
} satisfies PluginManifest

export function activate(ctx: PluginContext<typeof manifest.permissions>) {
  ctx.events.on('turn.started', (ev) => {
    void ctx.rpc.call('session.get', { chatID: 'x' })
  })
  return () => { /* 卸载清理 */ }
}
```

## 与后端插件系统的关系

后端插件系统（Go 原生 / stdio / gRPC / script runtime）与前端 Web 插件 v2 运行时是**两套系统**。同一个插件包可以**同时**携带两者：`plugin.json` 后端清单 + `Web` 声明（`entry` + `contributes`），后端把 Web 声明原样下发给前端。后端只做**传输层检查**（entry 非空、插件 ID 合法、静态托管路径安全）——贡献点语义校验只在前端 `registry.validate()`（单一门控）进行，规则永远不会在两处漂移。

- 前后端通信：`web_plugin_list`（拉取清单）、`web_plugin_init` / `web_plugin_deactivate`（热加载/卸载）、`web_plugin_event`（后端 → 前端事件）、`web_plugin_config_changed`（配置热重载）、`web_plugin_rpc`（前端 → 后端插件方法路由）。
- 静态托管：插件 web 产物托管在 `/plugins/<id>/web/*`（插件 ID 校验 `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`，防路径穿越）。

## 文档索引

| 文档 | 主题 |
|---|---|
| [类型即契约](type-as-contract.md) | 四件类型武器 |
| [Manifest](manifest.md) | 类型化贡献点声明 |
| [PluginContext API](context-api.md) | 能力即类型 |
| [事件总线](events.md) | 类型化事件 |
| [RPC](rpc.md) | 方法表驱动 |
| [消息渲染器](message-renderer.md) | 匹配条件精化渲染 |
| [声明式组件](components.md) | L1 组件声明 |
| [插件互操作](interop.md) | Exports API + 激活依赖 |
| [ESM 模块格式](module-format.md) | 构建与加载契约 |
| [热加载](hot-reload.md) | 重载/卸载生命周期 |
| [布局系统](layout.md) | VSCode 式布局槽位 |
| [Editor View API](editor-view.md) | webviewPanel 式编辑器 tab |
| [插件管理面板](plugin-manager.md) | 自举管理面板 |
