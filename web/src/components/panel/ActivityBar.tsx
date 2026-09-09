/**
 * ActivityBar —— 最左边缘的垂直图标列（VSCode Activity Bar 模型）。
 *
 * 交互（取代旧的下角 chip rail）：
 *  - 点击图标 → 左侧栏内容切换为该面板（独占：目标展开、其他折叠）→ 全高显示。
 *  - 当前独占的面板图标高亮 + 左边缘竖条指示（VSCode 同款反馈）。
 *  - 左侧栏折叠时点击 → 先展开左栏（onActivate 回调）。
 *
 * 旧模型的问题（用户三次否定）：图标长在侧栏底部，点击在同一区域展开 →
 * 用户看到"位置没变，只是内容换了"（"点击就回到侧边栏"）；或弹出 340×440
 * 小浮层（空间局促、遮挡、看着难受）。图标列独立在最左边缘后，点击的反馈
 * 明确落在它右边的栏上。
 */
import { useMemo, type ReactNode } from 'react'

import { usePanelDock } from './PanelLayout'
import { pluginIcon } from '@/plugin-runtime/pluginIcons'
import { useI18n } from '@/providers/i18n'

interface ActivityBarProps {
  /** 左侧栏是否收起（收起时点击图标 → 展开并显示该面板）。 */
  collapsed?: boolean
  /** 展开/收起左侧栏（点击已激活的图标时收起整栏）。 */
  onToggleCollapse?: () => void
}

export function ActivityBar({ collapsed, onToggleCollapse }: ActivityBarProps): ReactNode {
  const { t } = useI18n()
  const dock = usePanelDock()

  // 图标来源：side / chip zone 的全部面板（会话 + 工具），按 defs 注册序。
  const items = useMemo(() => {
    const out: Array<{ id: string; title: string; icon: string; badge: { text: string; color: string } | null }> = []
    for (const def of dock.defs) {
      const zone = dock.entryOf(def.id).loc.zone
      if (zone !== 'side' && zone !== 'chip') continue
      out.push({
        id: def.id,
        title: def.labelKey ? t(def.labelKey) : def.title,
        icon: def.icon,
        badge: def.badges?.() ?? null,
      })
    }
    return out
  }, [dock.defs, dock.entryOf, t])

  // 当前"独占左栏"的面板 = 唯一展开的 side 面板（focusPanel 的产物）。
  // 侧栏收起时不高亮任何图标——面板虽展开但不可见，高亮会造成"图标激活但
  // 侧栏是空的"错觉（用户报"布局有问题"）。
  const sideIds = dock.zoneIds('side')
  const expandedSideIds = sideIds.filter((pid) => !dock.entryOf(pid).collapsed)
  const soloId = collapsed ? null : expandedSideIds.length === 1 ? expandedSideIds[0] : null

  return (
    <nav
      data-testid="activity-bar"
      aria-label={t('layout.activityBar')}
      className="flex h-full w-12 shrink-0 flex-col items-center gap-0.5 overflow-y-auto border-r py-1.5 [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      style={{ borderColor: 'var(--border)', background: 'var(--bg-secondary)' }}
    >
      {items.map((it) => {
        const Icon = pluginIcon(it.icon)
        const isActive = soloId === it.id
        return (
          <button
            key={it.id}
            type="button"
            data-activity-item={it.id}
            title={it.title}
            aria-label={it.title}
            aria-pressed={isActive}
            onClick={() => {
              // 侧栏收起 → 展开 + 显示该面板。
              if (collapsed) {
                onToggleCollapse?.()
                dock.focusPanel(it.id)
                return
              }
              // 点击【已激活】的图标 → 收起整个侧栏（VSCode 行为）。绝不能只折叠
              // 面板自己——那会留下 329px 的空白侧栏（用户报"布局有问题"）。
              if (isActive) {
                onToggleCollapse?.()
                return
              }
              dock.focusPanel(it.id)
            }}
            className="relative flex size-10 shrink-0 items-center justify-center rounded-lg transition-colors hover:bg-bg-tertiary"
            style={isActive ? { background: 'color-mix(in srgb, var(--accent) 14%, transparent)' } : undefined}
          >
            {/* 激活指示条（VSCode 同款：左边缘竖条） */}
            {isActive ? (
              <span
                className="absolute left-0 top-1/2 h-5 w-[2px] -translate-y-1/2 rounded-full"
                style={{ background: 'var(--accent)' }}
              />
            ) : null}
            <Icon
              className="size-[18px]"
              style={{ color: isActive ? 'var(--accent)' : 'var(--text-secondary)' }}
            />
            {it.badge ? (
              <span
                className="absolute right-0.5 top-0.5 max-w-[28px] truncate rounded-full px-1 text-[8px] font-semibold leading-3"
                style={{
                  background: `color-mix(in srgb, ${it.badge.color} 22%, transparent)`,
                  color: it.badge.color,
                }}
              >
                {it.badge.text}
              </span>
            ) : null}
          </button>
        )
      })}
    </nav>
  )
}
