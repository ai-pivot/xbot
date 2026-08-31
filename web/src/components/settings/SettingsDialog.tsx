/**
 * SettingsDialog — global settings panel container (Spec 7 §3.2).
 *
 * A right-side Sheet (VSCode-style) with a left category nav and a right
 * content area. Width is fixed at 480px. The Sheet is controlled (open /
 * onOpenChange) so the launcher owns visibility.
 *
 * Categories: 外观 / 折叠 / 语言 / LLM 配置 / 账号. The LLM panel mounts its hook
 * lazily (only when selected) so a disconnected server doesn't fire RPCs on
 * every panel open. The Account panel shows current username + logout button.
 */
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Loader2, LogOut } from 'lucide-react'

import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
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

export function SettingsDialog({ open, onOpenChange }: SettingsDialogProps) {
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
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent
        side="right"
        // 固定 720px，全部 tab 统一 —— 切换 tab 零宽度跳变（按 active 切宽度
        // 的方案被否决："切换选项居然会改变 panel 宽度"）。LLM 控制台是重内容
        // 面板（订阅卡片 + 模型网格 + 4 按钮工具行），720px - w-36 导航 = 576px
        // 内容区才够一行放下 header；表单类 tab 是 flex 自适应单列布局，
        // 加宽无副作用。max-w-full 兜底小屏。
        className="flex h-full w-[720px] max-w-full flex-col gap-0 rounded-l-2xl border-l border-border p-0 shadow-2xl sm:max-w-[720px]"
      >
        <SheetHeader className="border-b border-border px-5 py-4">
          <SheetTitle>{t('settings.title')}</SheetTitle>
          <SheetDescription className="sr-only">{t('settings.title')}</SheetDescription>
        </SheetHeader>

        <div className="flex min-h-0 flex-1 flex-col sm:flex-row">
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
      </SheetContent>
    </Sheet>
  )
}
