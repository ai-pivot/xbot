---
title: "Declarative Components"
weight: 8
---

L1 declarative components let a plugin declare a view with pure JSON — no ESM entry, no React code. The runtime type-checks the declaration and renders it as React. Defined in `web/src/plugin-api/components.ts`.

## ComponentProps — the component catalog

```ts
export interface ComponentProps {
  badge: {
    text: string
    color?: 'green' | 'red' | 'blue' | 'amber' | 'gray' | 'indigo' | 'slate'
    dot?: boolean
    action?: string
    data?: unknown
  }
  progress: { value: number; max?: number; label?: string; tone?: string; show_value?: boolean }
  metric: {
    label: string
    value: string | number
    delta?: string
    tone?: string
    icon?: string
    action?: string
    data?: unknown
  }
  sparkline: { data: number[]; color?: string; type?: 'line' | 'bar'; height?: number }
  table: {
    columns: Array<{ key: string; label: string }>
    data: Array<Record<string, unknown>>
    maxHeight?: number
  }
  list: { items: Array<{ text: string; tone?: string; icon?: string }> }
  markdown: { text: string }
  code: { code: string; language?: string }
}
```

## ComponentDecl — discriminated union

```ts
/** Discriminated union: {type:'badge', props:{…}} narrows props to the exact type. */
export type ComponentDecl =
  { [T in keyof ComponentProps]: { type: T; props: ComponentProps[T] } }[keyof ComponentProps]

export type ComponentType = keyof ComponentProps
```

Writing `{ type: 'badge', props: { value: 3 } }` is a compile error — `value` is not a `badge` prop. The narrowing is structural: the `type` literal selects the matching `props` shape.

## Declaring an L1 view

A `ViewContribution` with `component` (instead of `entry`) is an L1 declarative view:

```ts
export const manifest = {
  id: 'xbot.demo',
  name: 'Demo',
  version: '0.1.0',
  contributes: [
    {
      kind: 'view',
      id: 'demo.stats',
      container: 'info_bar',
      title: 'Stats',
      component: {
        type: 'metric',
        props: { label: 'Turns', value: 12, delta: '+3', tone: 'green' },
      },
    },
  ] as const,
} satisfies PluginManifest
```

## Rendering implementation

The render side lives in `web/src/plugins/components.tsx` (shared with backend plugin widgets): JSON declarations are validated and rendered as React views. The engine consumes `component` without knowing the plugin — no per-plugin hard-coding (the same rule as view `align`: generic configuration read by the engine).

## L1 vs entry

| | `component` (L1) | `entry` (full) |
|---|---|---|
| Language | JSON declaration | ESM React module |
| Interactivity | static display | full React (state, effects, RPC) |
| Good for | badges, metrics, status displays | panels, editors, complex UIs |
| Validation | compile-time via `ComponentDecl` | module load + ErrorBoundary |

Both may coexist in one plugin — different views can choose different levels.

## Composition with layout

An L1 view participates in the layout system like any other view: it auto-registers as a movable layout item (default slot mapped from `container`), and `align: 'start' | 'end'` positions it inside the container (`status_bar_right` containers commonly pair with `align: 'end'` to push content right, e.g. iteration-stats badges).
