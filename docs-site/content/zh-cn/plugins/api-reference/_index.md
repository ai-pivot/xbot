---
title: "API 参考"
weight: 1
geekdocCollapseSection: true
---

本部分是 xbot 插件开发的完整 API 参考，基于实际源码（Go 插件 SDK `plugin/` 包与 Web 前端插件 API `web/src/plugin-api/`）编写。

## 内容覆盖

| 文档 | 说明 |
|------|------|
| [Manifest Schema](manifest-schema/) | `plugin.json` 完整字段、验证规则与全部贡献点类型 |
| [PluginContext API](plugin-context-api/) | 插件激活期间可用的权限过滤 API 面 |
| [PluginTool API](plugin-tool-api/) | 工具定义、执行、结果类型与流式构建器 |
| [Hook 事件](hook-events/) | 全部 13 个生命周期 hook 事件与 `HookPayload` 字段参考 |
| [环境变量](environment-variables/) | 注入 script 插件进程的 `XBOT_*` 变量 |
| [权限列表](permissions-list/) | 全部权限字符串、含义及所控制的 API |
| [触发事件](trigger-events/) | 激活事件与 widget 触发器匹配格式 |
| [Widget Zones](widget-zones/) | widget 可渲染的 UI 槽位名称 |
| [组件类型](component-types/) | Web 视图的声明式 L1 组件类型 |
| [RPC 方法](rpc-methods/) | 后端 RPC 方法表（宿主 RPC + 前端 `ctx.rpc`） |
| [事件类型](event-types/) | 生命周期事件、类型化事件总线（`EventMap`）与通知器类型 |

## 两套插件接口

xbot 插件有两套不同的 API 面：

1. **Go SDK**（`plugin/` 包）— 进程内 native 插件及 stdio 插件的宿主侧。类型：`Plugin`、`PluginContext`、`PluginTool`、`HookPayload` 等。
2. **Web 插件 API**（`web/src/plugin-api/`，包 `@xbot/plugin-api`）— 类型安全的 ESM 前端插件。类型：`PluginManifest`、`PluginContext<P>`、`EventMap`、`BackendRPC` 等。**能力即类型**：manifest 中的 `permissions` 数组决定编译期 context 上存在哪些能力接口。

## 权限模型

插件使用的每个能力都必须在 `plugin.json` 的 `permissions` 中声明。Go 侧由 `PermissionChecker`（见[权限列表](permissions-list/)）在运行时强制；Web 侧通过 `PluginContext<P>` 在编译期强制。

通配符 `"*"` 授予全部权限（仅 Go 侧）。无效权限在 manifest 加载时即失败。
