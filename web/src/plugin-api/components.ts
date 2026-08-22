/**
 * 声明式组件（L1）——props 类型随 type 精确收窄。
 *
 * 与 `web/src/plugins/components.tsx` 的渲染实现对应：JSON 声明经类型校验后
 * 由运行时渲染为 React 视图。
 */
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

/** 判别联合：`{type:'badge', props:{…}}` 的 props 精确收窄到对应类型。 */
export type ComponentDecl =
  { [T in keyof ComponentProps]: { type: T; props: ComponentProps[T] } }[keyof ComponentProps]

export type ComponentType = keyof ComponentProps
