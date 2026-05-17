import { useState, useEffect, useCallback, memo } from 'react'
import { useTranslation, type I18nKey } from '../i18n'

interface ShortcutGroup {
  title: string
  items: { keys: string; description: string }[]
}

const shortcutGroups: ShortcutGroup[] = [
  {
    title: 'navigation',
    items: [
      { keys: 'j', description: 'scrollDown' },
      { keys: 'k', description: 'scrollUp' },
      { keys: 'gg', description: 'goToTop' },
      { keys: 'G', description: 'goToBottom' },
      { keys: 'Ctrl+u', description: 'halfPageUp' },
      { keys: 'Ctrl+d', description: 'halfPageDown' },
    ],
  },
  {
    title: 'actions',
    items: [
      { keys: 'Ctrl+K', description: 'openCommandPalette' },
      { keys: '?', description: 'toggleKeyboardHelp' },
    ],
  },
  {
    title: 'mediaControl',
    items: [
      { keys: 'Space', description: 'playPause' },
      { keys: '←/→', description: 'seekBackForward' },
      { keys: '↑/↓', description: 'volumeUpDown' },
    ],
  },
]

export const KeyboardHelpPanel = memo(function KeyboardHelpPanel() {
  const { t } = useTranslation()
  const [visible, setVisible] = useState(false)

  const toggle = useCallback(() => setVisible(v => !v), [])
  const close = useCallback(() => setVisible(false), [])

  // Listen for ? key to toggle
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      const el = document.activeElement
      if (el && ((el as HTMLElement).tagName === 'INPUT' || (el as HTMLElement).tagName === 'TEXTAREA' || (el as HTMLElement).isContentEditable)) {
        return
      }

      if (e.key === '?' && !e.ctrlKey && !e.metaKey && !e.altKey) {
        e.preventDefault()
        toggle()
      }
      if (e.key === 'Escape' && visible) {
        e.preventDefault()
        close()
      }
    }

    window.addEventListener('keydown', handleKeyDown)
    return () => window.removeEventListener('keydown', handleKeyDown)
  }, [toggle, close, visible])

  // Expose toggle for external use
  useEffect(() => {
    const handler = () => toggle()
    window.addEventListener('toggle-keyboard-help', handler)
    return () => window.removeEventListener('toggle-keyboard-help', handler)
  }, [toggle])

  if (!visible) return null

  return (
    <div className="keyboard-help-overlay" onClick={close}>
      <div className="keyboard-help-panel" onClick={(e) => e.stopPropagation()}>
        <div className="keyboard-help-header">
          <h3>{t('keyboardHelp')}</h3>
          <button className="keyboard-help-close-btn" onClick={close} aria-label="Close">
            ✕
          </button>
        </div>

        <div className="keyboard-help-body">
          {shortcutGroups.map(group => (
            <div key={group.title} className="keyboard-help-group">
              <h4 className="keyboard-help-group-title">{t(group.title as I18nKey)}</h4>
              <div className="keyboard-help-items">
                {group.items.map(item => (
                  <div key={item.keys} className="keyboard-help-item">
                    <kbd className="keyboard-help-key">{item.keys}</kbd>
                    <span className="keyboard-help-desc">{t(item.description as I18nKey)}</span>
                  </div>
                ))}
              </div>
            </div>
          ))}
        </div>

        <div className="keyboard-help-footer">
          <span className="keyboard-help-hint">Press <kbd>?</kbd> or <kbd>Esc</kbd> to close</span>
        </div>
      </div>
    </div>
  )
})

export default KeyboardHelpPanel
