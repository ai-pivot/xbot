import ConfirmDialog from '../ConfirmDialog'
import { useEffect, useState, useCallback } from 'react'

import type { PresetCommand } from '../../types'
import type { ShowToastFn } from './shared'
import { fetchSettings, saveSettings } from './shared'

interface PresetsTabProps {
  showToast: ShowToastFn
  onPresetsChange?: (presets: PresetCommand[]) => void
}

export default function PresetsTab({ onPresetsChange }: PresetsTabProps) {
  const [presetList, setPresetList] = useState<PresetCommand[]>([])
  const [editingPreset, setEditingPreset] = useState<PresetCommand | null>(null)
  const [presetSaving, setPresetSaving] = useState(false)

  // Load presets on mount
  useEffect(() => {
    fetchSettings().then((s) => {
      if (s.preset_commands) {
        try {
          const parsed = JSON.parse(s.preset_commands)
          if (Array.isArray(parsed)) setPresetList(parsed)
        } catch { /* ignore */ }
      }
    })
  }, [])

  const savePresets = useCallback(async (list: PresetCommand[]) => {
    setPresetSaving(true)
    const sorted = [...list].sort((a, b) => a.sort - b.sort)
    const ok = await saveSettings({ preset_commands: JSON.stringify(sorted) })
    if (ok) {
      setPresetList(sorted)
      onPresetsChange?.(sorted)
    }
    setPresetSaving(false)
    return ok
  }, [onPresetsChange])

  const handlePresetAdd = useCallback(() => {
    setEditingPreset({
      id: Math.random().toString(36).slice(2, 10) + Date.now().toString(36),
      label: '',
      icon: '⚡',
      content: '',
      fill: false,
      sort: presetList.length,
    })
  }, [presetList.length])

  const handlePresetSave = useCallback(async (preset: PresetCommand) => {
    const exists = presetList.find(p => p.id === preset.id)
    const newList = exists
      ? presetList.map(p => p.id === preset.id ? preset : p)
      : [...presetList, preset]
    const ok = await savePresets(newList)
    if (ok) setEditingPreset(null)
  }, [presetList, savePresets])

  const [confirmDeleteId, setConfirmDeleteId] = useState<string | null>(null)

  const requestPresetDelete = (id: string) => setConfirmDeleteId(id)

  const executePresetDelete = useCallback(async () => {
    if (!confirmDeleteId) return
    const id = confirmDeleteId
    setConfirmDeleteId(null)
    const newList = presetList
      .filter(p => p.id !== id)
      .map((p, i) => ({ ...p, sort: i }))
    await savePresets(newList)
  }, [presetList, savePresets])

  const handlePresetMove = useCallback(async (id: string, direction: 'up' | 'down') => {
    const sorted = [...presetList].sort((a, b) => a.sort - b.sort)
    const idx = sorted.findIndex(p => p.id === id)
    if (idx < 0) return
    const swapIdx = direction === 'up' ? idx - 1 : idx + 1
    if (swapIdx < 0 || swapIdx >= sorted.length) return
    const temp = sorted[idx].sort
    sorted[idx] = { ...sorted[idx], sort: sorted[swapIdx].sort }
    sorted[swapIdx] = { ...sorted[swapIdx], sort: temp }
    await savePresets(sorted)
  }, [presetList, savePresets])

  const sectionClass = 'settings-section'
  const sectionTitleClass = 'settings-section-title'

  return (
    <>
    <ConfirmDialog
      open={confirmDeleteId !== null}
      message="确认删除此快捷指令？"
      onConfirm={executePresetDelete}
      onCancel={() => setConfirmDeleteId(null)}
    />
    <div className={sectionClass}>
      <div className={sectionTitleClass}>⚡ 快捷指令 Preset Commands</div>
      <p className="text-xs text-slate-500 mb-3">
        配置常用指令，在聊天输入框上方快速触发。最多 20 条。
      </p>

      {editingPreset ? (
        /* ── 编辑/新增表单 ── */
        <div className="preset-edit-form">
          <div className="settings-item">
            <label className="settings-label">图标 Icon</label>
            <input
              type="text"
              className="settings-input"
              style={{ width: '60px' }}
              maxLength={4}
              value={editingPreset.icon}
              onChange={(e) => setEditingPreset({ ...editingPreset, icon: e.target.value })}
            />
          </div>
          <div className="settings-item">
            <label className="settings-label">名称 Label *</label>
            <input
              type="text"
              className="settings-input"
              placeholder="例如：代码审查"
              maxLength={20}
              value={editingPreset.label}
              onChange={(e) => setEditingPreset({ ...editingPreset, label: e.target.value })}
            />
          </div>
          <div className="settings-item">
            <label className="settings-label">内容 Content *</label>
            <textarea
              className="settings-input"
              style={{ minHeight: '80px', resize: 'vertical' }}
              placeholder="点击后发送的内容..."
              maxLength={2000}
              value={editingPreset.content}
              onChange={(e) => setEditingPreset({ ...editingPreset, content: e.target.value })}
            />
            <p className="text-xs text-slate-600 mt-1">{editingPreset.content.length}/2000</p>
          </div>
          <div className="settings-item">
            <label className="settings-label flex items-center gap-2">
              <input
                type="checkbox"
                checked={editingPreset.fill ?? false}
                onChange={(e) => setEditingPreset({ ...editingPreset, fill: e.target.checked })}
              />
              填充模式（填入输入框而非直接发送）
            </label>
          </div>
          <div className="flex gap-2 mt-3">
            <button
              className="settings-action-btn"
              onClick={() => handlePresetSave(editingPreset)}
              disabled={!editingPreset.label.trim() || !editingPreset.content.trim() || presetSaving}
            >
              {presetSaving ? '保存中...' : '💾 保存'}
            </button>
            <button
              className="settings-action-btn settings-action-danger"
              onClick={() => setEditingPreset(null)}
            >
              取消
            </button>
          </div>
        </div>
      ) : (
        /* ── 列表视图 ── */
        <>
          {presetList.length === 0 ? (
            <div className="text-center py-6 text-slate-500">
              <p className="text-2xl mb-2">📭</p>
              <p className="text-sm">暂无快捷指令</p>
            </div>
          ) : (
            <div className="preset-list">
              {[...presetList].sort((a, b) => a.sort - b.sort).map((p) => (
                <div key={p.id} className="preset-item">
                  <div className="preset-item-main">
                    <span className="preset-item-icon">{p.icon || '⚡'}</span>
                    <div className="preset-item-info">
                      <span className="preset-item-label">{p.label}</span>
                      <span className="preset-item-content">{p.content.length > 40 ? p.content.slice(0, 40) + '...' : p.content}</span>
                    </div>
                  </div>
                  <div className="preset-item-actions">
                    <button
                      className="preset-action-btn"
                      onClick={() => handlePresetMove(p.id, 'up')}
                      title="上移"
                      disabled={presetSaving}
                    >↑</button>
                    <button
                      className="preset-action-btn"
                      onClick={() => handlePresetMove(p.id, 'down')}
                      title="下移"
                      disabled={presetSaving}
                    >↓</button>
                    <button
                      className="preset-action-btn"
                      onClick={() => setEditingPreset({ ...p })}
                      title="编辑"
                      disabled={presetSaving}
                    >✏️</button>
                    <button
                      className="preset-action-btn preset-action-delete"
                      onClick={() => requestPresetDelete(p.id)}
                      title="删除"
                      disabled={presetSaving}
                    >🗑️</button>
                  </div>
                </div>
              ))}
            </div>
          )}
          <button
            className="settings-action-btn w-full mt-3"
            onClick={handlePresetAdd}
            disabled={presetList.length >= 20 || presetSaving}
          >
            ➕ 新增指令 {presetList.length > 0 ? `(${presetList.length}/20)` : ''}
          </button>
        </>
      )}
    </div>
    </>
  )
}