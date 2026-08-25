---
title: "Component Types"
weight: 10
---

Reference for the declarative (L1) component types used by web plugin views (`web/src/plugin-api/components.ts`). A `ComponentDecl` is a discriminated union: `{ type: T, props: ComponentProps[T] }` — the props type narrows precisely by `type`.

## ComponentProps

| Type | Props Shape | Description |
|------|-------------|-------------|
| `badge` | `{ text: string; color?: 'green'\|'red'\|'blue'\|'amber'\|'gray'\|'indigo'\|'slate'; dot?: boolean; action?: string; data?: unknown }` | A status badge. `action` + `data` enable click interactions. |
| `progress` | `{ value: number; max?: number; label?: string; tone?: string; show_value?: boolean }` | A progress bar. |
| `metric` | `{ label: string; value: string \| number; delta?: string; tone?: string; icon?: string; action?: string; data?: unknown }` | A labeled metric with optional delta, icon, and click action. |
| `sparkline` | `{ data: number[]; color?: string; type?: 'line' \| 'bar'; height?: number }` | A mini chart. |
| `table` | `{ columns: Array<{ key: string; label: string }>; data: Array<Record<string, unknown>>; maxHeight?: number }` | A data table. |
| `list` | `{ items: Array<{ text: string; tone?: string; icon?: string }> }` | A simple item list. |
| `markdown` | `{ text: string }` | Rendered markdown. |
| `code` | `{ code: string; language?: string }` | A code block with optional language highlighting. |

## Type Definitions

```ts
export interface ComponentProps {
  badge: { text: string; color?: ...; dot?: boolean; action?: string; data?: unknown }
  progress: { value: number; max?: number; label?: string; tone?: string; show_value?: boolean }
  metric: { label: string; value: string | number; delta?: string; tone?: string; icon?: string; action?: string; data?: unknown }
  sparkline: { data: number[]; color?: string; type?: 'line' | 'bar'; height?: number }
  table: { columns: Array<{ key: string; label: string }>; data: Array<Record<string, unknown>>; maxHeight?: number }
  list: { items: Array<{ text: string; tone?: string; icon?: string }> }
  markdown: { text: string }
  code: { code: string; language?: string }
}

/** Discriminated union: {type:'badge', props:{…}} narrows props precisely. */
export type ComponentDecl =
  { [T in keyof ComponentProps]: { type: T; props: ComponentProps[T] } }[keyof ComponentProps]

export type ComponentType = keyof ComponentProps
```

## Usage in Views

A `ViewContribution` can declare an L1 view inline (no ESM entry needed):

```ts
{
  kind: 'view',
  id: 'my-plugin.stats',
  container: 'status_bar_right',
  title: 'Stats',
  component: { type: 'metric', props: { label: 'Tokens', value: '12k' } }
}
```

The JSON declaration is type-checked by the runtime and rendered as a React view (implementation in `web/src/plugins/components.tsx`).
