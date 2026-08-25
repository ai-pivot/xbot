---
title: "声明式组件"
weight: 8
---

L1 声明式组件让插件用纯 JSON 声明视图——无需 ESM entry、无需 React 代码。运行时校验声明类型后渲染为 React。定义于 `web/src/plugin-api/components.ts`。

## ComponentProps —— 组件目录

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

## ComponentDecl —— 判别联合

```ts
/** 判别联合：{type:'badge', props:{…}} 的 props 精确收窄到对应类型。 */
export type ComponentDecl =
  { [T in keyof ComponentProps]: { type: T; props: ComponentProps[T] } }[keyof ComponentProps]

export type ComponentType = keyof ComponentProps
```

写 `{ type: 'badge', props: { value: 3 } }` 是编译错误——`value` 不是 `badge` 的 prop。收窄是结构性的：`type` 字面量选择对应的 `props` 形状。

## 声明 L1 视图

带 `component`（而非 `entry`）的 `ViewContribution` 即 L1 声明式视图：

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

## 渲染实现

渲染侧位于 `web/src/plugins/components.tsx`（与后端插件 widgets 共享）：JSON 声明经类型校验后由运行时渲染为 React 视图。引擎消费 `component` 时不知道具体插件——无按插件硬编码（与 view `align` 同规则：通用配置由引擎读取）。

## L1 vs entry

| | `component`（L1） | `entry`（完整） |
|---|---|---|
| 语言 | JSON 声明 | ESM React 模块 |
| 交互性 | 静态展示 | 完整 React（state、effect、RPC） |
| 适合 | 徽章、指标、状态展示 | 面板、编辑器、复杂 UI |
| 校验 | 编译期（`ComponentDecl`） | 模块加载 + ErrorBoundary |

两者可在同一插件共存——不同视图选择不同层级。

## 与布局系统组合

L1 视图与普通视图一样参与布局系统：自动注册为可移动布局项（默认 slot 由 `container` 映射），`align: 'start' | 'end'` 定位容器内对齐（`status_bar_right` 容器常配合 `align: 'end'` 把内容推到右侧，如 iteration-stats 徽章）。
