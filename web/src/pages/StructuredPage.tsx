/**
 * /structured 全屏路由：结构化消息流 + 收件箱（e2e 截图与深链共用）。
 */
import { Link } from 'react-router-dom'

import { StructuredWorkspace } from '@/structured/StructuredWorkspace'

export function StructuredPage() {
  return (
    <div className="flex h-screen min-h-0 flex-col bg-bg-primary text-text-primary">
      <div className="flex shrink-0 items-center gap-3 border-b border-border bg-bg-secondary px-4 py-1.5">
        <Link to="/" className="text-[11px] text-app-accent">
          ← 返回
        </Link>
        <span className="text-[10px] text-text-muted">/structured（结构化消息 + 收件箱）</span>
      </div>
      <div className="min-h-0 flex-1">
        <StructuredWorkspace />
      </div>
    </div>
  )
}
