/**
 * SettingsDialog / SettingsPanel — global settings（卡片化重构：弹窗拆为
 * 内容区 + 壳两个导出）。
 *
 * - SettingsPanel（内容区）：左分类导航 + 右内容——dockview 设置卡片
 *   （PanelLauncher 打开的非 Tab 卡）与 Dialog 壳共用的唯一实现。
 * - SettingsDialog（弹窗壳）：手机端 MobileAppShell 专用（手机无 dockview
 *   卡片化布局）；桌面端走设置卡片（floating 悬浮卡片，拖边缘停靠平铺）。
 */
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Loader2, LogOut } from 'lucide-react'

import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { useI18n } from '@/providers/i18n'
import { useAuth } from '@/hooks/useAuth'
import { cn } from '@/lib/utils'

import { SettingsAppearance } from './SettingsAppearance'
import { SettingsInteraction } from './SettingsInteraction'
import { SettingsGeneral } from './SettingsGeneral'
import { SettingsLLM } from './SettingsLLM'
import { SettingsWebUsers } from './SettingsWebUsers'
import { SettingsSection } from './SettingsSection'
import { SettingsAbout } from './SettingsAbout'
import { SettingsDeveloper } from './SettingsDeveloper'
import { SettingsLayout } from './SettingsLayout'
import { SettingsPlugins } from './SettingsPlugins'
import { useLLMSettings } from '@/hooks/useLLMSettings'

type Category = 'appearance' | 'interaction' | 'language' | 'llm' | 'account' | 'webusers' | 'developer' | 'layout' | 'plugins' | 'about'

interface SettingsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * LLM panel with its own hook instance. Kept as a child (mounted only when the
 * LLM category is active) so RPCs fire on demand, not on every panel open.
 */
function SettingsLLMPanel() {
  const settings = useLLMSettings()
  return <SettingsLLM settings={settings} />
}

/**
 * Account panel — shows current username + logout button. After logout,
 * navigates to /login (AuthGuard will redirect if needed).
 */
function SettingsAccountPanel({ onLoggedOut }: { onLoggedOut: () => void }) {
  const { t } = useI18n()
  const { user, logout } = useAuth()
  const [loggingOut, setLoggingOut] = useState(false)

  const handleLogout = async () => {
    setLoggingOut(true)
    try {
      await logout()
    } catch {
      // ignore — logout() navigates to /login regardless
    }
    onLoggedOut()
  }

  return (
    <div className="flex flex-col gap-2.5 p-4">
      <SettingsSection title={t('settings.nav.account')} description={t('auth.currentUser')}>
        <div className="flex flex-col gap-2.5">
          <p className="text-sm text-text-primary">
            {user?.username || '—'}
          </p>
          <Button
            variant="outline"
            size="sm"
            onClick={handleLogout}
            disabled={loggingOut}
            className="w-fit gap-2 border-border bg-surface-bg hover:bg-surface-bg"
          >
            {loggingOut ? <Loader2 className="size-4 animate-spin" /> : <LogOut className="size-4" />}
            {t('auth.logout')}
          </Button>
        </div>
      </SettingsSection>
    </div>
  )
}

/**
 * SettingsPanel（内容区）：左分类导航 + 右内容。dockview 设置卡片
 * （PanelLauncher 打开，floating 悬浮卡片）与 Dialog 壳共用的唯一实现。
 * 根部 flex-1 撑满父容器（卡片内容区 / DialogContent）。
 */
export function SettingsPanel() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [active, setActive] = useState<Category>('appearance')

  const nav: { key: Category; labelKey: string }[] = [
    { key: 'appearance', labelKey: 'nav.appearance' },
    { key: 'interaction', labelKey: 'nav.interaction' },
    { key: 'language', labelKey: 'nav.language' },
    { key: 'llm', labelKey: 'nav.llm' },
    { key: 'account', labelKey: 'nav.account' },
    { key: 'webusers', labelKey: 'nav.webUsers' },
    { key: 'developer', labelKey: 'nav.developer' },
    { key: 'layout', labelKey: 'nav.layout' },
    { key: 'plugins', labelKey: 'nav.plugins' },
    { key: 'about', labelKey: 'nav.about' },
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-card-bg sm:flex-row">
      {/* Left nav — 手机（<sm）：顶部横向滚动 tab 条（w-36 侧栏会占掉 38% 屏宽，
          375px 视口下内容区仅剩 230px，LLM 控制台 header 等重内容溢出屏幕）；
          桌面（≥sm）：竖直侧栏不变 */}
      <nav className="flex w-full shrink-0 flex-row gap-1 overflow-x-auto border-b border-border bg-sidebar-bg p-2 sm:w-36 sm:flex-col sm:gap-0.5 sm:overflow-visible sm:border-r sm:border-b-0">
        {nav.map(({ key, labelKey }) => (
          <button
            key={key}
            type="button"
            aria-current={active === key}
            onClick={() => setActive(key)}
            className={cn(
              'shrink-0 whitespace-nowrap rounded-lg px-3 py-2 text-left text-sm transition-colors sm:shrink sm:whitespace-normal',
              active === key
                ? 'bg-[#6c8cff]/14 font-medium text-[#6c8cff]'
                : 'text-text-muted hover:bg-surface-bg hover:text-text-primary',
            )}
          >
            {t(`settings.${labelKey}`)}
          </button>
        ))}
      </nav>

      {/* Right content — overflow-x-hidden：表单/控制台面板无横向滚动场景，
          防止内容横向溢出产生可拖动的空白（mobile 上可感知） */}
      <div className="min-w-0 flex-1 overflow-y-auto overflow-x-hidden">
        {active === 'appearance' ? <SettingsAppearance /> : null}
        {active === 'interaction' ? <SettingsInteraction /> : null}
        {active === 'language' ? <SettingsGeneral /> : null}
        {active === 'llm' ? <SettingsLLMPanel /> : null}
        {active === 'account' ? (
          <SettingsAccountPanel onLoggedOut={() => navigate('/login', { replace: true })} />
        ) : null}
        {active === 'webusers' ? <SettingsWebUsers /> : null}
        {active === 'developer' ? <SettingsDeveloper /> : null}
        {active === 'layout' ? <SettingsLayout /> : null}
        {active === 'plugins' ? <SettingsPlugins /> : null}
        {active === 'about' ? <SettingsAbout /> : null}
      </div>
    </div>
  )
}

interface SettingsDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

/**
 * SettingsDialog（弹窗壳）：手机端 MobileAppShell 专用（手机无 dockview
 * 卡片化布局）。桌面端走设置卡片（PanelLauncher togglePanel → floating
 * 悬浮卡片，拖边缘停靠平铺）。内容区 = SettingsPanel（唯一实现）。
 */
export function SettingsDialog({ open, onOpenChange }: SettingsDialogProps) {
  const { t } = useI18n()

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        // 居中弹窗（手机端）：黄金比例矩形（φ≈1.618）——高 40vh，
        // 宽 = min(40vh×1.618, 100vw-4rem) 显式计算。⚠️ 不用 aspect-ratio：
        // DialogContent 基础类残留 w-full + sm:max-w-lg（w-full 使 aspect
        // 失效，sm:max-w-lg 把桌面宽截到 512px）。显式 w-[] 覆盖 w-full +
        // max-w-none/sm:max-w-none 清残留。
        className="flex h-[40vh] w-[min(calc(40vh*1.618),calc(100vw-4rem))] max-w-none flex-col gap-0 overflow-hidden rounded-xl p-0 sm:max-w-none"
      >
        <DialogHeader className="border-b border-border px-5 py-4">
          <DialogTitle>{t('settings.title')}</DialogTitle>
          <DialogDescription className="sr-only">{t('settings.title')}</DialogDescription>
        </DialogHeader>

        <SettingsPanel />
      </DialogContent>
    </Dialog>
  )
}
