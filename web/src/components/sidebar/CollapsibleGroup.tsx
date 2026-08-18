/**
 * CollapsibleGroup —— 可折叠分组容器。
 *
 * 侧栏里的相关布局项归入一个 group，group 有 header（标题 + chevron），可
 * 点击收起/展开。折叠状态通过 layoutRegistry 纯前端持久化（localStorage），
 * 这样「一个功能占满侧栏」时用户可收起其它 group。
 */
import { ChevronRight } from 'lucide-react'
import type { ReactNode } from 'react'

import { useLayoutCollapse } from '@/plugin-runtime/layoutRegistry'

interface CollapsibleGroupProps {
  groupId: string
  title: string
  children: ReactNode
}

export function CollapsibleGroup({ groupId, title, children }: CollapsibleGroupProps): ReactNode {
  const { isCollapsed, toggleCollapsed } = useLayoutCollapse()
  const collapsed = isCollapsed(groupId)

  return (
    <div className="flex min-h-0 flex-col">
      <button
        type="button"
        onClick={() => toggleCollapsed(groupId)}
        title={collapsed ? `展开${title}` : `收起${title}`}
        className="flex shrink-0 items-center gap-1.5 border-b border-[var(--border)] bg-[var(--bg-secondary)] px-2 py-1.5 text-xs font-medium text-text-muted transition-colors hover:text-text-secondary"
      >
        <ChevronRight className={`size-3.5 shrink-0 transition-transform ${collapsed ? '' : 'rotate-90'}`} />
        <span className="truncate">{title}</span>
      </button>
      {!collapsed && <div className="min-h-0 flex-1 overflow-y-auto">{children}</div>}
    </div>
  )
}