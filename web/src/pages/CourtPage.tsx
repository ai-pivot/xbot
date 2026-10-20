/**
 * /court 全屏路由：独立打开通信视图（侧边栏面板 / 外部深链共用同一组件）。
 */
import { Link } from 'react-router-dom'
import { CourtConsole } from '@/components/court/CourtConsole'

export function CourtPage({ demo = false }: { demo?: boolean }) {
  return (
    <div className="flex h-screen min-h-0 flex-col" style={{ background: '#070b14' }}>
      <div className="flex shrink-0 items-center gap-3 border-b px-4 py-1.5"
        style={{ borderColor: 'rgba(148,163,184,0.14)', background: 'rgba(9,14,24,0.9)' }}>
        <Link to="/" className="text-[11px]" style={{ color: '#38bdf8' }}>← 返回</Link>
        <span className="text-[10px]" style={{ color: 'rgba(148,163,184,0.6)' }}>{demo ? '/court-demo' : '/court'}</span>
      </div>
      <div className="min-h-0 flex-1">
        <CourtConsole demo={demo} />
      </div>
    </div>
  )
}
