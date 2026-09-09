/**
 * SettingsAbout — about / PWA install panel with diagnostics.
 */
import { useState } from 'react'
import { Download, Check, AlertCircle, RefreshCw } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { usePwaInstall } from '@/hooks/usePwaInstall'
import { useI18n } from '@/providers/i18n'

/** One diagnostic row with a pass/fail indicator. */
function DiagRow({ label, ok }: { label: string; ok: boolean }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <span
        className="size-1.5 shrink-0 rounded-full"
        style={{ backgroundColor: ok ? 'var(--status-success, #22c55e)' : 'var(--status-error)' }}
      />
      <span className="text-text-secondary">{label}</span>
    </div>
  )
}

export function SettingsAbout() {
  const { canInstall, isInstalled, install, updateAvailable, checkForUpdate, refreshSW, diagnostics } = usePwaInstall()
  const { t } = useI18n()
  const [checking, setChecking] = useState(false)
  const [upToDate, setUpToDate] = useState(false)
  const [reloading, setReloading] = useState(false)

  const handleUpdate = async () => {
    if (updateAvailable) {
      // New SW already activated — reload to pick up new cached assets.
      // Set reloading state immediately so the button shows feedback.
      setReloading(true)
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
      setReloading(true)
      await refreshSW()
    } else {
      setUpToDate(true)
    }
  }

  return (
    <div className="flex flex-col gap-2.5 p-4">
      {/* App info */}
      <section className="flex flex-col gap-1">
        <h3 className="text-sm font-semibold text-text-primary">xbot</h3>
        <p className="text-xs text-text-secondary">{t('settings.about.tagline')}</p>
      </section>

      {/* PWA status */}
      <section className="flex flex-col gap-2.5">
        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">{t('settings.about.installTitle')}</h3>

        {/* Installed */}
        {isInstalled && (
          <div className="flex items-center gap-2.5 rounded-xl border border-border bg-bg-secondary px-3 py-2 text-xs" style={{ color: 'var(--status-success, #22c55e)' }}>
            <Check className="size-4" />
            <span>{t('settings.about.installed')}</span>
          </div>
        )}

        {/* Install button (Chrome/Edge) */}
        {!isInstalled && canInstall && (
          <Button type="button" variant="default" onClick={() => install()} className="w-fit gap-2 bg-accent/14 text-accent hover:bg-accent/25">
            <Download className="size-4" />
            {t('settings.about.install')}
          </Button>
        )}

        {/* Safari / iOS — manual install instructions */}
        {!isInstalled && !canInstall && diagnostics?.isSafari && (
          <div className="flex flex-col gap-2.5 rounded-xl border border-border bg-bg-secondary px-3 py-2 text-xs">
            <div className="flex items-start gap-2.5">
              <Download className="mt-0.5 size-4 shrink-0" style={{ color: 'var(--status-success, #22c55e)' }} />
              <div className="flex flex-col gap-1 text-text-secondary">
                <span className="font-medium text-text-primary">{t('settings.about.addToHomeScreen')}</span>
                <span>{t('settings.about.safariSteps')}</span>
                <span>{t('settings.about.safariStep1')}</span>
                <span>{t('settings.about.safariStep2')}</span>
                <span>{t('settings.about.safariStep3')}</span>
              </div>
            </div>
          </div>
        )}

        {/* Not installable (non-Safari) — show diagnostics */}
        {!isInstalled && !canInstall && !(diagnostics?.isSafari) && (
          <div className="flex flex-col gap-2.5 rounded-xl border border-border bg-bg-secondary px-3 py-2 text-xs">
            <div className="flex items-start gap-2.5">
              <AlertCircle className="mt-0.5 size-4 shrink-0" style={{ color: 'var(--status-error)' }} />
              <span className="text-text-secondary">
                {t('settings.about.notInstallable')}
              </span>
            </div>
            {/* Diagnostics */}
            {diagnostics && (
              <div className="flex flex-col gap-1.5 border-t border-border pt-2">
                <p className="font-medium text-text-secondary">{t('settings.about.diagInfo')}</p>
                <DiagRow label={t('settings.about.diagBrowser', { name: diagnostics.browserName })} ok={true} />
                <DiagRow label="HTTPS" ok={diagnostics.isHttps} />
                <DiagRow label={t('settings.about.diagSw')} ok={diagnostics.hasSW} />
                <DiagRow label={t('settings.about.diagManifest')} ok={diagnostics.hasManifest} />
                <DiagRow label={`display: ${diagnostics.manifestDisplay}`} ok={diagnostics.manifestDisplay === 'standalone'} />
                <DiagRow label={t('settings.about.diagIcon192')} ok={diagnostics.has192Icon} />
                <DiagRow label={t('settings.about.diagIcon512')} ok={diagnostics.has512Icon} />
                <DiagRow label={t('settings.about.diagIconCount', { count: diagnostics.iconCount })} ok={diagnostics.iconCount >= 2} />
                {!diagnostics.isSafari && (
                  <DiagRow label={t('settings.about.diagBeforeInstall')} ok={canInstall} />
                )}
                {diagnostics.swUrl && (
                  <p className="text-text-muted">SW: {diagnostics.swUrl.split('/').pop()}</p>
                )}
              </div>
            )}
          </div>
        )}

        {/* Update / check for updates */}
        <div className="flex items-center gap-2.5">
          <Button
            type="button"
            variant={updateAvailable ? 'default' : 'outline'}
            onClick={() => void handleUpdate()}
            disabled={checking || reloading}
            className={cn(
              'w-fit gap-2',
              updateAvailable
                ? 'bg-accent/14 text-accent hover:bg-accent/25'
                : 'border-border bg-bg-tertiary hover:bg-bg-hover',
            )}
          >
            <RefreshCw className={`size-4 ${checking || reloading ? 'animate-spin' : ''}`} />
            {reloading ? t('settings.about.refreshing') : updateAvailable ? t('settings.about.updateAvailable') : checking ? t('settings.about.checking') : upToDate ? t('settings.about.upToDate') : t('settings.about.checkUpdate')}
          </Button>
          {updateAvailable && (
            <span className="text-xs" style={{ color: 'var(--status-warning, #f59e0b)' }}>
              ● {t('settings.about.newVersionAvailable')}
            </span>
          )}
          {upToDate && !updateAvailable && (
            <span className="text-xs" style={{ color: 'var(--status-success, #22c55e)' }}>
              ● {t('settings.about.upToDate')}
            </span>
          )}
        </div>
      </section>
    </div>
  )
}
