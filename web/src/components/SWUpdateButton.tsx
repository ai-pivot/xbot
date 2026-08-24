/**
 * 顶部/底部状态栏的 SW 更新控件。
 *
 * 三态（严格按用户要求）：
 *   - idle（未发现新版本）：刷新 icon（RefreshCw 刷新圆圈箭头）——点击触发一次
 *     手动检查，点击后 icon 短暂转圈反馈"检查中"（避免"点击没反应"的困惑）。
 *   - downloading（检查到，自动开始下载）：下载 icon（Download）+ spin
 *   - ready（下载完成）：点击 → 重启加载新版本（RotateCcw）
 *
 * 10s poll：interval 调 reg.update() 触发 SW 自己向服务器核对是否有新 SW。
 * workbox 配置 skipWaiting+clientsClaim，所以新 SW 下载完自动激活，但页面
 * JS 仍是旧 bundle —— 必须 reload 才能加载新前端。ready 态 = 点重启。
 *
 * icon 选择：RefreshCw（刷新圆圈箭头）是"检查更新"最通用的视觉符号，比
 * 放大镜(Search,易误认为搜索)直观。
 */
import { useState, useEffect, useRef, useCallback } from 'react'
import type { LucideIcon } from 'lucide-react'
import { RefreshCw, Download, RotateCcw } from 'lucide-react'

export type SWUpdateStatus = 'idle' | 'downloading' | 'ready'

export interface SWUpdateAPI {
  status: SWUpdateStatus
  /** 手动触发一次更新检查（点击检查 icon 时）。 */
  check: () => void
  /** reload 加载新版本（点击重启 icon 时）。 */
  reload: () => void
}

export function useSWUpdate(): SWUpdateAPI {
  const [status, setStatus] = useState<SWUpdateStatus>('idle')
  const regRef = useRef<ServiceWorkerRegistration | null>(null)

  useEffect(() => {
    if (!('serviceWorker' in navigator)) return
    const isLocalhost = location.hostname === 'localhost' || location.hostname === '127.0.0.1'
    // localhost / dev 无 SW（registerSW 也跳过），保持 idle 无副作用。
    if (isLocalhost) return

    let timer: ReturnType<typeof setInterval> | null = null
    let mounted = true

    navigator.serviceWorker.register('/sw2.js', { scope: '/' }).then((reg) => {
      if (!mounted) return
      regRef.current = reg
      // 10s poll：每次 reg.update() 让 SW 自己去服务器核对（SW 脚本 no-store）。
      timer = setInterval(() => {
        reg.update().catch(() => {})
      }, 10_000)
      // 发现新 SW → 下载 → 就绪。
      const onUpdateFound = () => {
        const nw = reg.installing
        if (!nw) return
        nw.addEventListener('statechange', () => {
          if (!mounted) return
          if (nw.state === 'installing') {
            setStatus('downloading')
          } else if (nw.state === 'installed' || nw.state === 'activated') {
            // skipWaiting 后 activated；页面还是旧 JS，需 reload。
            setStatus('ready')
          }
        })
      }
      reg.addEventListener('updatefound', onUpdateFound)
      // 初次注册也 poll 一次（页面可能恰好有更新）。
      reg.update().catch(() => {})
    }).catch(() => { /* SW 注册失败（如浏览器不支持 PWA）→ 保持 idle */ })

    return () => {
      mounted = false
      if (timer) clearInterval(timer)
    }
  }, [])

  const check = useCallback(() => {
    regRef.current?.update().catch(() => {})
  }, [])

  const reload = useCallback(() => {
    window.location.reload()
  }, [])

  return { status, check, reload }
}

const ICON_BY_STATUS: Record<SWUpdateStatus, LucideIcon> = {
  idle: RefreshCw,
  downloading: Download,
  ready: RotateCcw,
}

const TITLE_BY_STATUS: Record<SWUpdateStatus, string> = {
  idle: '检查更新',
  downloading: '正在下载新版本…',
  ready: '重启加载新版本',
}

/**
 * 状态栏 SW 更新按钮（InfoBar 底部用）。
 * idle=刷新icon（点击短暂转圈检查中）；downloading=下载icon+spin；
 * ready=重启icon，点击 reload。点击检查至少有动画反馈，不做"点了没反应"。
 */
export function SWUpdateButton() {
  const { status, check, reload } = useSWUpdate()
  // 手动点击"检查中"的短暂动画状态（区别于后台 10s poll 的自动状态）。
  const [manualChecking, setManualChecking] = useState(false)

  const Icon = ICON_BY_STATUS[status]
  const title = TITLE_BY_STATUS[status]
  const spinIcon = status === 'idle' && manualChecking

  const onClick = () => {
    if (status === 'ready') {
      reload()
      return
    }
    if (status === 'downloading') return // 下载中，不需要额外动作
    // idle：触发一次手动检查 + 立即转圈动画反馈（约 900ms 后恢复）。
    setManualChecking(true)
    check()
    setTimeout(() => setManualChecking(false), 900)
  }

  const showSpin = status === 'downloading' || spinIcon

  return (
    <button
      type="button"
      title={spinIcon ? '检查中…' : title}
      aria-label={spinIcon ? '检查中…' : title}
      onClick={onClick}
      disabled={status === 'downloading'}
      className="flex shrink-0 items-center gap-1 whitespace-nowrap text-text-muted hover:text-text-primary disabled:cursor-wait disabled:opacity-70"
    >
      <Icon className={`size-3.5 ${showSpin ? 'animate-spin' : ''}`} />
    </button>
  )
}
