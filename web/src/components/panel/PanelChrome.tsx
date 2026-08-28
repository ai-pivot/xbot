/**
 * PanelChrome ——「一切皆面板」统一外壳（布局 v4/v5.1）。
 *
 * 内置面板与插件面板的唯一形态：标题栏（icon + title + sub + badge +
 * 停靠⇄浮动 + 取消钉选 + 折叠 + docked grip）+ 主体 + docked 底边调高 handle。
 * docked 与 floating 两套皮肤：
 *  - docked：rounded-xl + bg-white/[.02] + inset ring white/5%
 *  - floating：毛玻璃 rgba(17,20,29,.9) + backdrop-blur(14px) + 大阴影 + ring
 *
 * 图标统一经 pluginIcons.ts 的 pluginIcon 映射（与插件 view tab 一致）。
 */
import type { CSSProperties, PointerEvent as ReactPointerEvent, ReactNode } from 'react'
import { ChevronDown, ChevronRight, GripVertical, Inbox, PanelLeft, PictureInPicture2, X } from 'lucide-react'

import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import type { PanelBadge, PanelMode } from '@/plugin-api'

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
  onToggleCollapse: () => void
  /** 停靠⇄浮动切换。 */
  onToggleMode: () => void
  /** v5.1 docked 专属：取消钉选（✕ → 'chip'）。缺省不渲染 ✕（PINNED_DEFAULTS 面板不可取消钉选）。 */
  onUnpin?: () => void
  /** floating 专属：关闭浮窗（收回 chips）。 */
  onClose?: () => void
  /** docked grip 拖拽重排（pointerdown 起始）。 */
  onGripPointerDown?: (e: ReactPointerEvent<HTMLElement>) => void
  /** floating 标题栏拖动（pointerdown 起始；按钮区自动豁免）。 */
  onTitlePointerDown?: (e: ReactPointerEvent<HTMLElement>) => void
  /** 双击标题回启动器（floating 语义）。 */
  onTitleDoubleClick?: () => void
  /** floating 右下角 resize（pointerdown 起始）。 */
  onResizePointerDown?: (e: ReactPointerEvent<HTMLElement>) => void
  /** v5.1 docked 展开态底边调高 handle（pointerdown 起始；拖拽协议 v5）。 */
  onResizeHeightPointerDown?: (e: ReactPointerEvent<HTMLElement>) => void
  /** docked 拖拽重排的插入线位置（PanelLayout 计算）。 */
  dropIndicator?: 'before' | 'after' | null
  /** docked 拖拽中的源面板半透明。 */
  isDragSource?: boolean
  /** floating 绝对定位（left/top/width/height 由 PanelLayout 传）。 */
  style?: CSSProperties
  /** v5.1 docked 主体固定高度 px（loc.h 存在/钉选默认/调高拖拽跟随；内部滚动）。 */
  bodyHeight?: number | null
  /** docked 展开主体 max-height（px；null = 不设上限；bodyHeight 存在时不生效）。 */
  bodyMaxHeight?: number | null
  /** 空态协议：render(ctx) 返回 null 时 body 显示统一空态（此文案可自定义，默认「暂无内容」）。 */
  emptyHint?: string
  children: ReactNode
}

function iconButtonProps(title: string): { type: 'button'; 'aria-label': string; title: string } {
  return { type: 'button', 'aria-label': title, title }
}

/** 统一空态（空态协议宿主侧）：render(ctx) 返回 null 时显示。无边框、muted、居中。 */
function PanelEmpty({ hint }: { hint?: string }) {
  return (
    <div className="flex h-full min-h-16 flex-col items-center justify-center gap-1 py-3 text-center">
      <Inbox className="size-4 shrink-0 text-text-muted/50" />
      <span className="text-[10.5px] leading-relaxed text-text-muted/70">{hint || '暂无内容'}</span>
    </div>
  )
}

export function PanelChrome({
  id,
  icon,
  title,
  sub,
  badge,
  mode,
  collapsed,
  onToggleCollapse,
  onToggleMode,
  onUnpin,
  onClose,
  onGripPointerDown,
  onTitlePointerDown,
  onTitleDoubleClick,
  onResizePointerDown,
  onResizeHeightPointerDown,
  dropIndicator = null,
  isDragSource = false,
  style,
  bodyHeight,
  bodyMaxHeight,
  emptyHint,
  children,
}: PanelChromeProps) {
  const Icon = pluginIcon(icon)
  const floating = mode === 'floating'
  const stop = (e: ReactPointerEvent) => e.stopPropagation()

  const shellStyle: CSSProperties = floating
    ? {
        background: 'rgba(17,20,29,0.9)',
        backdropFilter: 'blur(14px)',
        WebkitBackdropFilter: 'blur(14px)',
        boxShadow: '0 16px 48px rgba(0,0,0,0.5), 0 2px 8px rgba(0,0,0,0.4), inset 0 0 0 1px rgba(255,255,255,0.08)',
        pointerEvents: 'auto',
        ...style,
      }
    : {
        background: 'rgba(255,255,255,0.02)',
        boxShadow: 'inset 0 0 0 1px rgba(255,255,255,0.05)',
        opacity: isDragSource ? 0.4 : undefined,
        pointerEvents: isDragSource ? 'none' : undefined,
        ...style,
      }

  return (
    <section
      data-panel-id={id}
      {...(!floating ? { 'data-dock-item': id } : {})}
      className={
        floating
          ? 'absolute flex flex-col overflow-hidden rounded-xl'
          // v5 规格 9：docked section overflow-hidden——flex 收缩时 body 溢出
          // 叠到相邻面板（重叠 corner case）。
          : 'relative flex min-h-0 flex-col overflow-hidden rounded-xl'
      }
      style={shellStyle}
    >
      {dropIndicator === 'before' && <div data-drop-indicator="before" className="absolute inset-x-1 top-0 h-0.5 shrink-0 rounded bg-app-accent" />}
      {dropIndicator === 'after' && <div data-drop-indicator="after" className="absolute inset-x-1 bottom-0 h-0.5 shrink-0 rounded bg-app-accent" />}
      {/* 标题栏 h-8。floating：整体可拖动（按钮豁免）；docked：grip 拖动。
          v5 规格 7：拖拽把手 touch-action:none（touch-none）防触摸滚动干扰。 */}
      <header
        className={`flex h-8 shrink-0 select-none items-center gap-1.5 px-2 ${floating ? 'cursor-move touch-none' : ''}`}
        onPointerDown={floating ? onTitlePointerDown : undefined}
        onDoubleClick={floating ? onTitleDoubleClick : undefined}
      >
        {/* eslint-disable-next-line react-hooks/static-components -- pluginIcon
            返回 lucide 映射表中的稳定图标组件引用（无状态），规则误报。 */}
        <Icon className="size-3 shrink-0" style={{ color: 'var(--text-muted)' }} />
        <span className="min-w-0 truncate text-[11.5px] font-semibold" style={{ color: 'var(--text-primary)' }}>
          {title}
        </span>
        {sub ? <span className="shrink-0 font-mono text-[9.5px] text-text-muted">{sub}</span> : null}
        <span className="min-w-2 flex-1" />
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
        <button
          {...iconButtonProps(floating ? '收回启动器' : '浮动')}
          onPointerDown={stop}
          onClick={onToggleMode}
          className="flex shrink-0 items-center rounded p-1 text-text-muted transition-colors hover:bg-white/5 hover:text-text-secondary"
        >
          {floating ? <PanelLeft className="size-3" /> : <PictureInPicture2 className="size-3" />}
        </button>
        {!floating && onUnpin ? (
          <button
            {...iconButtonProps('取消钉选（收入底部启动器）')}
            onPointerDown={stop}
            onClick={onUnpin}
            className="flex shrink-0 items-center rounded p-1 text-text-muted transition-colors hover:bg-white/5 hover:text-text-primary"
          >
            <X className="size-3" />
          </button>
        ) : null}
        {floating && onClose ? (
          <button
            {...iconButtonProps('关闭浮窗（收入启动器）')}
            onPointerDown={stop}
            onClick={onClose}
            className="flex shrink-0 items-center rounded p-1 text-text-muted transition-colors hover:bg-white/5 hover:text-text-primary"
          >
            <X className="size-3" />
          </button>
        ) : null}
        <button
          {...iconButtonProps(collapsed ? '展开' : '折叠')}
          onPointerDown={stop}
          onClick={onToggleCollapse}
          className="flex shrink-0 items-center rounded p-1 text-text-muted transition-colors hover:bg-white/5 hover:text-text-secondary"
        >
          {collapsed ? <ChevronRight className="size-3" /> : <ChevronDown className="size-3" />}
        </button>
        {!floating && onGripPointerDown ? (
          <span
            role="button"
            aria-label="拖拽重排面板"
            title="拖拽重排（拖出左栏变浮动）"
            onPointerDown={onGripPointerDown}
            className="ml-0.5 flex shrink-0 cursor-grab touch-none items-center rounded p-0.5 text-text-muted active:cursor-grabbing hover:text-text-secondary"
          >
            <GripVertical className="size-3" />
          </span>
        ) : null}
      </header>
      {!collapsed && (
        <div
          className={floating ? 'min-h-0 flex-1 overflow-y-auto' : 'min-h-0 overflow-y-auto'}
          style={
            !floating
              ? (bodyHeight != null
                ? { height: bodyHeight }
                : bodyMaxHeight != null
                  ? { maxHeight: bodyMaxHeight }
                  : undefined)
              : undefined
          }
        >
          {/* 空态协议：面板 render(ctx) 返回 null → 统一空态占位（无边框，
              消灭"空边框"渲染——协议约定，插件按自身数据自行返回 null）。 */}
          {children ?? <PanelEmpty hint={emptyHint} />}
        </div>
      )}
      {/* v5.1 docked 展开态底边调高 handle：7px 高，hover 显 accent 条（设计稿样式）。
          拖拽协议 v5：pointerdown 起始 + pointer capture + touch-none，move 零持久化。 */}
      {!floating && !collapsed && onResizeHeightPointerDown ? (
        <span
          role="separator"
          aria-label="调整面板高度"
          data-testid="panel-height-handle"
          onPointerDown={onResizeHeightPointerDown}
          className="group flex h-[7px] shrink-0 cursor-ns-resize touch-none items-center justify-center"
        >
          <span className="h-[3px] w-10 rounded-full bg-white/10 transition-colors group-hover:bg-app-accent" />
        </span>
      ) : null}
      {floating && onResizePointerDown ? (
        <span
          role="separator"
          aria-label="调整面板大小"
          onPointerDown={onResizePointerDown}
          className="absolute bottom-0 right-0 z-10 size-3 cursor-nwse-resize touch-none"
          style={{
            background:
              'linear-gradient(135deg, transparent 0 50%, rgba(255,255,255,0.18) 50% 60%, transparent 60% 72%, rgba(255,255,255,0.18) 72% 84%, transparent 84%)',
          }}
        />
      ) : null}
    </section>
  )
}
