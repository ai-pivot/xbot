/**
 * SessionViewBar — 会话列表视图栏：分类切换（项目 / 状态 / 时间）+「全部折叠/展开」。
 *
 * 桌面 `core.sessions` 面板与手机抽屉 **共用同一实现**。
 * 历史事故：渠道下拉曾只存在于 `SessionSidebar`（手机抽屉），桌面面板换成
 * `CoreSessionsPanel` 后再也切不了渠道；分类切换器曾重演同一遗漏（只在抽屉里）
 * ⇒ 桌面上「按项目组织」根本切不到。新增列表级控件一律放这里，两处复用。
 */
import { useState } from 'react'
import { SessionGroupDialog, type GroupDialogTarget } from './SessionGroupDialog'
import { useSessionGroups } from '@/hooks/useSessionGroups'
import { ChevronsDownUp, ChevronsUpDown, FolderPlus } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import { useSessionStore } from '@/hooks/useSessionStore'
import { SESSION_CATEGORIES, collapseKey } from '@/lib/session-grouping'
import type { SessionCategory } from '@/types/shared'

interface SessionViewBarProps {
  /** Group keys of the CURRENT category (used by 全部折叠/展开). */
  groupKeys: string[]
  className?: string
}

function labelForCategory(c: SessionCategory, t: (k: string) => string): string {
  switch (c) {
    case 'time':
      return t('session.byTime')
    case 'status':
      return t('session.byStatus')
    case 'path':
      return t('session.byPath')
  }
}

export function SessionViewBar({ groupKeys, className }: SessionViewBarProps) {
  const { t } = useI18n()
  const [createTarget, setCreateTarget] = useState<GroupDialogTarget | null>(null)
  const groups = useSessionGroups()
  const store = useSessionStore()
  const keys = groupKeys.map((k) => collapseKey(store.category, k))
  const allCollapsed = keys.length > 0 && keys.every((k) => store.collapsedGroups.has(k))
  const toggleLabel = allCollapsed ? t('session.expandAllGroups') : t('session.collapseAllGroups')

  return (
    <div
      className={cn('flex shrink-0 items-center gap-0.5 px-2 py-1', className)}
      style={{ borderBottom: '1px solid var(--border)' }}
      data-testid="session-view-bar"
    >
      {SESSION_CATEGORIES.map((c) => {
        const active = store.category === c
        return (
          <button
            key={c}
            type="button"
            onClick={() => store.setCategory(c)}
            aria-pressed={active}
            data-testid={`session-category-${c}`}
            className="min-w-0 flex-1 truncate rounded px-2 py-1 text-[11px] font-medium transition-colors"
            style={{
              backgroundColor: active ? 'var(--bg-tertiary)' : 'transparent',
              color: active ? 'var(--text-primary)' : 'var(--text-secondary)',
            }}
          >
            {labelForCategory(c, t)}
          </button>
        )
      })}
      <button type="button" data-testid="group-create" disabled={groups.loading || groups.busy || !!groups.error} onClick={() => setCreateTarget({})} aria-label={t('session.createGroup')} title={t('session.createGroup')} className="size-7 shrink-0 rounded text-text-secondary hover:bg-bg-tertiary/60"><FolderPlus className="mx-auto size-3.5" /></button>
      <SessionGroupDialog target={createTarget} onClose={() => setCreateTarget(null)} />
      {keys.length > 0 && (
        <button
          type="button"
          onClick={() => store.setGroupsCollapsed(keys, !allCollapsed)}
          title={toggleLabel}
          aria-label={toggleLabel}
          data-testid="session-collapse-all"
          className="flex size-6 shrink-0 items-center justify-center rounded transition-colors hover:bg-bg-tertiary/60"
          style={{ color: 'var(--text-secondary)' }}
        >
          {allCollapsed ? <ChevronsUpDown className="size-3.5" /> : <ChevronsDownUp className="size-3.5" />}
        </button>
      )}
    </div>
  )
}
