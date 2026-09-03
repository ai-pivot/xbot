/**
 * rails —— 布局 v5 徽章 rail 宿主（TopRail / BottomRailBadges）+ v5.1
 * SideChips 底部启动器 + BadgeSlot 徽章槽位宽度锁定。
 *
 * top/bottom zone 的面板渲染为徽章（badge）。规格硬性要求「TopRail 定界」：
 * rail 是 flex 容器，max-width 由消费方 className 定；内部 ResizeObserver 测
 * 可用宽，徽章依次测量，放不下从尾部收进 ＋N 菜单（popover 列出被收纳徽章，
 * 点击项 = 徽章 popover）——绝不溢出/推挤消费方布局（容器 overflow-hidden +
 * min-w-0，尺寸变化只影响收纳数量）。
 *
 * BadgeSlot（v5.1 硬性要求「徽章宽度固定」）：useRef+useLayoutEffect 记录历史
 * 最大内容宽，style.minWidth 锁定（宽度只增不减）+ tabular-nums（数字等宽）——
 * 内容字符变化（847→1042）不再抖动。通用实现，零插件特化。
 *
 * SideChips（v5.1「Focus + Drawer」）：固定底部一行 36px 图标 chip（
 * overflow-x-auto 横向滚动容纳任意多插件）；徽标 = def.badges() 计数直接可见；
 * chip 单击 = 面板转 floating（临时使用不占侧栏）；chip hover 显示 📌 → 钉选
 * （zone 'side'，append 堆叠尾，默认 h 220）。组件在 PanelDock 内部渲染，
 * 无需导出给 AppShell。
 *
 * 徽章形态优先级：def.badgeRender(ctx)（registry 路扩展）→ def.badges() 文本
 * pill → 图标 + title。徽章交互：单击 = Popover 紧凑详情（badgeRender 内容 +
 * 「⤢ 升为浮窗」按钮）；双击 = 直接升浮窗。
 *
 * 收纳算法（两遍线性扫描，宽度缓存）：
 *  1. 每个徽章渲染时经 ref 回调缓存宽度（getBoundingClientRect）。
 *  2. useLayoutEffect / ResizeObserver 触发 recompute：容器 clientWidth 内
 *     依次累加徽章宽（＋N 预留宽两遍扫描处理），超出即从尾部收纳。
 *  3. visibleCount 稳定时 setState 同值 bail-out，无渲染循环。
 */
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { Inbox, Maximize2, Pin, Plus, X } from 'lucide-react'

import { usePanelDock, zoneHighlightStyle } from './PanelLayout'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import type { PanelBadge, PanelDefinition } from '@/plugin-api'
import type { TabManager } from '@/hooks/useTabManager'
import { cn } from '@/lib/utils'
import { Popover, PopoverAnchor, PopoverContent } from '@/components/ui/popover'

const BADGE_GAP = 4
/** ＋N 按钮宽度预留（收纳态才需要空间）。 */
const PLUS_RESERVED = 44

/**
 * v5.1 BadgeSlot——徽章槽位宽度锁定（硬性要求）。每次渲染后测量内容自然宽，
 * 历史最大值写入 style.minWidth（只增不减）；tabular-nums 数字等宽。
 * 历史最大存组件级 ref——Popover 等宿主重渲染可能重建 DOM 元素，锁定值必须
 * 由 React 实例（同挂载周期内稳定）持有，每次 effect 写回，不受 DOM 重建影响。
 */
export function BadgeSlot({ children, className }: { children: ReactNode; className?: string }): ReactNode {
  const ref = useRef<HTMLSpanElement | null>(null)
  const maxWRef = useRef(0)
  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    // 空态协议（宿主侧）：插件徽章无数据渲染 null/空文本 → wrapper 无子节点
    // （:empty）→ 锚点 button 经 has-[>[data-badge-slot]:empty]:hidden 整体隐藏。
    // ⚠️ wrapper 必须保持挂载、绝不卸载 children——插件徽章组件内部
    // useSyncExternalStore 订阅数据源，卸载即断流（数据恢复永不重渲染：
    // 用户报告"有数据也不渲染 ttft/tok/s"）。可见性由 CSS :has/:empty 驱动。
    const isEmpty = el.childElementCount === 0 && (el.textContent ?? '').trim() === ''
    if (isEmpty) return
    const w = Math.ceil(el.getBoundingClientRect().width)
    if (w > maxWRef.current) {
      maxWRef.current = w
    }
    if (maxWRef.current > 0 && el.style.minWidth !== `${maxWRef.current}px`) {
      el.style.minWidth = `${maxWRef.current}px`
    }
  })
  return (
    <span
      ref={ref}
      data-badge-slot=""
      className={cn('inline-flex min-w-0 items-center', className)}
      style={{ fontVariantNumeric: 'tabular-nums' }}
    >
      {children}
    </span>
  )
}

/** 徽章形态：badgeRender(ctx) → badges() 文本 pill → 图标 + title。 */
export function PanelBadgeView({ def, tabManager }: { def: PanelDefinition; tabManager: TabManager }): ReactNode {
  if (def.badgeRender) return <>{def.badgeRender({ tabManager })}</>
  // 空态协议：badges() 返回 null = 无徽章内容 → 不渲染（rail 上的空 pill/空边框
  // 由锚点 button 的 empty:hidden 兜底隐藏）。无 icon+title 兜底——rail 徽章必须
  // 由 badges/badgeRender 数据驱动，无数据即隐藏。
  const badge: PanelBadge | null = def.badges?.() ?? null
  if (!badge) return null
  return (
    <span
      className="max-w-[120px] truncate rounded-full px-1.5 py-px text-[9px] font-semibold leading-4"
      style={{ background: `color-mix(in srgb, ${badge.color} 22%, transparent)`, color: badge.color }}
    >
      {badge.text}
    </span>
  )
}

/** 徽章紧凑详情 popover 内容（badgeRender 内容 + ⤢ 升为浮窗）。 */
function BadgeDetail({
  def,
  tabManager,
  onFloat,
}: {
  def: PanelDefinition
  tabManager: TabManager
  onFloat: () => void
}): ReactNode {
  return (
    <div data-rail-detail={def.id} className="flex min-w-56 flex-col gap-2 p-1">
      <div className="flex items-center gap-1.5">
        <PanelBadgeView def={def} tabManager={tabManager} />
      </div>
      <button
        type="button"
        data-testid="rail-detail-float"
        onClick={onFloat}
        className="flex items-center justify-center gap-1.5 rounded-md border px-2 py-1.5 text-[11px] transition-colors hover:bg-accent/10"
        style={{ borderColor: 'var(--border)', color: 'var(--text-secondary)' }}
      >
        <Maximize2 className="size-3" />
        ⤢ 升为浮窗
      </button>
    </div>
  )
}

function BadgeRail({ zone, className }: { zone: 'top' | 'bottom'; className?: string }): ReactNode {
  const dock = usePanelDock()
  const ids = dock.zoneIds(zone)
  const containerRef = useRef<HTMLDivElement | null>(null)
  const widthRef = useRef<Map<string, number>>(new Map())
  // null = 全部可见（未测量/绰绰有余）；数字 = 前 N 个可见，其余收进 ＋N。
  const [visibleCount, setVisibleCount] = useState<number | null>(null)
  const [menuOpen, setMenuOpen] = useState(false)
  /** ＋N 菜单里点开的徽章详情（anchor 在 ＋N 按钮）。 */
  const [menuDetailId, setMenuDetailId] = useState<string | null>(null)
  /** rail 内联徽章的详情 popover。 */
  const [inlineDetailId, setInlineDetailId] = useState<string | null>(null)

  const setBadgeRef = useCallback((id: string) => (el: HTMLElement | null) => {
    if (el) {
      const w = el.getBoundingClientRect().width
      if (w > 0) widthRef.current.set(id, w)
    }
    // el=null（收纳卸载）保留缓存——容器再变宽时仍能恢复该徽章。
  }, [])

  const recompute = useCallback(() => {
    const el = containerRef.current
    if (!el || ids.length === 0) {
      setVisibleCount((prev) => (prev === null ? prev : null))
      return
    }
    const available = el.clientWidth
    const scan = (reservePlus: boolean): number => {
      let used = 0
      let count = 0
      for (let i = 0; i < ids.length; i++) {
        const w = widthRef.current.get(ids[i])
        if (w == null) break
        const need = count === 0 ? w : used + BADGE_GAP + w
        // 后面还有徽章且本徽章已占用空间 → 收纳态需要 ＋N 按钮空间。
        const reserve = reservePlus && i < ids.length - 1 ? PLUS_RESERVED : 0
        if (need + reserve > available) break
        used = need
        count++
      }
      return count
    }
    let count = scan(false)
    if (count < ids.length) count = Math.min(count, scan(true))
    setVisibleCount((prev) => {
      const next = count >= ids.length ? null : count
      return prev === next ? prev : next
    })
  }, [ids])

  // 每次渲染后重算（ref 回调在 commit 阶段已更新宽度缓存；visibleCount 同值
  // setState 由 React bail-out，无循环）。
  useLayoutEffect(() => {
    recompute()
  })

  // 容器尺寸变化（消费方布局变化）→ 重算收纳。
  useEffect(() => {
    const el = containerRef.current
    if (!el || typeof ResizeObserver === 'undefined') return
    const ro = new ResizeObserver(() => recompute())
    ro.observe(el)
    return () => ro.disconnect()
  }, [recompute])

  // ids 变化 → 清理已注销面板的宽度缓存。
  useEffect(() => {
    const live = new Set(ids)
    for (const id of [...widthRef.current.keys()]) {
      if (!live.has(id)) widthRef.current.delete(id)
    }
  }, [ids])

  const defOf = useCallback((id: string) => dock.defs.find((d) => d.id === id), [dock])
  const float = useCallback((id: string) => {
    dock.floatPanel(id)
    setInlineDetailId(null)
    setMenuOpen(false)
    setMenuDetailId(null)
  }, [dock])

  const visible = visibleCount == null ? ids : ids.slice(0, visibleCount)
  const overflow = visibleCount == null ? [] : ids.slice(visibleCount)

  // segment 分组渲染（left 靠左 / center 居中 / right 靠右；缺组不占位）。
  const groups = useMemo(() => {
    const bySeg: Record<'left' | 'center' | 'right', string[]> = { left: [], center: [], right: [] }
    for (const id of visible) {
      const seg = dock.entryOf(id).loc.segment ?? 'left'
      ;(bySeg[seg] ?? bySeg.left).push(id)
    }
    return bySeg
  }, [visible, dock])

  const renderBadge = (id: string) => {
    const def = defOf(id)
    if (!def) return null
    const open = inlineDetailId === id
    return (
      <Popover key={id} open={open}>
        <PopoverAnchor asChild>
          <button
            type="button"
            ref={setBadgeRef(id)}
            data-rail-badge={id}
            title={`${def.title}（双击升为浮窗）`}
            onClick={() => setInlineDetailId((prev) => (prev === id ? null : id))}
            onDoubleClick={() => float(id)}
            className="flex max-w-[200px] shrink-0 items-center gap-1 rounded-full border px-2 py-1 text-[11px] transition-colors hover:bg-accent/10 has-[>[data-badge-slot]:empty]:hidden"
            style={{ borderColor: 'var(--border)' }}
          >
            {/* v5.1：徽章槽位宽度锁定（BadgeSlot）——内容字符变化不抖动。 */}
            <BadgeSlot>
              <PanelBadgeView def={def} tabManager={dock.tabManager} />
            </BadgeSlot>
          </button>
        </PopoverAnchor>
        <PopoverContent align="start" sideOffset={6} className="w-auto p-2">
          <BadgeDetail def={def} tabManager={dock.tabManager} onFloat={() => float(id)} />
        </PopoverContent>
      </Popover>
    )
  }

  const renderGroup = (seg: 'left' | 'center' | 'right', grow: boolean) =>
    groups[seg].length > 0 || (seg === 'right' && overflow.length > 0) ? (
      <div
        key={seg}
        className={cn('flex min-w-0 items-center gap-1 overflow-hidden', grow && 'flex-1', seg === 'center' && 'justify-center', seg === 'right' && 'justify-end')}
      >
        {groups[seg].map(renderBadge)}
        {seg === 'right' && overflow.length > 0 ? (
          <Popover open={menuOpen}>
            <PopoverAnchor asChild>
              <button
                type="button"
                data-testid="rail-overflow-button"
                onClick={() => setMenuOpen((v) => !v)}
                className="flex shrink-0 items-center gap-0.5 rounded-full border px-2 py-1 text-[11px] transition-colors hover:bg-accent/10"
                style={{ borderColor: 'var(--border)', color: 'var(--text-secondary)' }}
              >
                <Plus className="size-3" />
                {overflow.length}
              </button>
            </PopoverAnchor>
            <PopoverContent align="end" sideOffset={6} className="w-56 p-1">
              {overflow.map((id) => {
                const def = defOf(id)
                if (!def) return null
                const detailOpen = menuDetailId === id
                return (
                  <Popover key={id} open={detailOpen}>
                    <PopoverAnchor asChild>
                      <button
                        type="button"
                        data-rail-overflow-item={id}
                        onClick={() => setMenuDetailId((prev) => (prev === id ? null : id))}
                        className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs transition-colors hover:bg-accent/10"
                      >
                        <PanelBadgeView def={def} tabManager={dock.tabManager} />
                      </button>
                    </PopoverAnchor>
                    <PopoverContent align="start" sideOffset={6} className="w-auto p-2">
                      <BadgeDetail def={def} tabManager={dock.tabManager} onFloat={() => float(id)} />
                    </PopoverContent>
                  </Popover>
                )
              })}
            </PopoverContent>
          </Popover>
        ) : null}
      </div>
    ) : null

  const zoneActive = dock.activeZone === zone
  return (
    <div
      ref={containerRef}
      data-panel-zone={zone}
      data-testid={`panel-rail-${zone}`}
      data-zone-active={zoneActive || undefined}
      className={cn('flex h-8 min-w-0 shrink-0 items-center gap-1 overflow-hidden px-2', className)}
      style={zoneHighlightStyle(zoneActive)}
    >
      {renderGroup('left', groups.center.length === 0 && groups.right.length === 0)}
      {groups.center.length > 0 ? renderGroup('center', true) : null}
      {renderGroup('right', true)}
    </div>
  )
}

/** top 徽章 rail（max-width 由消费方 className 定界；绝不溢出/推挤布局）。 */
export function TopRail({ className }: { className?: string }): ReactNode {
  return <BadgeRail zone="top" className={className} />
}

/** bottom 徽章 rail（形态同 TopRail，zone='bottom'）。 */
export function BottomRailBadges({ className }: { className?: string }): ReactNode {
  return <BadgeRail zone="bottom" className={className} />
}

/**
 * v5.2 SideChips——底部 chips 启动器（PanelDock 内部消费）。
 *
 * 固定底部一行图标 chip（overflow-x-auto 横向滚动容纳任意多插件）。
 * ⚠️ 容器高度必须大于 chip 高度 + 上下 border（h-10 = 40px border-box → 38px
 * 内容区 ≥ size-9 36px chip）：h-9（34px 内容区 < 36px chip）会产生 2px 竖向
 * 溢出，而 overflow-x-auto 使 overflow-y:visible 计算为 auto → 竖向滚动条
 * （用户报告"禁止竖着滚动"）。同理 pin（-top-1）与激活指示条（-bottom-0.5）
 * 的负偏移也会在 hover/激活时触发滚动条 —— 悬浮元素必须收进容器内容区。
 * 交互（v5.2 设计稿确认）：
 *  - chip 单击 = 原地展开/收起精简内容（不再弹浮窗！）——chip dock 上方弹出
 *    max-h-[240px] 内容区，再点收起。点其他 chip = 切换内容。
 *  - chip hover 显示 📌 → pin 到 side 钉选区（zone 'side'，append 堆叠尾，默认 h 220）。
 *  - 拖入 chip dock = 收纳（zone 'chip'）。
 *
 * 根元素 data-panel-zone="chip"——拖拽落点判定宿主。
 */
export function SideChips(): ReactNode {
  const dock = usePanelDock()
  const ids = dock.zoneIds('chip')
  const zoneActive = dock.activeZone === 'chip'
  const [expandedChip, setExpandedChip] = useState<string | null>(null)
  return (
    <div data-panel-zone="chip" data-testid="panel-chip-dock" className="flex flex-col">
      {/* 展开内容区——在 chip 行上方向上弹出（chip 行保持底部固定） */}
      {expandedChip ? (
        <div className="mb-1 max-h-[240px] overflow-y-auto rounded-lg border p-2" style={{ borderColor: 'var(--border)', background: 'var(--bg-elevated)' }}>
          <div className="mb-1 flex items-center justify-between">
            <span className="text-[9px] font-semibold uppercase tracking-wider" style={{ color: 'var(--text-muted)' }}>
              {dock.defs.find((d) => d.id === expandedChip)?.title ?? expandedChip}
            </span>
            <div className="flex items-center gap-1">
              <button
                type="button"
                onClick={() => { dock.pinPanel(expandedChip); setExpandedChip(null) }}
                className="rounded px-1.5 py-0.5 text-[9px] font-medium"
                style={{ background: 'color-mix(in srgb, var(--accent) 18%, transparent)', color: 'var(--accent)' }}
              >
                📌 钉选到侧栏
              </button>
              <button
                type="button"
                onClick={() => setExpandedChip(null)}
                className="rounded p-0.5"
                style={{ color: 'var(--text-muted)' }}
              >
                <X className="size-3" />
              </button>
            </div>
          </div>
          <div className="text-[11px]" style={{ color: 'var(--text-secondary)' }}>
            {(() => {
              const def = dock.defs.find((d) => d.id === expandedChip)
              if (!def) return null
              return def.render({ tabManager: dock.tabManager }) ?? (
                <div className="flex items-center justify-center py-4 text-[10px]" style={{ color: 'var(--text-muted)' }}>
                  <Inbox className="mr-1 size-3.5" /> 暂无内容
                </div>
              )
            })()}
          </div>
        </div>
      ) : null}
      <div
        className="flex h-10 shrink-0 items-center gap-1 overflow-x-auto rounded-lg border px-1 bg-bg-elevated"
        style={{ borderColor: 'var(--border)', ...zoneHighlightStyle(zoneActive) }}
      >
        {ids.map((id) => {
          const def = dock.defs.find((d) => d.id === id)
          if (!def) return null
          const badge: PanelBadge | null = def.badges?.() ?? null
          const Icon = pluginIcon(def.icon)
          const isActive = expandedChip === id
          return (
            <div key={id} className="group relative shrink-0">
              <button
                type="button"
                data-panel-chip={id}
                title={`${def.title}（单击展开/收起）`}
                onClick={() => setExpandedChip(isActive ? null : id)}
                className="flex size-9 items-center justify-center rounded-lg transition-colors hover:bg-accent/10"
                style={isActive ? { background: 'color-mix(in srgb, var(--accent) 15%, transparent)' } : undefined}
              >
                <Icon className="size-4" style={{ color: isActive ? 'var(--accent)' : 'var(--text-secondary)' }} />
                {badge ? (
                  <span
                    className="absolute right-0 top-0 max-w-[32px] truncate rounded-full px-1 text-[8px] font-semibold leading-3"
                    style={{ background: `color-mix(in srgb, ${badge.color} 22%, transparent)`, color: badge.color }}
                  >
                    {badge.text}
                  </span>
                ) : null}
                {isActive && (
                  <span className="absolute top-0 left-1/2 h-[2px] w-4 -translate-x-1/2 rounded-full" style={{ background: 'var(--accent)' }} />
                )}
              </button>
              <button
                type="button"
                aria-label={`钉选 ${def.title}`}
                title={`钉选 ${def.title} 到侧栏`}
                onClick={() => dock.pinPanel(id)}
                className="absolute right-0.5 top-0.5 hidden items-center justify-center rounded-full border p-0.5 group-hover:flex"
                style={{ borderColor: 'var(--border)', background: 'var(--bg-elevated)', color: 'var(--text-muted)' }}
              >
                <Pin className="size-2.5" />
              </button>
            </div>
          )
        })}
        {ids.length === 0 ? (
          <span className="px-2 text-[10px]" style={{ color: 'var(--text-muted)' }}>
            无收纳面板
          </span>
        ) : null}
      </div>
    </div>
  )
}
