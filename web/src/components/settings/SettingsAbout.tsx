/**
 * SettingsAbout — about / PWA install panel with diagnostics.
 */
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
  const { canInstall, isInstalled, install, error, refreshSW, updateAvailable, diagnostics } = usePwaInstall()

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

        {/* Install button */}
        {!isInstalled && canInstall && (
          <Button type="button" variant="default" onClick={() => install()} className="w-fit gap-2">
            <Download className="size-4" />
            安装应用
          </Button>
        )}

        {/* Not installable — show diagnostics */}
        {!isInstalled && !canInstall && (
          <div className="flex flex-col gap-3 rounded-md bg-bg-tertiary px-3 py-3 text-xs">
            <div className="flex items-start gap-2">
              <AlertCircle className="mt-0.5 size-4 shrink-0" style={{ color: 'var(--status-error)' }} />
              <span className="text-text-secondary">
                暂未满足安装条件。Chrome 需要 Service Worker 激活后才会弹出安装提示，
                请刷新页面或等待几秒后重试。
              </span>
            </div>
            {error && (
              <p className="text-text-muted" style={{ color: 'var(--status-error)' }}>错误: {error}</p>
            )}
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
                <DiagRow label="beforeinstallprompt 事件" ok={false} />
                {diagnostics.swUrl && (
                  <p className="text-text-muted">SW: {diagnostics.swUrl.split('/').pop()}</p>
                )}
              </div>
            )}
          </div>
        )}

        {/* Update available */}
        {updateAvailable && (
          <Button type="button" variant="outline" onClick={() => refreshSW()} className="w-fit gap-2">
            <RefreshCw className="size-4" />
            更新到最新版本
          </Button>
        )}
      </section>
    </div>
  )
}
