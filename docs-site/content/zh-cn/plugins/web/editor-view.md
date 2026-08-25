---
title: "Editor View API"
weight: 13
---

Editor View API 给插件 VSCode 式编辑器 tab：在**主编辑区**打开参数化插件视图、宿主文件编辑器或原生 diff 编辑器——并附带命令式控制句柄。类型在 `web/src/plugin-api/ui.ts`；运行时在 `web/src/plugin-runtime/editorTabs.ts` + `editorRegistry.ts`。

## 模块级桥

PluginRuntime/PluginUI 是纯 TS 实例（**不在** React 树内），而 `tabManager` 在 AppShell（React 树内）创建。模块级可变注册器解耦两者：

```
AppShell（挂载时）   → registerEditorTabOpener(tabManager.openTab)
PluginUI.openViewTab() → openEditorViewTab(options)
```

```ts
export type EditorTabOpener = (input: {
  type: 'plugin' | 'file' | 'diff'
  title: string
  icon?: string
  closable?: boolean
  data?: Record<string, unknown>
}) => string

/** AppShell 挂载时注册 tabManager.openTab（卸载时传 null 清理）。 */
export function registerEditorTabOpener(fn: EditorTabOpener | null): void
```

## openViewTab —— 参数化动态视图

```ts
export interface OpenViewTabOptions {
  /** 插件 view 贡献点 id（必须已声明，container 任意——editor tab 全宽渲染）。 */
  viewId: string
  /** tab 标题（如文件路径 / commit 短哈希）。 */
  title: string
  /** Lucide 图标名（可选）。 */
  icon?: string
  /** 去重逻辑键：同 key 聚焦已有 tab，不同 key 开新 tab。 */
  key?: string
  /** 传给 view 组件的 props（如 { path, commit }）。 */
  params?: Record<string, unknown>
}
```

语义（VSCode webviewPanel 模型）：

- `viewId` 必须是已声明的 view 贡献点；无静态入口的视图声明 `dynamic: true`（被侧栏与布局注册表过滤——只能经 `openViewTab` 打开）。
- `key` 是 tab 去重逻辑键：同 key 聚焦已有 tab，不同 key 各开一个 tab（`PanelParams.viewKey` → `tabLogicalKey` → `plugin-view:${key}`）。
- `params` 作为 **props** 传给 view 组件——`PluginView` 把 `panelParams.viewParams` spread 到组件上（`<state.comp {...(viewParams ?? {})} />`）。
- dockview panel 的 component 必须用 `viewId`（`openTab` 设 `component: input.data.viewId ?? 'plugin'`）——`renderPluginView` 按 `view.id === component` 查找，泛型 `'plugin'` 永远查不到（历史空白 tab bug）。

`editorTabs.ts` 中的包装：

```ts
export function openEditorViewTab(options: OpenViewTabOptions): string {
  if (!opener) {
    console.warn('[plugin-runtime] openViewTab: editor tab opener 尚未注册（AppShell 未挂载）', options)
    return ''
  }
  return opener({
    type: 'plugin',
    title: options.title,
    icon: options.icon,
    closable: true,
    data: { viewId: options.viewId, viewKey: options.key, viewParams: options.params },
  })
}
```

## openFileTab —— 宿主文件编辑器

```ts
export interface OpenFileTabOptions {
  /** tab 标题（缺省取文件名）。 */
  title?: string
  /** 去重逻辑键（同 key 聚焦已有 tab；缺省按 path）。 */
  key?: string
  /** 打开后跳转到该行（1-based，居中显示）。 */
  line?: number
  /** 打开后高亮的行范围（start ≤ end）。 */
  highlight?: { startLine: number; endLine?: number }
  /** 覆盖语法高亮语言（缺省按文件扩展名推断）。 */
  language?: string
  /** 覆盖初始视图（仅 markdown 可 preview）。缺省按扩展名。 */
  viewMode?: 'editor' | 'preview'
}

openFileTab(path: string, opts?: OpenFileTabOptions): EditorHandle
```

返回 **EditorHandle** —— tab 打开后持续有效的命令式控制；tab 关闭后方法变 no-op 返回 `false`：

```ts
export interface EditorHandle {
  readonly editorId: string
  revealLine(line: number, opts?: { center?: boolean }): boolean
  revealRange(startLine: number, endLine: number): boolean
  setSelection(startLine: number, startCol?: number, endLine?: number, endCol?: number): boolean
  setCursorPosition(line: number, column?: number): boolean
  highlightLines(startLine: number, endLine?: number, opts?: { className?: string }): boolean
  clearHighlights(): boolean
  getContent(): string | null          // 内容编辑不落盘（与用户手动编辑一致）
  setContent(text: string): boolean
  setLanguage(language: string): boolean
  setTitle(title: string): boolean
  setViewMode(mode: 'editor' | 'preview'): boolean
  isVisible(): boolean
  close(): boolean
  onClose(cb: () => void): void
}
```

## openDiffTab —— 原生 Monaco diff 编辑器

```ts
export interface OpenDiffTabOptions {
  title: string
  /** 旧内容（左/上侧）。 */
  original: string
  /** 新内容（右/下侧）。 */
  modified: string
  /** 文件路径（宿主据此推断语法高亮语言，可选）。 */
  path?: string
  /** 去重逻辑键。 */
  key?: string
  /** 范围标注（如 "commit abc1234" / "工作区"）。 */
  scope?: string
}

export interface DiffHandle {
  readonly editorId: string
  nextDiff(): boolean
  prevDiff(): boolean
  setRenderSideBySide(sideBySide: boolean): boolean
  setTitle(title: string): boolean
  isVisible(): boolean
  close(): boolean
  onClose(cb: () => void): void
}
```

插件只传**两侧内容**——零渲染代码。宿主负责语言推断 + Monaco 渲染 + diff 导航（语法高亮、行级着色、并排/内联导航）。

## 确定性 editorId

```ts
export function editorIdForFile(path: string): string { return `ed-file:${path}` }
export function editorIdForDiff(diffKey: string): string { return `ed-diff:${diffKey}` }
```

id 确定性派生——同一文件 / 同一 diff key 的 id 恒定，因此：

- 重复 `open` 拿到**同一 handle**（无需会话级映射）。
- 刷新后布局恢复的 tab（params 携带 id）与 handle 天然对上。

## Handle 路由

```ts
/** FilePanel/DiffPanel 挂载时注册控制器；返回 detach。 */
export function attachEditor(editorId: string, controller: EditorController | DiffController): () => void
```

Handle 与实例**解耦**：每个方法执行时实时查注册表（`withEntry`）。tab 关闭（panel 卸载）→ 注册表 miss → no-op 返回 `false`——插件无需关心编辑器生命周期。`onClose` 回调在 panel detach 时广播（有守护：被新实例覆盖的 detach 不影响新实例）。

## 参考实现：xbot.git-fancy

`web/src/plugins/git-fancy/` 是典型的多入口 editor-view 插件：

- `index.tsx` —— 主入口 + 侧栏面板（变更文件列表 + 提交历史分页）。点击文件/commit 经 `openDiffTab` / `openViewTab` 打开主编辑区 tab。
- `diff.tsx` / `commit.tsx` —— 独立 view entry（必须声明为带各自 `entry` 的 view；用 esbuild `--splitting` 构建，`activate(ctx)` 注入的共享 `rpc`/`ui` 单例对每个 entry 可见）。
- 权限：manifest **必须包含实际用到的每个能力**——`openDiffTab` 走 `ctx.ui.openViewTab`，缺 `"ui"` 时 `ctx.ui` 为 undefined（buildContext 按权限注入），点击**静默无反应**（无报错无日志）。

## PluginUI 中的接线

`web/src/plugin-runtime/ui.ts` 的 `PluginUI.openViewTab/openFileTab/openDiffTab` 优先宿主实现（`UIServices.openViewTab?` 等，测试替身用），缺省走 `editorTabs` 注册器。
