/**
 * PanelChrome ——「一切皆面板」统一外壳（布局 v4/v5.1）。
 *
 * 内置面板与插件面板的唯一形态：标题栏（icon + title + sub + badge +
 * 停靠⇄浮动 + 取消钉选 + 折叠 + docked grip）+ 主体 + docked 底边调高 handle。
 * docked 与 floating 两套皮肤：
 *  - docked：rounded-xl + bg-bg-secondary + inset ring white/5%
 *  - floating：毛玻璃 bg-primary 90% + backdrop-blur(14px) + 大阴影 + var(--border) ring
 *
 * 图标统一经 pluginIcons.ts 的 pluginIcon 映射（与插件 view tab 一致）。
 *
 * 皮肤全部走 theme 语义 token（light/dark/glass 均自适应）：
 *  - docked：bg-secondary（微亮/微暗于侧栏底 bg-primary）+ var(--border) inset ring
 *  - floating：bg-primary 90% 半透明毛玻璃 + backdrop-blur + var(--border) ring
 *    （glass 模式下 --bg-primary 被 AmbienceBackground 覆盖为半透明，浮窗自动玻璃化）
 */
import type { CSSProperties, PointerEvent as ReactPointerEvent, ReactNode } from 'react'
import { ChevronRight, Inbox } from 'lucide-react'

import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import type { PanelBadge, PanelMode } from '@/plugin-api'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useI18n } from '@/providers/i18n'

export interface PanelChromeProps {
  /** 面板 id（data-dock-item 定位 + 拖拽数据）。 */
  id: string
  icon: string
  title: string
  /** 标题右侧的次要信息（mono 小字，如任务计数）。 */
  sub?: string
  badge?: PanelBadge | null
  mode: PanelMode
  collapsed: boolean
  /**
   * v5.3 常驻面板（PINNED_DEFAULTS/core.sessions）：**不可折叠、不可浮窗**——它是
   * 左栏的常驻内容，任何"离开左栏/从堆叠消失"的动作都只留下一个空左栏，用户看到
   * 的是"点一下会话面板没了"（2026-09-15：「sessions 这一行还有一个有完全一样的 bug
   * 的按钮」）。这两颗按钮因此不渲染（状态层 `enforcePinnedState` 同时兜底不变量）。
   */
  pinned?: boolean
  onToggleCollapse: () => void
  /** v5.1 docked 展开态底边调高 handle（pointerdown 起始；拖拽协议 v5）。 */
  onResizeHeightPointerDown?: (e: ReactPointerEvent<HTMLElement>) => void
  /** docked 拖拽重排的插入线位置（PanelLayout 计算）。 */
  /**
   * 标题行扩展节点（渲染在标题右侧、面板按钮左侧）。
   * 面板定义 `headerExtra(ctx)` 的产物经 PanelLayout 传入（见 plugin-api/panels.ts）。
   */
  headerExtra?: ReactNode
  /** floating 绝对定位（left/top/width/height 由 PanelLayout 传）。 */
  style?: CSSProperties
  /** 空态协议：render(ctx) 返回 null 时 body 显示统一空态（此文案可自定义，默认「暂无内容」）。 */
  emptyHint?: string
  children: ReactNode
}

function iconButtonProps(title: string): { type: 'button'; 'aria-label': string; title: string } {
  return { type: 'button', 'aria-label': title, title }
}

/** 统一空态（空态协议宿主侧）：render(ctx) 返回 null 时显示。无边框、muted、居中。 */
function PanelEmpty({ hint }: { hint?: string }) {
  const { t } = useI18n()
  return (
    <div className="flex h-full min-h-16 flex-col items-center justify-center gap-1 py-3 text-center">
      <Inbox className="size-4 shrink-0 text-text-muted/50" />
      <span className="text-[10.5px] leading-relaxed text-text-muted/70">{hint || t('panel.empty')}</span>
    </div>
  )
}

export function PanelChrome({
  id,
  icon,
  title,
  sub,
  badge,
  collapsed,
  pinned = false,
  headerExtra,
  onToggleCollapse,
  onResizeHeightPointerDown,
  style,
  emptyHint,
  children,
}: PanelChromeProps) {
  const isTouch = useIsTouch()
  const { t } = useI18n()
  const Icon = pluginIcon(icon)
  const stop = (e: ReactPointerEvent) => e.stopPropagation()

  // 皮肤走 theme token（light/dark/glass 自适应）：docked = bg-secondary
  // （相对侧栏底 bg-primary 微亮/微暗形成层次）。阴影保持黑色系。
  // 浮窗皮肤已删除（2026-09-20 用户要求：删掉悬浮窗口）。
  const shellStyle: CSSProperties = {
    background: 'var(--bg-secondary)',
    boxShadow: 'inset 0 0 0 1px var(--border)',
    ...style,
  }

  return (
    <section
      data-panel-id={id}
      data-dock-item={id}
      className="relative flex min-h-0 flex-col overflow-hidden shadow-[inset_0_-1px_0_0_rgba(255,255,255,0.055)]"
      style={shellStyle}
    >
      {/* 标题栏 h-8。floating：整体可拖动（按钮豁免）；docked：grip 拖动。
          v5 规格 7：拖拽把手 touch-action:none（touch-none）防触摸滚动干扰。 */}
      {/* ⛔ 标题栏【不再是隐藏的折叠点击区】：点击标题文字/图标/空白处一律不折叠
          （2026-09-15 用户：「点 `Sessions` 这个词有bug，别的位置没有」——侧栏里唯一
          展开的会话面板被文字点击收掉后，左栏只剩一片黑，看起来像坏了）。折叠只有
          一个显式控件：⌄ 按钮（+ 左侧图标栏点击激活项 = 收起整栏）。 */}
      <header
        className={`group/header flex h-9 shrink-0 select-none items-center gap-1.5 border-l-2 border-l-transparent px-2 transition-spring hover:border-l-app-accent/60 hover:bg-bg-tertiary/30 ${!collapsed ? 'bg-bg-tertiary/15' : ''}`}
      >
        {/* eslint-disable-next-line react-hooks/static-components -- pluginIcon
            返回 lucide 映射表中的稳定图标组件引用（无状态），规则误报。 */}
        <Icon className="size-3.5 shrink-0" style={{ color: 'var(--text-muted)' }} />
        <span data-testid="panel-title" className="min-w-0 truncate text-xs font-semibold" style={{ color: 'var(--text-primary)' }}>
          {title}
        </span>
        {sub ? <span className="shrink-0 font-mono text-[9.5px] text-text-muted">{sub}</span> : null}
        <span className="min-w-2 flex-1" />
        {headerExtra}
        {badge ? (
          <span
            className="shrink-0 rounded-full px-1.5 py-px text-[9px] font-semibold leading-4"
            style={{
              background: `color-mix(in srgb, ${badge.color} 22%, transparent)`,
              color: badge.color,
            }}
          >
            {badge.text}
          </span>
        ) : null}
        {/* 停靠⇄浮动 / 取消钉选 / 关闭浮窗按钮已删除（2026-09-20 用户要求：
            删掉悬浮窗口和拖拽逻辑）。面板只能 docked 在侧栏堆叠里。 */}
        {/* 折叠：常驻面板（pinned）不渲染——收起后它连同自己的 header 一起从堆叠
            消失，左栏只剩空态提示（用户报的"点一下会话面板没了"）。 */}
        {!pinned ? (
          <button
            {...iconButtonProps(collapsed ? t('panel.expand') : t('panel.collapse'))}
            onPointerDown={stop}
            onClick={onToggleCollapse}
            className={`flex shrink-0 items-center rounded-md p-2 text-text-muted transition-spring hover:bg-bg-tertiary/60 hover:text-text-secondary active:scale-90 hover:[&_svg]:scale-110 [&_svg]:transition-transform [&_svg]:duration-200 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-app-accent/50 ${isTouch ? '' : 'opacity-0 group-hover/header:opacity-100'}`}
          >
            <ChevronRight
              className={`size-3.5 shrink-0 transition-transform duration-200 ${collapsed ? '' : 'rotate-90'}`}
            />
          </button>
        ) : null}
      </header>
      {/* 折叠时用 display:none 而非条件渲染——保留 children 的 React state
          （useState/useEffect/订阅不卸载），展开时恢复（git-fancy 的 commit
          accordion 展开、技能面板的子项展开等不再丢失）。 */}
      <div
        className={`min-h-0 flex-1 overflow-y-auto overscroll-contain px-2.5 py-2 ${!collapsed ? 'border-t border-border/25 bg-bg-secondary/25' : ''}`}
        style={{
          display: collapsed ? 'none' : undefined,
        }}
      >
        {/* 空态协议：面板 render(ctx) 返回 null → 统一空态占位（无边框，
            消灭"空边框"渲染——协议约定，插件按自身数据自行返回 null）。 */}
        {children ?? <PanelEmpty hint={emptyHint} />}
      </div>
      {/* v5.1 docked 展开态底边调高 handle：7px 高，hover 显 accent 条（设计稿样式）。
          拖拽协议 v5：pointerdown 起始 + pointer capture + touch-none，move 零持久化。 */}
      {!collapsed && onResizeHeightPointerDown ? (
        <span
          role="separator"
          aria-label={t('panel.resizeHeight')}
          data-testid="panel-height-handle"
          onPointerDown={onResizeHeightPointerDown}
          className="group flex h-[7px] shrink-0 cursor-ns-resize touch-none items-center justify-center"
        >
          <span className="h-[3px] w-10 rounded-full bg-text-muted/30 transition-colors group-hover:bg-app-accent" />
        </span>
      ) : null}
    </section>
  )
}
