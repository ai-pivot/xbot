/**
 * 顶部/底部状态栏的 SW 更新控件。
 *
 * 三态（严格按用户要求）：
 *   - checking（未发现新版本）：检查更新 icon —— 点击手动触发一次 update 检查
 *   - downloading（检查到，自动开始下载）：下载 icon
 *   - ready（下载完成）：点击 → 重启加载新版本
 *
 * 10s poll：interval 调 reg.update() 触发 SW 自己向服务器核对是否有新 SW。
 * workbox 配置 skipWaiting+clientsClaim，所以新 SW 下载完自动激活，但页面
 * JS 仍是旧 bundle —— 必须 reload 才能加载新前端。所以 ready 态 = 点重启。
 */
import { useState, useEffect, useRef, useCallback } from 'react'
import type { LucideIcon } from 'lucide-react'
import { Search, Download, RotateCcw } from 'lucide-react'

export type SWUpdateStatus = 'checking' | 'downloading' | 'ready'

export interface SWUpdateAPI {
  status: SWUpdateStatus
  /** 手动触发一次更新检查（点击检查 icon 时）。 */
  check: () => void
  /** reload 加载新版本（点击重启 icon 时）。 */
  reload: () => void
}

export function useSWUpdate(): SWUpdateAPI {
  const [status, setStatus] = useState<SWUpdateStatus>('checking')
  const regRef = useRef<ServiceWorkerRegistration | null>(null)
  const statusRef = useRef<SWUpdateStatus>('checking')
  statusRef.current = status

  useEffect(() => {
    if (!('serviceWorker' in navigator)) return
    const isLocalhost = location.hostname === 'localhost' || location.hostname === '127.0.0.1'
    // localhost / dev 无 SW（registerSW 也跳过），保持 checking 无副作用。
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
    }).catch(() => { /* SW 注册失败（如浏览器不支持 PWA）→ 保持 checking */ })

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
  checking: Search,
  downloading: Download,
  ready: RotateCcw,
}

const TITLE_BY_STATUS: Record<SWUpdateStatus, string> = {
  checking: '检查更新',
  downloading: '正在下载新版本…',
  ready: '重启加载新版本',
}

/** 状态栏 SW 更新按钮（InfoBar 底部用）。无新版本=检查 icon；检查到=下载；下载完=重启。 */
export function SWUpdateButton() {
  const { status, check, reload } = useSWUpdate()
  const Icon = ICON_BY_STATUS[status]
  const title = TITLE_BY_STATUS[status]
  const onClick = status === 'ready' ? reload : status === 'checking' ? check : undefined
  const disabled = status === 'downloading'
  return (
    <button
      type="button"
      title={title}
      aria-label={title}
      disabled={disabled}
      onClick={onClick}
      className="flex shrink-0 items-center gap-1 whitespace-nowrap text-text-muted hover:text-text-primary disabled:cursor-wait disabled:opacity-70"
    >
      <Icon className={status === 'downloading' ? 'size-3.5 animate-spin' : 'size-3.5'} />
    </button>
  )
}
