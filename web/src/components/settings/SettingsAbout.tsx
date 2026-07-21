/**
 * SettingsAbout — about / PWA install panel.
 *
 * Shows app version, PWA install status, and an install button (or error
 * message if installation isn't available). Uses the BeforeInstallPrompt
 * event captured by usePwaInstall.
 */
import { Download, Check, AlertCircle, RefreshCw } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import { usePwaInstall } from '@/hooks/usePwaInstall'

export function SettingsAbout() {
  const { t } = useI18n()
  const { canInstall, isInstalled, install, error, refreshSW, updateAvailable } = usePwaInstall()

  return (
    <div className="flex flex-col gap-6 p-5">
      {/* App info */}
      <section className="flex flex-col gap-1">
        <h3 className="text-sm font-semibold text-text-primary">{t('settings.about.appName', { defaultValue: 'xbot' })}</h3>
        <p className="text-xs text-text-secondary">{t('settings.about.description', { defaultValue: 'AI 智能对话助手' })}</p>
      </section>

      {/* PWA status */}
      <section className="flex flex-col gap-3">
        <h3 className="text-sm font-semibold text-text-primary">{t('settings.about.pwa', { defaultValue: '应用' })}</h3>

        {/* Installed */}
        {isInstalled && (
          <div className="flex items-center gap-2 rounded-md bg-bg-tertiary px-3 py-2 text-xs text-status-running">
            <Check className="size-4" />
            <span>{t('settings.about.installed', { defaultValue: '已安装到桌面' })}</span>
          </div>
        )}

        {/* Install button */}
        {!isInstalled && canInstall && (
          <Button type="button" variant="default" onClick={() => install()} className="w-fit gap-2">
            <Download className="size-4" />
            {t('settings.about.install', { defaultValue: '安装应用' })}
          </Button>
        )}

        {/* Not installable + reason */}
        {!isInstalled && !canInstall && (
          <div className="flex items-start gap-2 rounded-md bg-bg-tertiary px-3 py-2 text-xs text-text-secondary">
            <AlertCircle className="mt-0.5 size-4 shrink-0" />
            <span>
              {error ?? t('settings.about.notInstallable', {
                defaultValue: '当前浏览器不支持安装。请使用 Chrome / Edge / Safari，并确保通过 HTTPS 或 localhost 访问。',
              })}
            </span>
          </div>
        )}

        {/* Update available */}
        {updateAvailable && (
          <Button type="button" variant="outline" onClick={() => refreshSW()} className="w-fit gap-2">
            <RefreshCw className="size-4" />
            {t('settings.about.update', { defaultValue: '更新到最新版本' })}
          </Button>
        )}
      </section>
    </div>
  )
}
