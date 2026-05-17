import { useState, useRef } from 'react'

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
  const [closing, setClosing] = useState(false)
  const panelRef = useRef<HTMLDivElement>(null)

  const { showToast } = useToast()
  const { t } = useTranslation()

  const handleClose = () => {
    setClosing(true)
    setTimeout(onClose, 200)
  }

  const handleAnimationEnd = () => {
    if (closing) setClosing(false)
  }

  if (open && closing) setClosing(false)

  if (!open && !closing) return null

  return (
    <>
      <div
        className={`settings-backdrop${closing ? " settings-backdrop-exit" : ""}`}
        onClick={handleClose}
      />
      <div ref={panelRef}
        className={`settings-panel${closing ? " settings-panel-exit" : ""}`}
        role="dialog" aria-modal="true" aria-label={t('settings')}
        onAnimationEnd={handleAnimationEnd}>
        <div className="flex items-center justify-between">
          <h2 className="text-lg font-bold text-white">{t('settings')}</h2>
          <div className="flex items-center gap-2">
            {saving && <span className="text-xs text-slate-500">{t('saving')}</span>}
            <button className="settings-close-btn text-sm" onClick={handleClose} data-testid="settings-close-btn" aria-label={t('closeSettings')}>✕</button>
          </div>
        </div>

        <div className="flex gap-1 p-1 bg-slate-700/50 rounded-lg flex-shrink-0">
          {TABS.map((tab) => (
            <button
              key={tab.id}
              onClick={() => setActiveTab(tab.id)}
              className={`flex-1 text-xs py-1.5 px-2 rounded-md transition-colors ${
                activeTab === tab.id
                  ? 'bg-slate-600 text-white'
                  : 'text-slate-400 hover:text-white hover:bg-slate-700'
              }`}
            >
              {tab.icon} {t(tab.labelKey as any)}
            </button>
          ))}
        </div>

        <div style={{ flex: 1, overflowY: 'auto', minHeight: 0 }}>

        {activeTab === 'appearance' && (
          <AppearanceTab showToast={showToast} onNicknameChange={onNicknameChange} onSavingChange={setSaving} />
        )}

        {activeTab === 'sessions' && (
          <SessionsTab />
        )}

        {activeTab === 'presets' && (
          <PresetsTab showToast={showToast} onPresetsChange={onPresetsChange} />
        )}

        {activeTab === 'llm' && (
          <LLMTab showToast={showToast} />
        )}

        {activeTab === 'runner' && <RunnerTab />}

        {activeTab === 'market' && (
          <MarketTab />
        )}

        </div>
      </div>
    </>
  )
}
