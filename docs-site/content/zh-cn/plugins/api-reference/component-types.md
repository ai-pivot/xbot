---
title: "组件类型"
weight: 10
---

Web 插件视图使用的声明式（L1）组件类型参考（`web/src/plugin-api/components.ts`）。`ComponentDecl` 是判别联合：`{ type: T, props: ComponentProps[T] }`——props 类型随 `type` 精确收窄。

## ComponentProps

| 类型 | Props 形状 | 说明 |
|------|-----------|------|
| `badge` | `{ text: string; color?: 'green'\|'red'\|'blue'\|'amber'\|'gray'\|'indigo'\|'slate'; dot?: boolean; action?: string; data?: unknown }` | 状态徽章。`action` + `data` 启用点击交互。 |
| `progress` | `{ value: number; max?: number; label?: string; tone?: string; show_value?: boolean }` | 进度条。 |
| `metric` | `{ label: string; value: string \| number; delta?: string; tone?: string; icon?: string; action?: string; data?: unknown }` | 带标签的度量值，可选 delta、图标与点击动作。 |
| `sparkline` | `{ data: number[]; color?: string; type?: 'line' \| 'bar'; height?: number }` | 迷你图表。 |
| `table` | `{ columns: Array<{ key: string; label: string }>; data: Array<Record<string, unknown>>; maxHeight?: number }` | 数据表格。 |
| `list` | `{ items: Array<{ text: string; tone?: string; icon?: string }> }` | 简单列表。 |
| `markdown` | `{ text: string }` | 渲染 markdown。 |
| `code` | `{ code: string; language?: string }` | 代码块，可选语言高亮。 |

## 类型定义

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

/** 判别联合：{type:'badge', props:{…}} 的 props 精确收窄到对应类型。 */
export type ComponentDecl =
  { [T in keyof ComponentProps]: { type: T; props: ComponentProps[T] } }[keyof ComponentProps]

export type ComponentType = keyof ComponentProps
```

## 在视图中使用

`ViewContribution` 可内联声明 L1 视图（无需 ESM entry）：

```ts
{
  kind: 'view',
  id: 'my-plugin.stats',
  container: 'status_bar_right',
  title: 'Stats',
  component: { type: 'metric', props: { label: 'Tokens', value: '12k' } }
}
```

JSON 声明经运行时类型校验后渲染为 React 视图（实现位于 `web/src/plugins/components.tsx`）。
