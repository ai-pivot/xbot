---
title: "消息渲染器"
weight: 7
---

消息渲染器让插件替换聊天流中工具结果与消息的渲染方式。核心类型思想：**`matches` 匹配条件精化 `render` 收到的消息类型**——"匹配了什么，就能安全处理什么"。定义于 `web/src/plugin-api/renderer.ts`。

## ToolResultMap —— 工具结果类型表

```ts
/** 工具结果类型表（核心内置；后端插件用声明合并扩展）。 */
export interface ToolResultMap {
  display_html: { code: string; summary: string }
  shell: { command: string; output: string; exitCode: number }
  web_search: { query: string; results: readonly unknown[] }
  git_status: {
    branch: string
    dirty: boolean
    changes: number
    ahead: number
    behind: number
  }
}
```

## Matcher

```ts
export type Matcher =
  | { tool: keyof ToolResultMap }
  // metadata-driven 匹配：UI 能力由工具元数据声明，不由工具名决定。
  // 如 { uiMode: 'genui' } 匹配任何声明 ui.mode === 'genui' 的工具。
  | { uiMode: string }
  | { role: 'assistant' | 'user' | 'system' }
  // 通用匹配：空对象。（Record<string, never> 保证 excess property 检查生效，
  // 裸 {} 会吞掉 {role:'admin'} 之类的非法字面量。）
  | Record<string, never>
```

## MatchedMessage —— 条件类型精化

```ts
export type MatchedMessage<M extends Matcher> =
  M extends { tool: infer T extends keyof ToolResultMap }
    ? SafeMessage & { tool: { name: T; result: ToolResultMap[T] } }
    : M extends { uiMode: string }
      ? SafeMessage & { tool: { name: string; uiMode: string; result: unknown } }
      : M extends { role: 'assistant' }
        ? SafeAssistantMessage
        : M extends { role: 'user' }
          ? SafeUserMessage
          : SafeMessage
```

声明 `matches: { tool: 'shell' }` 的渲染器收到的 `tool.result` 类型为 `{ command: string; output: string; exitCode: number }`——零 cast。

## MessageRendererContribution

```ts
export interface MessageRendererContribution<M extends Matcher = Matcher> {
  kind: 'messageRenderer'
  id: string
  /** 大者优先；render 返回 null 时继续 fallback 到下一个。 */
  priority: number
  matches: M
  /** msg 类型由 matches 精化。 */
  render: (msg: MatchedMessage<M>, ctx: RenderContext) => ReactNode | null
}

/** 渲染上下文：当前会话、可用 UI 原语。 */
export interface RenderContext {
  chatID: string
  /** 渲染到某个已声明视图 slot（返回 JSX 由运行时挂载）。可选。 */
  renderTo?: (slotId: string) => ReactNode
  /** 本地 UI 状态（如折叠）。 */
  collapsed?: boolean
}
```

## 派发：`PluginRuntime.renderTool`

派发（`web/src/plugin-runtime/index.ts`）把宿主注册的内置渲染器（如 GenUI）与插件渲染器合并为一条优先级链：

```ts
renderTool(tool: ToolRenderInput, ctx: RenderContext): ReactNode | null {
  const renderers = [
    ...this.builtinRenderers,
    ...this.registry.listAllRenderers().map((r) => r.renderer),
  ].sort((a, b) => b.priority - a.priority)

  for (const renderer of renderers) {
    if (!matchesTool(renderer.matches, tool)) continue
    const msg = { tool: { name: tool.name ?? '', uiMode: tool.uiMode, result: tool } }
    try {
      const node = renderer.render(msg as never, ctx)
      if (node != null) return node
    } catch (error) {
      // 渲染器崩溃只降级到默认渲染，不崩整个消息。
      console.error(`[plugin-runtime] 渲染器 ${renderer.id} 崩溃:`, error)
    }
  }
  return null   // 宿主回退默认渲染
}
```

匹配是 metadata-driven 的（`matchesTool`）：

```ts
export function matchesTool(matcher: Matcher, tool: ToolRenderInput): boolean {
  if ('tool' in matcher) return tool.name === matcher.tool
  if ('uiMode' in matcher) return tool.uiMode === matcher.uiMode
  if ('role' in matcher) return false   // 工具不是消息角色
  return true
}

export interface ToolRenderInput {
  name?: string
  /** UI 能力模式（来自 UIDecl 元数据，如 "genui"）。 */
  uiMode?: string
  detail?: string
  args?: string
  summary?: string
}
```

注册表侧，`listAllRenderers()` 返回跨插件的全部渲染器，预按 `priority` 降序稳定排序。渲染器**id 重名合法**（校验门控中 `kind === 'messageRenderer'` 豁免 id 唯一性检查）——优先级链即机制。

## 为什么有 `{ uiMode }`

历史上宿主硬编码工具名（`display_html`）决定渲染。`{ uiMode }` 匹配删除这一硬编码：UI 能力由工具**元数据**声明（`UIDecl.mode`，如 `genui`），渲染器按声明的 mode 匹配。新增 UI 能力工具无需改渲染器。

## 示例：fancy shell 渲染

```ts
import type { MessageRendererContribution, RenderContext } from '@xbot/plugin-api'

export const shellRenderer = {
  kind: 'messageRenderer',
  id: 'xbot.demo.shell-renderer',
  priority: 10,
  matches: { tool: 'shell' },
  render: (msg, ctx: RenderContext) => {
    // msg.tool.result: { command: string; output: string; exitCode: number }
    const { command, output, exitCode } = msg.tool.result
    if (!output) return null   // fallback 到下一个渲染器
    return (
      <div>
        <code>{command}</code>
        <pre>{output}</pre>
        {exitCode !== 0 && <span>EXIT {exitCode}</span>}
      </div>
    )
  },
} satisfies MessageRendererContribution
```

宿主的 `ToolRender` 同时收集 `ToolHints`（如 Edit diff）——渲染器可自由读 `msg.tool.result`，但精化类型只承诺 `ToolResultMap` 形状。
