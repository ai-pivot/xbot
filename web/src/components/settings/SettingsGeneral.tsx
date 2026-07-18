/**
 * SettingsGeneral — general preferences (Spec 7 §3.5).
 *
 * Currently hosts the language switch (中文 / English). `useI18n.setLocale`
 * delegates to `changeLocale`, which persists to localStorage 'xbot-locale'
 * and switches react-i18next live; the whole app re-renders translated.
 *
 * Appearance and collapse live in their own components, surfaced as separate
 * nav categories by SettingsDialog.
 */
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Button } from '@/components/ui/button'
import { Trash2 } from 'lucide-react'
import { useI18n } from '@/providers/i18n'
import { clearWebCaches } from '@/lib/webCache'
import type { Locale } from '@/types/shared'

import { SettingsSection } from './SettingsSection'

const LOCALES: { value: Locale; label: string }[] = [
  { value: 'zh-CN', label: '中文' },
  { value: 'en', label: 'English' },
]

export function SettingsGeneral() {
  const { t, locale, setLocale } = useI18n()
  const clearCache = () => {
    clearWebCaches()
    window.location.reload()
  }

  return (
    <div className="flex flex-col">
      <SettingsSection title={t('settings.language')} description={t('settings.languageDesc')}>
        <Select value={locale} onValueChange={(v) => setLocale(v as Locale)}>
          <SelectTrigger className="w-full max-w-[320px]">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {LOCALES.map((l) => (
              <SelectItem key={l.value} value={l.value}>
                {l.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </SettingsSection>
      <SettingsSection title={t('settings.cache')}>
        <Button type="button" variant="destructive" size="sm" className="w-fit" onClick={clearCache}>
          <Trash2 data-icon="inline-start" />
          {t('settings.clearCache')}
        </Button>
      </SettingsSection>
    </div>
  )
}
