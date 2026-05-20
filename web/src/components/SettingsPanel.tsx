import { useState, useRef, useEffect } from 'react'
import { useToast } from '../contexts/ToastContext'
import { useTranslation } from '../i18n'
import type { PresetCommand } from '../types'
import type { TabId } from './settings/shared'
import { TABS } from './settings/shared'
import AppearanceTab from './settings/AppearanceTab'
import SessionsTab from './settings/SessionsTab'
import PresetsTab from './settings/PresetsTab'
import LLMTab from './settings/LLMTab'
import RunnerTab from './settings/RunnerTab'
import MarketTab from './settings/MarketTab'

interface SettingsPanelProps {
  open: boolean
  onClose: () => void
  onNicknameChange?: (nickname: string) => void
  onPresetsChange?: (presets: PresetCommand[]) => void
}

export default function SettingsPanel({ open, onClose, onNicknameChange, onPresetsChange }: SettingsPanelProps) {
  const [activeTab, setActiveTab] = useState<TabId>('appearance')
  const [saving, setSaving] = useState(false)
  const [visible, setVisible] = useState(false)
  const [animating, setAnimating] = useState<'in' | 'out' | null>(null)
  const [searchQuery, setSearchQuery] = useState('')

  const panelRef = useRef<HTMLDivElement>(null)
  const { showToast } = useToast()
  const { t } = useTranslation()

  useEffect(() => {
    if (open) {
      setVisible(true)
      setAnimating('in')
    } else if (visible) {
      setAnimating('out')
    }
  }, [open])

  const handlePanelAnimEnd = (e: React.AnimationEvent) => {
    if (e.target !== e.currentTarget) return
    if (animating === 'out') {
      setVisible(false)
      setAnimating(null)
    } else if (animating === 'in') {
      setAnimating(null)
    }
  }

  if (!visible) return null

  return (
    <>
      <div
        className={`settings-backdrop${animating === 'out' ? " settings-backdrop-exit" : ""}`}
        onClick={onClose}
        onAnimationEnd={handlePanelAnimEnd}
      />
      <div
        ref={panelRef}
        className={`settings-panel${animating === 'out' ? " settings-panel-exit" : ""}`}
        role="dialog"
        aria-modal="true"
        aria-label={t('settings')}
        onAnimationEnd={handlePanelAnimEnd}
      >
        {/* Header */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', padding: '20px 24px 16px', borderBottom: '1px solid var(--border)', flexShrink: 0 }}>
          <h2 style={{ fontSize: 18, fontWeight: 600, color: 'var(--text-primary)', margin: 0, letterSpacing: '-0.01em' }}>{t('settings')}</h2>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            {saving && <span style={{ fontSize: 12, color: 'var(--text-tertiary)' }}>{t('saving')}</span>}
            <button className="settings-close-btn" onClick={onClose} data-testid="settings-close-btn" aria-label={t('closeSettings')}>×</button>
          </div>
        </div>

        {/* Tabs */}
        <div className="settings-tab-bar">
          {TABS.map((tab) => (
            <button key={tab.id} onClick={() => setActiveTab(tab.id)} className={`settings-tab ${activeTab === tab.id ? 'settings-tab-active' : ''}`}>
              {tab.icon} {t(tab.labelKey)}
            </button>
          ))}
        </div>

        {/* Search */}
        <div className="settings-search-wrap">
          <input type="text" className="settings-input" placeholder={t('searchSettings')} value={searchQuery} onChange={(e) => setSearchQuery(e.target.value)} data-testid="settings-search-input" />
        </div>

        {/* Content */}
        <div className="settings-content">
          {activeTab === 'appearance' && <AppearanceTab showToast={showToast} onNicknameChange={onNicknameChange} onSavingChange={setSaving} />}
          {activeTab === 'sessions' && <SessionsTab />}
          {activeTab === 'presets' && <PresetsTab showToast={showToast} onPresetsChange={onPresetsChange} />}
          {activeTab === 'llm' && <LLMTab showToast={showToast} />}
          {activeTab === 'runner' && <RunnerTab />}
          {activeTab === 'market' && <MarketTab />}
        </div>
      </div>
    </>
  )
}
