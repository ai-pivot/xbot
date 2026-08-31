/**
 * TabHeader — dockview tab header, "rounded pill block" design (布局 v2):
 *   - Tab bar background is var(--sidebar-bg) (owned by index.css `.dv-tab`
 *     is transparent, so the bar shows through); each tab is a rounded-lg block.
 *   - Active tab: bg = var(--app-bg) + 1px var(--border) border,
 *     text = var(--text-primary), icon = var(--accent), close × always visible.
 *   - Inactive tab: transparent bg + var(--text-muted) text; close × appears
 *     on hover only (always visible on touch devices — no hover there).
 */
import type { ComponentType, SVGProps } from 'react'
import { X, Bot, FileText, SquareTerminal, ListVideo, Box, FileDiff, Folder } from 'lucide-react'
import type { DockviewPanelApi } from 'dockview'
import type { PanelParams } from '@/types/tab'
import { cn } from '@/lib/utils'
import { useIsTouch } from '@/hooks/useIsMobile'
import { useDockviewContext } from '@/workspace/types'
import { useI18n } from '@/providers/i18n'
import {
  ContextMenu,
  ContextMenuContent,
  ContextMenuItem,
  ContextMenuSeparator,
  ContextMenuTrigger,
} from '@/components/ui/context-menu'

type IconComponent = ComponentType<SVGProps<SVGSVGElement> & { size?: number | string }>

const ICONS: Record<string, IconComponent> = {
  bot: Bot,
  file: FileText,
  terminal: SquareTerminal,
  background: ListVideo,
}

const TYPE_ICONS: Record<PanelParams['type'], IconComponent> = {
  agent: Bot,
  file: FileText,
  terminal: SquareTerminal,
  background: ListVideo,
  plugin: Box,
  diff: FileDiff,
  panel: Folder,
}

export interface TabHeaderProps {
  params: PanelParams
  api: DockviewPanelApi
  isActive: boolean
  onActivate: () => void
}

export function TabHeader({ params, api, isActive, onActivate }: TabHeaderProps) {
  const isTouch = useIsTouch()
  const { t } = useI18n()
  const ctx = useDockviewContext()
  const tabManager = ctx.tabManager
  // 连接状态（agent tab 绿点）——卡片 header 已删（主卡 tab 栏常驻承载
  // 连接状态），agent tab 上的小圆点显示 ws 连接状态
  const ws = ctx.ws
  const Icon = (params.icon ? ICONS[params.icon] : null) ?? TYPE_ICONS[params.type]
  const fullTitle = params.type === 'file' ? (params.filePath || params.title) : params.title

  // 右键菜单 disable 态：同 group 的可关 tab 分布（TabManager 是 dockview 状态
  // 的镜像——tabs 变化时 ctx 更新触发 panel.update → 本组件 re-render）。
  const groupTabs = tabManager?.groupTabsOf(params.tabId) ?? []
  const selfIdx = groupTabs.findIndex((tab) => tab.tabId === params.tabId)
  const hasClosableLeft = selfIdx > 0 && groupTabs.slice(0, selfIdx).some((tab) => tab.closable)
  const hasClosableRight = selfIdx >= 0 && groupTabs.slice(selfIdx + 1).some((tab) => tab.closable)
  const hasClosableOther = groupTabs.some((tab, i) => i !== selfIdx && tab.closable)
  const hasClosableAny = groupTabs.some((tab) => tab.closable)

  return (
    <ContextMenu>
      <ContextMenuTrigger asChild>
        <div
          className={cn(
            'group flex h-[35px] w-full min-w-0 cursor-pointer select-none items-center gap-1.5',
            'rounded-lg border px-2.5 py-1 text-[13px] transition-colors duration-100',
            isActive
              ? 'border-border bg-tab-active-bg text-text-primary'
              : 'border-transparent bg-transparent text-text-muted',
          )}
          title={fullTitle}
          role="tab"
          aria-selected={isActive}
          tabIndex={isActive ? 0 : -1}
          onMouseDown={(e) => {
            if (e.button === 1) {
              if (params.closable) {
                e.preventDefault()
                api.close()
              } else {
                e.preventDefault()
              }
            }
          }}
          onClick={(e) => {
            e.stopPropagation()
            onActivate()
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter' || e.key === ' ') {
              e.preventDefault()
              onActivate()
            }
          }}
        >
          {params.type === 'agent' && (
            <span
              aria-label={ws.connected ? '已连接' : '连接中…'}
              title={ws.connected ? '已连接' : '连接中…'}
              className={cn(
                'size-1.5 shrink-0 rounded-full',
                ws.connected ? 'bg-emerald-500' : 'animate-pulse bg-amber-500',
              )}
            />
          )}
          <Icon aria-hidden className={cn('size-3.5 shrink-0', isActive ? 'text-accent' : 'text-text-muted')} />
          <span className="min-w-0 flex-1 truncate leading-none">{params.title}</span>
          {params.closable && (
            <button
              type="button"
              aria-label={t('common.close')}
              className={cn(
                'ml-0.5 flex size-[18px] shrink-0 items-center justify-center rounded-sm text-text-secondary',
                'transition-[color,background-color,opacity] duration-100 hover:bg-surface-bg hover:text-text-primary',
                'focus-visible:opacity-100 focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-accent',
                isActive || isTouch ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
              )}
              onClick={(e) => {
                e.stopPropagation()
                api.close()
              }}
            >
              <X aria-hidden className="size-3" />
            </button>
          )}
        </div>
      </ContextMenuTrigger>
      <ContextMenuContent>
        <ContextMenuItem
          disabled={!params.closable}
          onSelect={() => tabManager?.closeTab(params.tabId)}
        >
          {t('tabs.close')}
        </ContextMenuItem>
        <ContextMenuSeparator />
        <ContextMenuItem
          disabled={!hasClosableLeft}
          onSelect={() => tabManager?.closeTabsInGroup(params.tabId, 'left')}
        >
          {t('tabs.closeLeft')}
        </ContextMenuItem>
        <ContextMenuItem
          disabled={!hasClosableRight}
          onSelect={() => tabManager?.closeTabsInGroup(params.tabId, 'right')}
        >
          {t('tabs.closeRight')}
        </ContextMenuItem>
        <ContextMenuItem
          disabled={!hasClosableOther}
          onSelect={() => tabManager?.closeTabsInGroup(params.tabId, 'others')}
        >
          {t('tabs.closeOthers')}
        </ContextMenuItem>
        <ContextMenuItem
          disabled={!hasClosableAny}
          onSelect={() => tabManager?.closeTabsInGroup(params.tabId, 'all')}
        >
          {t('tabs.closeAll')}
        </ContextMenuItem>
      </ContextMenuContent>
    </ContextMenu>
  )
}
