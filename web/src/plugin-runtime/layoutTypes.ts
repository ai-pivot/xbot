/**
 * Layout slot system —— VSCode 式布局定制核心类型。
 *
 * 设计：每个 UI 容器是一个命名 slot（如手机底部导航 mobile.bottom_nav、
 * 手机顶栏 mobile.top_bar、桌面侧栏 desktop.sidebar）。布局项（内置按钮、
 * 插件 view 等）注册到默认 slot，用户可通过布局设置把项移到其他 slot，
 * 配置持久化到 localStorage（key: xbot:layout:overrides）。
 *
 * 插件接入：view 贡献点自动注册为布局项（id = view.id，默认 slot 由
 * container 映射）；插件也可通过 `contributes.layout` 声明自定义布局项。
 */

/** 命名布局槽位。 */
export type LayoutSlotId =
  /** 手机底部导航条（会话/工具按钮所在）。 */
  | 'mobile.bottom_nav'
  /** 手机顶栏右侧操作区（+ / 设置等）。 */
  | 'mobile.top_bar'
  /** 桌面左侧 ActivityBar。 */
  | 'desktop.activity_bar'
  /** 桌面右侧边栏面板 tab。 */
  | 'desktop.sidebar'
  /** 桌面底部 InfoBar。 */
  | 'desktop.info_bar'

/** 布局项——可放置到某个 slot 的 UI 元素。 */
export interface LayoutItem {
  /** 唯一 id（内置项如 "mobile.view.agent"，插件 view 如 "xbot.git-fancy.panel"）。 */
  id: string
  /** 默认 slot。 */
  slot: LayoutSlotId
  /** 显示标题。 */
  title: string
  /** i18n key（可选）：优先于 title 做界面翻译（内置项保持本地化）。 */
  labelKey?: string
  /** 可选 lucide 图标名。 */
  icon?: string
  /** 排序权重（同 slot 内升序，默认 0）。 */
  weight?: number
  /** 是否允许用户移动（默认 true；核心按钮如会话可移动但可标记重要）。 */
  movable?: boolean
}

/** 用户布局覆盖：itemId → 目标 slot（未列出的项留在默认 slot）。 */
export type LayoutOverrides = Record<string, LayoutSlotId>

/** 内置布局项 id 常量。 */
export const BUILTIN_LAYOUT_ITEMS = {
  /** 手机「会话」按钮（切到 Agent 视图）。 */
  mobileAgent: 'mobile.view.agent',
  /** 手机「工具」按钮（切到工具/detail 视图）。 */
  mobileTools: 'mobile.view.tools',
  /** 手机顶栏「+ 新会话」。 */
  mobileNewChat: 'mobile.action.new_chat',
  /** 手机顶栏「设置」。 */
  mobileSettings: 'mobile.action.settings',
  /** 桌面左侧 ActivityBar「会话列表」。 */
  desktopSessions: 'desktop.activity.sessions',
  /** 桌面右侧边栏「文件」。 */
  desktopFiles: 'desktop.sidebar.files',
  /** 桌面右侧边栏「搜索」。 */
  desktopSearch: 'desktop.sidebar.search',
  /** 桌面右侧边栏「信息」。 */
  desktopInfo: 'desktop.sidebar.info',
  /** 桌面右侧边栏「任务」。 */
  desktopTasks: 'desktop.sidebar.tasks',
  /** 桌面右侧边栏「终端」。 */
  desktopTerminal: 'desktop.sidebar.terminal',
} as const

/** localStorage 覆盖配置 key。 */
export const LAYOUT_OVERRIDES_KEY = 'xbot:layout:overrides'
