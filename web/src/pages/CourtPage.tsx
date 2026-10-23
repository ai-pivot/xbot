/**
 * /court 全屏路由：独立打开通信视图（侧边栏面板 / 外部深链共用同一组件）。
 */
import { Link } from 'react-router-dom'
import { CourtConsole } from '@/components/court/CourtConsole'

export function CourtPage({ demo = false }: { demo?: boolean }) {
  return (
    <div className="flex h-screen min-h-0 flex-col bg-bg-primary text-text-primary">
      <div className="flex shrink-0 items-center gap-3 border-b border-border bg-bg-secondary px-4 py-1.5">
        <Link to="/" className="text-[11px] text-app-accent">← 返回</Link>
        <span className="text-[10px] text-text-muted">{demo ? '/court-demo' : '/court'}</span>
      </div>
      <div className="min-h-0 flex-1">
        <CourtConsole demo={demo} />
      </div>
    </div>
  )
}
