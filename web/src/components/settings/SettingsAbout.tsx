/**
 * SettingsAbout — 关于面板：版本信息（前端 + 后端）、检查更新（后端程序 +
 * 网页界面一起）、一键更新、重启服务，以及 PWA 安装诊断。
 *
 * 数据来源：
 *  - 后端版本/运行环境：`get_system_info` RPC（version 包的 ldflags 注入值；
 *    DEV 构建时 commit 由服务端运行时 git rev-parse 尽力补全）。
 *  - 前端版本：构建期 `__BUILD_INFO__`（vite define；release CI 传
 *    VITE_APP_VERSION/VITE_APP_CHANNEL，本地构建回退 git commit + 构建时刻）。
 *  - 更新：`check_update`（GitHub Releases，按二进制渠道）→ `apply_update`
 *    （下载二进制 + web dist + 内置插件，校验和验证，原子替换）。
 *    后端程序与网页界面在同一个 release 里发布，一次更新全部覆盖 —— UI 不
 *    区分"前端更新/后端更新"，只有一个检查入口、一个更新按钮。
 *  - 重启：`restart_server`（systemd/launchd 主动调 manager 重启；supervisord/
 *    docker/手动启动走 SIGTERM 优雅停机，是否自动恢复取决于用户的服务管理
 *    策略 —— UI 文案保持中性，不假定会不会自动重启）。
 */
import { useCallback, useEffect, useState } from 'react'
import { AlertTriangle, Check, Download, Info, RefreshCw, RotateCw, Server } from 'lucide-react'
import { cn } from '@/lib/utils'
import { Button } from '@/components/ui/button'
import { postAPI } from '@/lib/api'
import { usePwaInstall } from '@/hooks/usePwaInstall'
import { useI18n } from '@/providers/i18n'

/** get_system_info RPC 的返回结构（internal/selfupdate.SystemInfo）。 */
interface SystemInfo {
  version: string
  commit: string
  buildTime: string
  channel: string
  goVersion: string
  os: string
  arch: string
  exePath: string
  managedBy: 'systemd' | 'launchd' | 'supervisord' | 'docker' | 'none' | string
  devBuild: boolean
}

/** check_update RPC 的返回结构（internal/selfupdate.UpdateCheck）。 */
interface UpdateCheck {
  current: string
  latest: string
  tag: string
  hasUpdate: boolean
  channel: string
  url: string
  skipped: boolean
  reason: string
}

/** apply_update RPC 的返回结构（internal/selfupdate.ApplyResult）。 */
interface ApplyResult {
  newVersion: string
  tag: string
  components: string[]
  warnings: string[]
}

async function rpc<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
  return postAPI<T>('/api/rpc', { method, params })
}

/** 一行「标签: 值」的版本元数据。 */
function MetaRow({ label, value }: { label: string; value?: string }) {
  if (!value) return null
  return (
    <div className="flex items-baseline gap-2 text-xs">
      <span className="w-14 shrink-0 text-text-muted">{label}</span>
      <span className="min-w-0 break-all font-mono text-text-secondary">{value}</span>
    </div>
  )
}

/** 渠道徽标：stable 绿 / beta 黄 / nightly 紫 / dev 灰。 */
function ChannelBadge({ channel }: { channel: string }) {
  const style =
    channel === 'stable'
      ? 'bg-emerald-500/15 text-emerald-500'
      : channel === 'beta'
        ? 'bg-amber-500/15 text-amber-500'
        : channel === 'nightly'
          ? 'bg-violet-500/15 text-violet-400'
          : 'bg-bg-tertiary text-text-muted'
  return (
    <span className={cn('rounded px-1.5 py-0.5 text-[10px] font-medium', style)}>
      {channel || 'dev'}
    </span>
  )
}

export function SettingsAbout({ autoCheckUpdate = false }: { autoCheckUpdate?: boolean }) {
  const { t } = useI18n()
  const { canInstall, isInstalled, install, updateAvailable, refreshSW, diagnostics } = usePwaInstall()

  // ── 版本信息 ──
  const [sysInfo, setSysInfo] = useState<SystemInfo | null>(null)
  const [infoError, setInfoError] = useState<string | null>(null)

  // ── 检查更新 / 一键更新（后端程序 + 网页界面一起） ──
  const [checking, setChecking] = useState(false)
  const [updateInfo, setUpdateInfo] = useState<UpdateCheck | null>(null)
  const [applying, setApplying] = useState(false)
  const [applyResult, setApplyResult] = useState<ApplyResult | null>(null)
  const [updateError, setUpdateError] = useState<string | null>(null)

  // ── 重启 ──
  const [confirmRestart, setConfirmRestart] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const [restartDone, setRestartDone] = useState(false)
  const [restartError, setRestartError] = useState<string | null>(null)

  const loadSysInfo = useCallback(async () => {
    try {
      const res = await rpc<SystemInfo>('get_system_info')
      setSysInfo(res)
      setInfoError(null)
    } catch (e) {
      setInfoError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void loadSysInfo()
  }, [loadSysInfo])

  const handleCheckUpdate = useCallback(async () => {
    setChecking(true)
    setUpdateError(null)
    setUpdateInfo(null)
    setApplyResult(null)
    try {
      const res = await rpc<UpdateCheck>('check_update')
      setUpdateInfo(res)
    } catch (e) {
      setUpdateError(e instanceof Error ? e.message : String(e))
    } finally {
      setChecking(false)
    }
  }, [])

  useEffect(() => {
    if (autoCheckUpdate) void handleCheckUpdate()
  }, [autoCheckUpdate, handleCheckUpdate])

  const handleApplyUpdate = async () => {
    if (!updateInfo?.tag) return
    setApplying(true)
    setUpdateError(null)
    try {
      const res = await rpc<ApplyResult>('apply_update', { tag: updateInfo.tag })
      setApplyResult(res)
    } catch (e) {
      setUpdateError(e instanceof Error ? e.message : String(e))
    } finally {
      setApplying(false)
    }
  }

  /**
   * 重启：restart_server 立即返回（实际重启 ~800ms 后触发），随后轮询
   * get_system_info 直到服务回来（托管的服务自动恢复）或超时（手动启动
   * 的进程不会自动恢复 —— 提示用户自行启动）。
   */
  const handleRestart = async () => {
    setRestarting(true)
    setRestartError(null)
    setRestartDone(false)
    try {
      await rpc('restart_server')
    } catch (e) {
      setRestartError(e instanceof Error ? e.message : String(e))
      setRestarting(false)
      return
    }
    // 轮询等待服务回来：每 1.5s 一次，最多 40s（systemd RestartSec=5 + 启动耗时）。
    const deadline = Date.now() + 40_000
    const poll = async (): Promise<void> => {
      if (Date.now() > deadline) {
        setRestartError(t('settings.about.restartTimeout'))
        setRestarting(false)
        return
      }
      try {
        await rpc<SystemInfo>('get_system_info')
        setRestartDone(true)
        setRestarting(false)
        void loadSysInfo() // 刷新版本（更新后重启即新版本）
      } catch {
        await new Promise((r) => setTimeout(r, 1500))
        await poll()
      }
    }
    // 给响应留出 flush 时间再开始轮询（重启 800ms 后才触发）。
    await new Promise((r) => setTimeout(r, 1200))
    await poll()
  }

  const managedBy = sysInfo?.managedBy

  return (
    <div className="flex flex-col gap-5 p-4">
      {/* ── 版本信息 ── */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold text-text-primary">{t('settings.about.versionTitle')}</h3>
        {infoError && (
          <p className="text-xs" style={{ color: 'var(--status-error)' }}>
            {t('settings.about.infoError')}: {infoError}
          </p>
        )}
        {/* 后端 */}
        <div className="flex flex-col gap-1.5 rounded-xl border border-border bg-bg-secondary px-3 py-2.5">
          <div className="flex items-center gap-2">
            <Server className="size-3.5 shrink-0 text-text-muted" />
            <span className="text-xs font-medium text-text-primary">{t('settings.about.backendVersion')}</span>
            <ChannelBadge channel={sysInfo?.channel ?? ''} />
            {sysInfo?.devBuild && (
              <span className="rounded bg-bg-tertiary px-1.5 py-0.5 text-[10px] text-text-muted">DEV</span>
            )}
          </div>
          <div className="flex flex-col gap-0.5 pl-5.5">
            <MetaRow label={t('settings.about.version')} value={sysInfo?.version} />
            <MetaRow label="commit" value={sysInfo?.commit} />
            <MetaRow label={t('settings.about.built')} value={sysInfo?.buildTime} />
            <MetaRow label="runtime" value={sysInfo ? `${sysInfo.goVersion} · ${sysInfo.os}/${sysInfo.arch}` : undefined} />
          </div>
        </div>
        {/* 前端 */}
        <div className="flex flex-col gap-1.5 rounded-xl border border-border bg-bg-secondary px-3 py-2.5">
          <div className="flex items-center gap-2">
            <Download className="size-3.5 shrink-0 text-text-muted" />
            <span className="text-xs font-medium text-text-primary">{t('settings.about.frontendVersion')}</span>
            <ChannelBadge channel={__BUILD_INFO__.channel} />
            {__BUILD_INFO__.version === 'dev' && (
              <span className="rounded bg-bg-tertiary px-1.5 py-0.5 text-[10px] text-text-muted">DEV</span>
            )}
          </div>
          <div className="flex flex-col gap-0.5 pl-5.5">
            <MetaRow label={t('settings.about.version')} value={__BUILD_INFO__.version} />
            <MetaRow label="commit" value={__BUILD_INFO__.commit} />
            <MetaRow label={t('settings.about.built')} value={__BUILD_INFO__.buildTime} />
          </div>
        </div>
      </section>

      {/* ── 检查更新 / 一键更新（后端程序 + 网页界面一起） ── */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold text-text-primary">{t('settings.about.updateTitle')}</h3>
        <div className="flex flex-col gap-2 rounded-xl border border-border bg-bg-secondary px-3 py-2.5">
          <p className="text-xs text-text-muted">{t('settings.about.updateIncludes')}</p>
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              variant="outline"
              onClick={() => void handleCheckUpdate()}
              disabled={checking}
              className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            >
              <RefreshCw className={cn('size-4', checking && 'animate-spin')} />
              {checking ? t('settings.about.checking') : t('settings.about.checkUpdate')}
            </Button>
            {updateInfo?.hasUpdate && updateInfo.tag && !applyResult && (
              <Button
                type="button"
                onClick={() => void handleApplyUpdate()}
                disabled={applying}
                className="w-fit gap-2 bg-accent/14 text-accent hover:bg-accent/25"
              >
                <Download className={cn('size-4', applying && 'animate-pulse')} />
                {applying
                  ? t('settings.about.updating')
                  : t('settings.about.updateTo', { version: updateInfo.latest })}
              </Button>
            )}
          </div>

          {updateError && (
            <p className="text-xs" style={{ color: 'var(--status-error)' }}>
              {updateError}
            </p>
          )}

          {updateInfo?.skipped && (
            <p className="flex items-start gap-1.5 text-xs text-text-secondary">
              <Info className="mt-0.5 size-3.5 shrink-0 text-text-muted" />
              {updateInfo.reason}
            </p>
          )}

          {updateInfo && !updateInfo.skipped && !updateInfo.hasUpdate && (
            <p className="flex items-center gap-1.5 text-xs" style={{ color: 'var(--status-success, #22c55e)' }}>
              <Check className="size-3.5" />
              {t('settings.about.upToDate')}
            </p>
          )}

          {updateInfo?.hasUpdate && !applyResult && (
            <p className="text-xs text-text-secondary">
              {t('settings.about.currentToLatest', {
                current: updateInfo.current,
                latest: updateInfo.latest,
              })}
              {updateInfo.url && (
                <>
                  {' · '}
                  <a
                    href={updateInfo.url}
                    target="_blank"
                    rel="noreferrer"
                    className="text-accent underline-offset-2 hover:underline"
                  >
                    {t('settings.about.releaseNotes')}
                  </a>
                </>
              )}
            </p>
          )}

          {applyResult && (
            <div className="flex flex-col gap-1.5 text-xs">
              <p className="flex items-center gap-1.5" style={{ color: 'var(--status-success, #22c55e)' }}>
                <Check className="size-3.5" />
                {t('settings.about.updateDone', { version: applyResult.newVersion })}
              </p>
              <p className="text-text-secondary">
                {t('settings.about.updatedComponents', { components: applyResult.components.join(', ') })}
              </p>
              {applyResult.warnings.map((w) => (
                <p key={w} className="text-text-muted">
                  ⚠ {w}
                </p>
              ))}
              <p className="font-medium text-text-primary">{t('settings.about.restartRequired')}</p>
            </div>
          )}

          {/* 浏览器缓存的网页界面落后于服务器（SW 自动检测到新 bundle）——
              被动提示刷新，不是主动检查按钮（更新入口统一在上方）。 */}
          {updateAvailable && (
            <div className="flex flex-wrap items-center gap-2 border-t border-border pt-2">
              <span className="text-xs" style={{ color: 'var(--status-warning, #f59e0b)' }}>
                ● {t('settings.about.frontendBundleUpdate')}
              </span>
              <Button
                type="button"
                variant="outline"
                onClick={() => void refreshSW()}
                className="h-7 w-fit gap-1.5 border-border bg-bg-tertiary px-2.5 text-xs hover:bg-bg-hover"
              >
                <RefreshCw className="size-3.5" />
                {t('settings.about.refreshPage')}
              </Button>
            </div>
          )}
        </div>
      </section>

      {/* ── 重启服务 ── */}
      <section className="flex flex-col gap-2">
        <h3 className="text-sm font-semibold text-text-primary">{t('settings.about.restartTitle')}</h3>
        <div className="flex flex-col gap-2 rounded-xl border border-border bg-bg-secondary px-3 py-2.5">
          {/* 中性提示：不假定会不会自动重启 —— 是否恢复取决于用户的服务管理方式 */}
          <p className="flex items-start gap-1.5 text-xs text-text-secondary">
            <Info className="mt-0.5 size-3.5 shrink-0 text-text-muted" />
            {managedBy && managedBy !== 'none'
              ? t('settings.about.restartManagedBy', { manager: managedBy })
              : t('settings.about.restartHint')}
          </p>

          {!confirmRestart ? (
            <Button
              type="button"
              variant="outline"
              onClick={() => setConfirmRestart(true)}
              disabled={restarting}
              className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            >
              <RotateCw className="size-4" />
              {t('settings.about.restartServer')}
            </Button>
          ) : (
            <div className="flex flex-col gap-2">
              <p className="text-xs text-text-secondary">{t('settings.about.restartConfirmDesc')}</p>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  onClick={() => void handleRestart()}
                  disabled={restarting}
                  className="w-fit gap-2"
                  style={{ backgroundColor: 'rgba(239, 68, 68, 0.12)', color: 'var(--status-error)' }}
                >
                  <RotateCw className={cn('size-4', restarting && 'animate-spin')} />
                  {restarting ? t('settings.about.restarting') : t('settings.about.restartConfirm')}
                </Button>
                <Button
                  type="button"
                  variant="ghost"
                  onClick={() => setConfirmRestart(false)}
                  disabled={restarting}
                  className="w-fit"
                >
                  {t('settings.about.cancel')}
                </Button>
              </div>
            </div>
          )}

          {restartError && (
            <p className="flex items-start gap-1.5 text-xs" style={{ color: 'var(--status-error)' }}>
              <AlertTriangle className="mt-0.5 size-3.5 shrink-0" />
              {restartError}
            </p>
          )}
          {restartDone && (
            <p className="flex items-center gap-1.5 text-xs" style={{ color: 'var(--status-success, #22c55e)' }}>
              <Check className="size-3.5" />
              {t('settings.about.restartDone')}
            </p>
          )}
        </div>
      </section>

      {/* ── PWA 安装（既有） ── */}
      <section className="flex flex-col gap-2.5">
        <h3 className="text-[10px] font-semibold uppercase tracking-wider text-text-muted">
          {t('settings.about.installTitle')}
        </h3>

        {isInstalled && (
          <div
            className="flex items-center gap-2.5 rounded-xl border border-border bg-bg-secondary px-3 py-2 text-xs"
            style={{ color: 'var(--status-success, #22c55e)' }}
          >
            <Check className="size-4" />
            <span>{t('settings.about.installed')}</span>
          </div>
        )}

        {!isInstalled && canInstall && (
          <Button
            type="button"
            variant="default"
            onClick={() => install()}
            className="w-fit gap-2 bg-accent/14 text-accent hover:bg-accent/25"
          >
            <Download className="size-4" />
            {t('settings.about.install')}
          </Button>
        )}

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

        {!isInstalled && !canInstall && !(diagnostics?.isSafari) && (
          <div className="flex flex-col gap-2.5 rounded-xl border border-border bg-bg-secondary px-3 py-2 text-xs">
            <div className="flex items-start gap-2.5">
              <AlertTriangle className="mt-0.5 size-4 shrink-0" style={{ color: 'var(--status-error)' }} />
              <span className="text-text-secondary">{t('settings.about.notInstallable')}</span>
            </div>
          </div>
        )}
      </section>
    </div>
  )
}
