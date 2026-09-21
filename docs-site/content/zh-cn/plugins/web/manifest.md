---
title: "Manifest"
weight: 3
---

Web 插件的 manifest 是单一真相源：既是后端下发的运行时数据，也是编译期检查的类型源头。定义于 `web/src/plugin-api/manifest.ts`。

## PluginManifest

```ts
export interface PluginManifest {
  /** 全局唯一插件 id（如 "xbot.git-info"）。 */
  id: string
  name: string
  version: string
  description?: string
  /** 能力声明——决定 ctx 形状（类型即契约 §能力即类型）。 */
  permissions?: readonly Permission[]
  /** 强依赖：必须先于本插件激活的插件 id 列表（拓扑激活）。 */
  activationDependencies?: readonly string[]
  /** 类型化贡献点声明。 */
  contributes: readonly Contribution[]
  /** 前端入口模块（ESM）。纯后端插件无此字段。 */
  entry?: string
}
```

## 权限（Permission）

```ts
export type Permission = 'events' | 'commands' | 'rpc' | 'state' | 'ui' | 'plugins' | 'config'
```

每个权限解锁 `PluginContext` 上的一个能力接口（见[PluginContext API](context-api.md)）。后端权限白名单（`plugin/permissions.go` 的 `allPermissions`）必须与这个列表同步——新增前端 `Permission` 值需要同步添加对应 Go 常量。

## 贡献点（Contribution）

`Contribution` 是八个成员的判别联合：

### 视图 —— `kind: 'view'`

```ts
export type ViewContainer = 'right_sidebar' | 'panel' | 'bottom' | 'info_bar' | 'status_bar_right' | 'iteration' | 'main'

export interface ViewContribution {
  kind: 'view'
  /** 全局唯一：<pluginId>.<viewId>。 */
  id: string
  /** 渲染容器。 */
  container: ViewContainer
  title: string
  icon?: string
  /** ESM 模块路径（相对插件包根）。entry 导出的默认组件即视图。 */
  entry?: string
  /** L1 声明式视图：type + props（无需 entry）。 */
  component?: ComponentDecl
  /** 容器内对齐：'start'（默认）或 'end'。 */
  align?: 'start' | 'end'
  /** 参数化动态视图：无静态入口，只能通过 ctx.ui.openViewTab 打开。 */
  dynamic?: boolean
}
```

- 视图的面板 tab **自动**出现在桌面侧栏和移动侧栏（`usePluginViewPanels`）——声明一次，两端渲染。
- `dynamic: true` 的视图被侧栏与布局注册表过滤——只能通过 `ctx.ui.openViewTab({ viewId, params })` 打开（VSCode webviewPanel 语义，见[Editor View API](editor-view.md)）。
- `container: 'main'` 映射到桌面主编辑区（`desktop.main` slot）——视图渲染为全宽编辑器 tab。

### 命令 —— `kind: 'command'`

```ts
export interface CommandContribution {
  kind: 'command'
  id: string
  title: string
  keybinding?: string
  /** 禁用/显示条件表达式（保留给未来 when 求值器）。 */
  when?: string
}
```

handler 在运行时解析：先查 manifest 的可选 `handlers[id]`，再查模块导出的 `commandHandlers[id]`（`registry.ts` `mount`）。

### 消息渲染器 —— `kind: 'messageRenderer'`

在聊天流中渲染工具结果/消息。匹配条件精化渲染参数类型。见[消息渲染器](message-renderer.md)。

### 工具栏 / 上下文菜单

```ts
export interface ToolbarContribution {
  kind: 'toolbar'
  id: string
  title: string
  icon?: string
  /** 点击后执行的命令 id。 */
  command: string
}

export interface ContextMenuContribution {
  kind: 'contextMenu'
  id: string
  title: string
  /** 匹配消息/文件类型（保留）。 */
  when?: string
  command: string
}
```

### 设置项 —— `kind: 'setting'`

```ts
export interface SettingContribution {
  kind: 'setting'
  key: string
  type: 'boolean' | 'string' | 'number' | 'select' | 'multiselect'
  label: string
  description?: string
  default?: unknown
  options?: Array<{ label: string; value: string }>
  /** 分组名：同一 section 的属性在设置面板归为一组。 */
  section?: string
  /** 敏感值：UI 中以掩码输入框展示。 */
  secret?: boolean
  placeholder?: string
  required?: boolean
}
```

### 事件处理器 —— `kind: 'eventHandler'`

```ts
export interface EventHandlerContribution<E extends keyof EventMap = keyof EventMap> {
  kind: 'eventHandler'
  event: E
  /** 处理逻辑模块路径。模块导出 handler(payload: EventMap[E])。 */
  entry: string
  /** 订阅所需权限（缺省继承插件 permissions）。 */
  permission?: string
}
```

### 主题 —— `kind: 'theme'`

```ts
export interface ThemeContribution {
  kind: 'theme'
  cssVars: Record<string, string>
}
```

## 运行时注册（ContributionAPI）

`activate` 可以动态注册额外贡献点。每个注册调用返回 disposable，卸载时自动清理：

```ts
export interface ContributionAPI {
  register(contribution: Contribution): Disposable
  registerAll(contributions: readonly Contribution[]): Disposable
}
```

## Disposable

```ts
/** 可清理句柄：调用即释放，幂等。 */
export type Disposable = () => void
```

## PluginMeta

```ts
/** 传给 activate 的生命周期元信息。 */
export interface PluginMeta {
  id: string
  version: string
}
```

## 校验（单一门控）

前端 `ContributionRegistry.validate()` 是贡献点语义校验的**唯一**位置（`web/src/plugin-runtime/registry.ts`）：

- `id`/`name`/`version` 必须存在；`contributes` 必须是数组。
- 每个贡献点必须有合法的 `kind` 和非空 `id`。
- 贡献点 id 必须**跨插件**唯一——`messageRenderer` 除外（允许重名，走优先级链）。
- 所有 `activationDependencies` 必须已激活。

后端只做传输层检查（entry 非空、插件 ID 合法、静态路径安全）。**绝不在后端再加一层语义校验**——两处门控必漂移。

### 插件文案（`web.i18n`）

插件文案随插件**一起分发**，绝不写进宿主的 i18n 文件：

```json
{
  "web": {
    "entry": "index.js",
    "i18n": { "en": { "save": "Save" }, "zh-CN": { "save": "保存" }, "ja": { "save": "保存" } }
  }
}
```

`ctx.i18n.t(key, fallback)` 按宿主当前语言解析，回退链：精确 locale → 语言前缀
（`zh-TW`/`zh-HK` 命中 `zh-CN`）→ `en` → 表里首个可用 locale → 你的 `fallback` 参数 → key 本身。
`ctx.i18n` 对**所有插件**可用（不需要权限），且**调用时**读取语言 ⇒ 宿主切语言后插件界面立即跟随。
建议 fallback 直接写英文，或三语表都补齐——否则宿主显示某种语言时插件会显示兜底文案。

> ⚠️ **清单在服务端启动时只读一次。** 改 `plugin.json`（`web.i18n` / `web.entry` / `permissions` /
> `version` …）在**重载插件之前不生效**：`config action=reload_plugins`（日志出现
> `Plugin discovered plugin=<id>`）或重启服务。插件的 **web 静态产物**（`/plugins/<id>/web/*.js`）
> 是每次请求从磁盘读的，改完立即可见——这会造成「新 bundle 生效、清单却是旧的」的陷阱
> （症状：`ctx.i18n` 静默回退到你的兜底文案）。

### 容器（`container`）

`right_sidebar` · `panel` · `bottom` · `info_bar` · `status_bar_right` · `iteration` · `main`。

- `main`：渲染为桌面主编辑区的全宽编辑器 tab。
- `info_bar`：渲染在**桌面底部状态栏**——与应用的「检查更新」按钮同一行，适合放紧凑状态条
  （例如 ssh-runner 插件在这里显示当前会话绑定的 runner，并可点击切换）。
