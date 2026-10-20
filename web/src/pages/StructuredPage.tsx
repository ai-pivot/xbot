/**
 * /structured 全屏路由：结构化消息流 + 收件箱（e2e 截图与深链共用）。
 */
import { Link } from 'react-router-dom'

import { StructuredWorkspace } from '@/structured/StructuredWorkspace'

export function StructuredPage() {
  return (
    <div className="flex h-screen min-h-0 flex-col bg-slate-100 dark:bg-slate-950">
      <div className="flex shrink-0 items-center gap-3 border-b border-slate-300/40 px-4 py-1.5 dark:border-slate-700/50">
        <Link to="/" className="text-[11px] text-sky-500">
          ← 返回
        </Link>
        <span className="text-[10px] opacity-60">/structured（结构化消息 + 收件箱）</span>
      </div>
      <div className="min-h-0 flex-1">
        <StructuredWorkspace />
      </div>
    </div>
  )
}
