import { useEffect, useState, useCallback } from 'react'

import type { ShowToastFn, Theme, FontSize, Language, UserSettings } from './shared'
import { lsGet, fetchSettings, saveSettings, FONT_SIZE_MAP, DEFAULT_SETTINGS } from './shared'
import { useTranslation } from '../../i18n'

interface AppearanceTabProps {
  showToast: ShowToastFn
  onNicknameChange?: (nickname: string) => void
  onSavingChange?: (saving: boolean) => void
}

export default function AppearanceTab({ showToast, onNicknameChange, onSavingChange }: AppearanceTabProps) {
  const [theme, setTheme] = useState<Theme>(() => lsGet('theme', DEFAULT_SETTINGS.theme))
  const [fontSize, setFontSize] = useState<FontSize>(() => lsGet('font_size', DEFAULT_SETTINGS.font_size))
  const [nickname, setNickname] = useState<string>(() => lsGet('nickname', DEFAULT_SETTINGS.nickname))
  const [language, setLanguage] = useState<Language>(() => lsGet('language', DEFAULT_SETTINGS.language))
  const { t, setLocale } = useTranslation()

  // Load settings from server on mount
  useEffect(() => {
    fetchSettings().then((s) => {
      setTheme(s.theme as Theme)
      setFontSize(s.font_size as FontSize)
      setNickname(s.nickname)
      setLanguage(s.language as Language)
    })
  }, [])

  // Apply theme
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
  }, [theme])

  // Apply font size
  useEffect(() => {
    document.documentElement.style.setProperty('--xbot-font-size', FONT_SIZE_MAP[fontSize])
  }, [fontSize])

  const handleSave = useCallback(async (updates: Partial<UserSettings>) => {
    onSavingChange?.(true)
    const ok = await saveSettings(updates)
    onSavingChange?.(false)
    if (ok) {
      showToast(t('settingsSaved'), 'success')
    } else {
      showToast(t('saveFailed'), 'error')
    }
  }, [showToast, onSavingChange, t])

  const sectionClass = 'settings-section'
  const sectionTitleClass = 'settings-section-title'

  return (
    <div className={sectionClass}>
      <div className={sectionTitleClass}>{t('appearanceTitle')}</div>

      <div className="settings-item">
        <label className="settings-label">{t('themeLabel')}</label>
        <select
          className="settings-select"
          value={theme}
          onChange={(e) => {
            const v = e.target.value as Theme
            setTheme(v)
            handleSave({ theme: v, font_size: fontSize, nickname, language })
          }}
        >
          <option value="dark">{t('dark')}</option>
          <option value="light">{t('light')}</option>
        </select>
      </div>

      <div className="settings-item">
        <label className="settings-label">{t('fontSizeLabel')}</label>
        <select
          className="settings-select"
          value={fontSize}
          onChange={(e) => {
            const v = e.target.value as FontSize
            setFontSize(v)
            handleSave({ theme, font_size: v, nickname, language })
          }}
        >
          <option value="small">{t('smallSize')}</option>
          <option value="medium">{t('mediumSize')}</option>
          <option value="large">{t('largeSize')}</option>
        </select>
      </div>

      <div className="settings-item">
        <label className="settings-label">{t('nicknameLabel')}</label>
        <input
          type="text"
          className="settings-input"
          placeholder={t('enterNickname')}
          maxLength={32}
          value={nickname}
          onChange={(e) => setNickname(e.target.value)}
          onBlur={() => {
            onNicknameChange?.(nickname)
            handleSave({ theme, font_size: fontSize, nickname, language })
          }}
          onKeyDown={(e) => {
            if (e.key === 'Enter') {
              ;(e.target as HTMLInputElement).blur()
            }
          }}
        />
      </div>

      <div className="settings-item">
        <label className="settings-label">{t('languageLabel')}</label>
        <select
          className="settings-select"
          value={language}
          onChange={(e) => {
            const v = e.target.value as Language
            setLanguage(v)
            // Update I18nProvider context to trigger immediate re-render
            setLocale(v)
            handleSave({ theme, font_size: fontSize, nickname, language: v })
          }}
        >
          <option value="zh-CN">简体中文</option>
          <option value="en">English</option>
        </select>
      </div>
    </div>
  )
}
