---
title: "Message Renderer"
weight: 7
---

Message renderers let plugins replace how tool results and messages render in the chat flow. The core type-level idea: **the `matches` condition refines the type of the message the `render` function receives** — "what you match is what you can safely handle". Defined in `web/src/plugin-api/renderer.ts`.

## ToolResultMap — the tool result type table

```ts
/** Tool result type table (core built-ins; backend plugins extend via declaration merging). */
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
  // metadata-driven matching: UI capability is declared by TOOL METADATA, not the
  // tool name. E.g. { uiMode: 'genui' } matches any tool declaring ui.mode === 'genui'.
  | { uiMode: string }
  | { role: 'assistant' | 'user' | 'system' }
  // Universal match: empty object. (Record<string, never> keeps excess-property
  // checks active — a bare {} would swallow illegal literals like {role:'admin'}.)
  | Record<string, never>
```

## MatchedMessage — conditional-type refinement

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

A renderer declaring `matches: { tool: 'shell' }` gets `msg.tool.result` typed as `{ command: string; output: string; exitCode: number }` — zero casts.

## MessageRendererContribution

```ts
export interface MessageRendererContribution<M extends Matcher = Matcher> {
  kind: 'messageRenderer'
  id: string
  /** Higher wins; render returning null falls back to the next renderer. */
  priority: number
  matches: M
  /** msg type is refined by matches. */
  render: (msg: MatchedMessage<M>, ctx: RenderContext) => ReactNode | null
}

/** Render context: current session + available UI primitives. */
export interface RenderContext {
  chatID: string
  /** Render into a declared view slot (returns JSX mounted by the runtime). Optional. */
  renderTo?: (slotId: string) => ReactNode
  /** Local UI state (e.g. collapsed). */
  collapsed?: boolean
}
```

## Dispatch: `PluginRuntime.renderTool`

The dispatch (`web/src/plugin-runtime/index.ts`) merges host-registered built-in renderers (e.g. GenUI) with plugin renderers into one priority chain:

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
      // A crashing renderer only degrades to default rendering — never crashes the message.
      console.error(`[plugin-runtime] 渲染器 ${renderer.id} 崩溃:`, error)
    }
  }
  return null   // host falls back to default rendering
}
```

Matching is metadata-driven (`matchesTool`):

```ts
export function matchesTool(matcher: Matcher, tool: ToolRenderInput): boolean {
  if ('tool' in matcher) return tool.name === matcher.tool
  if ('uiMode' in matcher) return tool.uiMode === matcher.uiMode
  if ('role' in matcher) return false   // tools are not message roles
  return true
}

export interface ToolRenderInput {
  name?: string
  /** UI capability mode (from UIDecl metadata, e.g. "genui"). */
  uiMode?: string
  detail?: string
  args?: string
  summary?: string
}
```

Registry-side, `listAllRenderers()` returns all renderers across plugins, pre-sorted by `priority` descending (stable). Duplicate renderer **ids** are legal in the validation gate (`kind === 'messageRenderer'` is exempt from the ID-uniqueness check) — the priority chain is the mechanism.

## Why `{ uiMode }` exists

Historically the host hard-coded tool names (`display_html`) to decide rendering. The `{ uiMode }` matcher deletes that: UI capability is declared by tool **metadata** (`UIDecl.mode`, e.g. `genui`), and renderers match on the declared mode. Adding a new UI-capable tool requires zero renderer changes.

## Example: fancy shell rendering

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
    if (!output) return null   // fall back to the next renderer
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

The host's `ToolRender` collects `ToolHints` (e.g. Edit diffs) alongside — renderers may read `msg.tool.result` freely, but the refined type only promises the `ToolResultMap` shape.
