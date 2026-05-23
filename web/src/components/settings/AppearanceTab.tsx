import { useEffect, useState, useCallback } from 'react'
import { IconRefresh, IconUpload, IconDownload } from '../Icons'

import type { ShowToastFn, Theme, FontSize, Language, UserSettings } from './shared'
import { lsGet, fetchSettings, saveSettings, FONT_SIZE_MAP, DEFAULT_SETTINGS, LS_KEYS } from './shared'
import { useTranslation } from '../../i18n'
import { useSoundFeedback } from '../../hooks/useSoundFeedback'

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
  const [imageBrightness, setImageBrightness] = useState<number>(() => lsGet('image_brightness', DEFAULT_SETTINGS.image_brightness) ?? 1)
  const { t, setLocale } = useTranslation()
  const { config: soundConfig, updateConfig: updateSoundConfig, toggleEnabled: toggleSoundEnabled } = useSoundFeedback()

  // Load settings from server on mount — sync to localStorage for next page load
  useEffect(() => {
    fetchSettings().then((s) => {
      setTheme(s.theme as Theme)
      setFontSize(s.font_size as FontSize)
      setNickname(s.nickname)
      setLanguage(s.language as Language)
      const ib = s.image_brightness !== undefined ? Number(s.image_brightness) : undefined
      if (ib !== undefined && !isNaN(ib)) setImageBrightness(ib)
      // Sync server settings → localStorage so App.tsx reads correctly on refresh
      localStorage.setItem('xbot-theme', s.theme)
      localStorage.setItem('xbot-font-size', s.font_size)
      localStorage.setItem('xbot-language', s.language)
      if (s.nickname) localStorage.setItem('xbot-nickname', s.nickname)
      if (ib !== undefined) localStorage.setItem('xbot-image-brightness', String(ib))
    })
  }, [])

  // Apply theme + persist to localStorage so App.tsx reads it on next load
  useEffect(() => {
    document.documentElement.setAttribute('data-theme', theme)
    localStorage.setItem('xbot-theme', theme)
  }, [theme])

  // Apply font size + persist
  useEffect(() => {
    document.documentElement.style.setProperty('--xbot-font-size', FONT_SIZE_MAP[fontSize])
    localStorage.setItem('xbot-font-size', fontSize)
  }, [fontSize])

  // Apply language + persist
  useEffect(() => {
    setLocale(language)
    localStorage.setItem('xbot-language', language)
  }, [language, setLocale])

  // Apply image brightness + persist
  useEffect(() => {
    document.documentElement.style.setProperty('--xbot-img-brightness', String(imageBrightness))
    localStorage.setItem('xbot-image-brightness', String(imageBrightness))
  }, [imageBrightness])

  const handleSave = useCallback(async (updates: Partial<UserSettings>) => {
    // Persist each setting to localStorage so App.tsx can read it on next load
    for (const [key, value] of Object.entries(updates)) {
      if (value !== undefined && LS_KEYS[key]) {
        localStorage.setItem(LS_KEYS[key], String(value))
      }
    }
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
            handleSave({ theme: v, font_size: fontSize, nickname, language, image_brightness: imageBrightness })
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
            handleSave({ theme, font_size: v, nickname, language, image_brightness: imageBrightness })
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
            handleSave({ theme, font_size: fontSize, nickname, language, image_brightness: imageBrightness })
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
            setLocale(v)
            handleSave({ theme, font_size: fontSize, nickname, language: v, image_brightness: imageBrightness })
          }}
        >
          <option value="zh-CN">简体中文</option>
          <option value="en">English</option>
        </select>
      </div>


      <div className="settings-item">
        <label className="settings-label">{t('imageBrightnessLabel')}</label>
        <div className="flex items-center gap-3 w-full">
          <input
            type="range"
            min="0.3"
            max="1.5"
            step="0.1"
            value={imageBrightness}
            onChange={(e) => {
              const v = Number(e.target.value)
              setImageBrightness(v)
              localStorage.setItem('xbot-image-brightness', String(v))
            }}
            onBlur={() => {
              handleSave({ theme, font_size: fontSize, nickname, language, image_brightness: imageBrightness })
            }}
            className="flex-1"
                style={{ accentColor: 'var(--accent)' }}
          />
          <span className="text-xs w-10 text-right" style={{ color: 'var(--text-tertiary)' }}>{imageBrightness.toFixed(1)}</span>
        </div>
        <p className="text-xs mt-1" style={{ color: 'var(--text-tertiary)' }}>{t('imageBrightnessHint')}</p>
      </div>

      {/* Sound Feedback Settings */}
      <div className="settings-item mt-4 pt-4" style={{ borderTop: '1px solid var(--border)' }}>
        <div className="flex items-center justify-between mb-2">
          <label className="settings-label">{t('soundFeedback')}</label>
          <button
            className={`px-3 py-1 text-xs rounded-full transition-colors`}
            style={{ background: soundConfig.enabled ? 'var(--accent)' : 'var(--bg-hover)', color: soundConfig.enabled ? '#fff' : 'var(--text-tertiary)' }}
            onClick={toggleSoundEnabled}
            data-testid="sound-toggle"
          >
            {soundConfig.enabled ? t('soundOn') : t('soundOff')}
          </button>
        </div>

        {soundConfig.enabled && (
          <div className="space-y-3 mt-2">
            {/* Volume */}
            <div className="flex items-center gap-3">
              <label className="text-xs w-16" style={{ color: 'var(--text-tertiary)' }}>{t('soundVolume')}</label>
              <input
                type="range"
                min="0.1"
                max="1"
                step="0.1"
                value={soundConfig.volume}
                onChange={(e) => updateSoundConfig({ volume: Number(e.target.value) })}
                className="flex-1"
                style={{ accentColor: 'var(--accent)' }}
              />
              <span className="text-xs w-8 text-right" style={{ color: 'var(--text-tertiary)' }}>{Math.round(soundConfig.volume * 100)}%</span>
             </div>

             {/* Sent sound */}
             <div className="flex items-center gap-3">
              <label className="text-xs w-16" style={{ color: 'var(--text-tertiary)' }}>{t('soundSent')}</label>
              <select
                className="settings-select text-xs flex-1"
                value={soundConfig.sentSound}
                onChange={(e) => updateSoundConfig({ sentSound: e.target.value as 'beep' | 'chime' | 'pop' | 'none' })}
              >
                <option value="beep">{t('soundBeep')}</option>
                <option value="chime">{t('soundChime')}</option>
                <option value="pop">{t('soundPop')}</option>
                <option value="none">🔇 {t('mute')}</option>
              </select>
            </div>

            {/* Receive sound */}
            <div className="flex items-center gap-3">
              <label className="text-xs w-16" style={{ color: 'var(--text-tertiary)' }}>{t('soundReceive')}</label>
              <select
                className="settings-select text-xs flex-1"
                value={soundConfig.receiveSound}
                onChange={(e) => updateSoundConfig({ receiveSound: e.target.value as 'beep' | 'chime' | 'pop' | 'none' })}
              >
                <option value="beep">{t('soundBeep')}</option>
                <option value="chime">{t('soundChime')}</option>
                <option value="pop">{t('soundPop')}</option>
                <option value="none">🔇 {t('mute')}</option>
              </select>
            </div>

            {/* Notify sound */}
            <div className="flex items-center gap-3">
              <label className="text-xs w-16" style={{ color: 'var(--text-tertiary)' }}>{t('soundNotify')}</label>
              <select
                className="settings-select text-xs flex-1"
                value={soundConfig.notifySound}
                onChange={(e) => updateSoundConfig({ notifySound: e.target.value as 'beep' | 'chime' | 'pop' | 'none' })}
              >
                <option value="beep">{t('soundBeep')}</option>
                <option value="chime">{t('soundChime')}</option>
                <option value="pop">{t('soundPop')}</option>
                <option value="none">🔇 {t('mute')}</option>
              </select>
            </div>
          </div>
        )}
      </div>

      <div className="settings-item mt-4 pt-4" style={{ borderTop: '1px solid var(--border)' }}>
        <div className="flex gap-2 flex-wrap">
          <button
            className="settings-btn-secondary text-xs"
            onClick={() => {
              const data = JSON.stringify({ _version: 1, theme, font_size: fontSize, nickname, language, image_brightness: imageBrightness }, null, 2)
              const blob = new Blob([data], { type: 'application/json' })
              const url = URL.createObjectURL(blob)
              const a = document.createElement('a')
              a.href = url
              a.download = 'xbot-settings.json'
              a.click()
              URL.revokeObjectURL(url)
            }}
          >
            <IconUpload className="inline" /> {t('exportSettings')}
          </button>
          <button
            className="settings-btn-secondary text-xs"
            onClick={() => {
              const input = document.createElement('input')
              input.type = 'file'
              input.accept = '.json'
              input.onchange = async (e) => {
                const file = (e.target as HTMLInputElement).files?.[0]
                if (!file) return
                try {
                  const text = await file.text()
                  const data = JSON.parse(text)
                  if (data.theme) setTheme(data.theme as Theme)
                  if (data.font_size) setFontSize(data.font_size as FontSize)
                  if (data.nickname !== undefined) setNickname(data.nickname)
                  if (data.language) setLanguage(data.language as Language)
                  if (data.image_brightness !== undefined) setImageBrightness(Number(data.image_brightness))
                  handleSave({
                    theme: data.theme || theme,
                    font_size: data.font_size || fontSize,
                    nickname: data.nickname ?? nickname,
                    language: data.language || language,
                    image_brightness: data.image_brightness ?? imageBrightness,
                  })
                  showToast(t('importSuccess'), 'success')
                } catch {
                  showToast(t('importFailed'), 'error')
                }
              }
              input.click()
            }}
          >
            <IconDownload className="inline" /> {t('importSettings')}
          </button>
          <button
            className="settings-btn-secondary text-xs"
            onClick={() => {
              if (!confirm(t('confirmResetSettings'))) return
              const defaults = DEFAULT_SETTINGS
              setTheme(defaults.theme)
              setFontSize(defaults.font_size)
              setNickname(defaults.nickname)
              setLanguage(defaults.language)
              setImageBrightness(defaults.image_brightness ?? 1)
              handleSave({
                theme: defaults.theme,
                font_size: defaults.font_size,
                nickname: defaults.nickname,
                language: defaults.language,
                image_brightness: defaults.image_brightness ?? 1,
              })
              showToast(t('settingsReset'), 'success')
            }}
          >
            <IconRefresh className="inline" /> {t('resetToDefaults')}
          </button>
        </div>
      </div>
    </div>
  )
}
