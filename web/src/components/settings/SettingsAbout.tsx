/**
 * SettingsAbout — about / PWA install panel with diagnostics.
 */
import { useState } from 'react'
import { Download, Check, AlertCircle, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { usePwaInstall } from '@/hooks/usePwaInstall'

/** One diagnostic row with a pass/fail indicator. */
function DiagRow({ label, ok }: { label: string; ok: boolean }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <span
        className="size-1.5 shrink-0 rounded-full"
        style={{ backgroundColor: ok ? 'var(--status-running)' : 'var(--status-error)' }}
      />
      <span className="text-text-secondary">{label}</span>
    </div>
  )
}

export function SettingsAbout() {
  const { canInstall, isInstalled, install, updateAvailable, checkForUpdate, refreshSW, diagnostics } = usePwaInstall()
  const [checking, setChecking] = useState(false)
  const [upToDate, setUpToDate] = useState(false)

  const handleUpdate = async () => {
    if (updateAvailable) {
      // New SW already activated — reload to pick up new cached assets.
      await refreshSW()
      return
    }
    // Check for updates manually.
    setChecking(true)
    setUpToDate(false)
    const found = await checkForUpdate()
    setChecking(false)
    if (found) {
      // checkForUpdate set updateAvailable=true and the SW activated.
      // Reload to pick up new assets.
      await refreshSW()
    } else {
      setUpToDate(true)
    }
  }

  return (
    <div className="flex flex-col gap-6 p-5">
      {/* App info */}
      <section className="flex flex-col gap-1">
        <h3 className="text-sm font-semibold text-text-primary">xbot</h3>
        <p className="text-xs text-text-secondary">AI 智能对话助手</p>
      </section>

      {/* PWA status */}
      <section className="flex flex-col gap-3">
        <h3 className="text-sm font-semibold text-text-primary">应用安装</h3>

        {/* Installed */}
        {isInstalled && (
          <div className="flex items-center gap-2 rounded-md bg-bg-tertiary px-3 py-2 text-xs" style={{ color: 'var(--status-running)' }}>
            <Check className="size-4" />
            <span>已安装到桌面，以独立应用模式运行</span>
          </div>
        )}

        {/* Install button (Chrome/Edge) */}
        {!isInstalled && canInstall && (
          <Button type="button" variant="default" onClick={() => install()} className="w-fit gap-2">
            <Download className="size-4" />
            安装应用
          </Button>
        )}

        {/* Safari / iOS — manual install instructions */}
        {!isInstalled && !canInstall && diagnostics?.isSafari && (
          <div className="flex flex-col gap-2 rounded-md bg-bg-tertiary px-3 py-3 text-xs">
            <div className="flex items-start gap-2">
              <Download className="mt-0.5 size-4 shrink-0" style={{ color: 'var(--status-running)' }} />
              <div className="flex flex-col gap-1 text-text-secondary">
                <span className="font-medium text-text-primary">添加到主屏幕</span>
                <span>Safari 不支持自动安装，请按以下步骤操作：</span>
                <span>1. 点击底部「分享」按钮 (方框+向上箭头)</span>
                <span>2. 滚动选择「添加到主屏幕」</span>
                <span>3. 点击「添加」完成安装</span>
              </div>
            </div>
          </div>
        )}

        {/* Not installable (non-Safari) — show diagnostics */}
        {!isInstalled && !canInstall && !(diagnostics?.isSafari) && (
          <div className="flex flex-col gap-3 rounded-md bg-bg-tertiary px-3 py-3 text-xs">
            <div className="flex items-start gap-2">
              <AlertCircle className="mt-0.5 size-4 shrink-0" style={{ color: 'var(--status-error)' }} />
              <span className="text-text-secondary">
                暂未满足安装条件。Chrome 需要 Service Worker 激活后才会弹出安装提示，
                请刷新页面或等待几秒后重试。
              </span>
            </div>
            {/* Diagnostics */}
            {diagnostics && (
              <div className="flex flex-col gap-1.5 border-t border-border pt-2">
                <p className="font-medium text-text-secondary">诊断信息:</p>
                <DiagRow label={`浏览器: ${diagnostics.browserName}`} ok={true} />
                <DiagRow label="HTTPS" ok={diagnostics.isHttps} />
                <DiagRow label="Service Worker 已激活" ok={diagnostics.hasSW} />
                <DiagRow label="Manifest 可访问" ok={diagnostics.hasManifest} />
                <DiagRow label={`display: ${diagnostics.manifestDisplay}`} ok={diagnostics.manifestDisplay === 'standalone'} />
                <DiagRow label={`192x192 图标`} ok={diagnostics.has192Icon} />
                <DiagRow label={`512x512 图标`} ok={diagnostics.has512Icon} />
                <DiagRow label={`图标总数: ${diagnostics.iconCount}`} ok={diagnostics.iconCount >= 2} />
                {!diagnostics.isSafari && (
                  <DiagRow label="beforeinstallprompt 事件" ok={canInstall} />
                )}
                {diagnostics.swUrl && (
                  <p className="text-text-muted">SW: {diagnostics.swUrl.split('/').pop()}</p>
                )}
              </div>
            )}
          </div>
        )}

        {/* Update / check for updates */}
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant={updateAvailable ? 'default' : 'outline'}
            onClick={() => void handleUpdate()}
            disabled={checking}
            className="w-fit gap-2"
          >
            <RefreshCw className={`size-4 ${checking ? 'animate-spin' : ''}`} />
            {updateAvailable ? '有新版本，点击更新' : checking ? '检查更新中…' : upToDate ? '已是最新版本' : '检查更新'}
          </Button>
          {updateAvailable && (
            <span className="text-xs" style={{ color: 'var(--status-running)' }}>
              ● 有新版本可用
            </span>
          )}
          {upToDate && !updateAvailable && (
            <span className="text-xs" style={{ color: 'var(--status-running)' }}>
              ● 已是最新版本
            </span>
          )}
        </div>
      </section>
    </div>
  )
}
