import { useTranslation } from '../../i18n'
import ConfirmDialog from '../ConfirmDialog'
import { useEffect, useState, useCallback } from 'react'

import type { ShowToastFn, LLMConfig } from './shared'
import { PROVIDER_OPTIONS } from './shared'

interface LLMTabProps {
  showToast: ShowToastFn
}

export default function LLMTab({ showToast }: LLMTabProps) {
  const [llmConfig, setLlmConfig] = useState<LLMConfig | null>(null)
  const [llmConfigLoading, setLlmConfigLoading] = useState(false)
  const [llmSaving, setLlmSaving] = useState(false)
  const [llmMaxContext, setLlmMaxContext] = useState<number>(0)
  const [llmMaxContextSaving, setLlmMaxContextSaving] = useState(false)

  const [llmFormProvider, setLlmFormProvider] = useState('openai')
  const [llmFormBaseUrl, setLlmFormBaseUrl] = useState('')
  const [llmFormApiKey, setLlmFormApiKey] = useState('')
  const [llmFormModel, setLlmFormModel] = useState('')
  const [llmError, setLlmError] = useState('')
  const [llmFormMaxOutputTokens, setLlmFormMaxOutputTokens] = useState('')
  const [llmFormThinkingMode, setLlmFormThinkingMode] = useState('auto')

  const fetchLLMConfig = useCallback(async () => {
    setLlmConfigLoading(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config')
      const data = await resp.json()
      if (data.ok) {
        setLlmConfig({
          provider: data.provider,
          base_url: data.base_url,
          model: data.model,
          models: data.models || [],
          is_global: !!data.is_global,
        })
        setLlmMaxContext(data.max_context || 0)
      } else {
        setLlmConfig(null)
      }
    } catch {
      setLlmError('获取配置失败')
    }
    setLlmConfigLoading(false)
  }, [])

  // Load LLM config on mount
  useEffect(() => {
    fetchLLMConfig()
  }, [fetchLLMConfig])

  const handleLLMAdd = useCallback(async () => {
    if (!llmFormBaseUrl.trim() || !llmFormApiKey.trim()) {
      setLlmError('Base URL 和 API Key 为必填项')
      return
    }
    setLlmSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          provider: llmFormProvider,
          base_url: llmFormBaseUrl.trim(),
          api_key: llmFormApiKey.trim(),
          model: llmFormModel.trim(),
          max_output_tokens: llmFormMaxOutputTokens ? parseInt(llmFormMaxOutputTokens) : 0,
          thinking_mode: llmFormThinkingMode,
        }),
      })
      const data = await resp.json()
      if (data.ok) {
        setLlmFormBaseUrl('')
        setLlmFormApiKey('')
        setLlmFormModel('')
        setLlmFormMaxOutputTokens('')
        setLlmFormThinkingMode('auto')
        await fetchLLMConfig()
        showToast(t('configSaved'), 'success')
      } else {
        setLlmError(data.error || '保存失败')
        showToast(data.error || '保存失败', 'error')
      }
    } catch {
      setLlmError('网络错误')
      showToast(t('networkError'), 'error')
    }
    setLlmSaving(false)
  }, [llmFormProvider, llmFormBaseUrl, llmFormApiKey, llmFormModel, llmFormMaxOutputTokens, llmFormThinkingMode, fetchLLMConfig, showToast])

  const handleLLMSetModel = useCallback(async (model: string) => {
    setLlmSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config/model', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ model }),
      })
      const data = await resp.json()
      if (data.ok) {
        await fetchLLMConfig()
        showToast(t('modelSwitched'), 'success')
      } else {
        setLlmError(data.error || '切换模型失败')
        showToast(data.error || '切换模型失败', 'error')
      }
    } catch {
      setLlmError('网络错误')
      showToast(t('networkError'), 'error')
    }
    setLlmSaving(false)
  }, [fetchLLMConfig, showToast])

  const [confirmLLMDelete, setConfirmLLMDelete] = useState(false)
  const { t } = useTranslation()

  const requestLLMDelete = () => setConfirmLLMDelete(true)

  const executeLLMDelete = useCallback(async () => {
    setConfirmLLMDelete(false)
    setLlmSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-config', { method: 'DELETE' })
      const data = await resp.json()
      if (data.ok) {
        await fetchLLMConfig()
        // Clear form too
        setLlmFormBaseUrl('')
        setLlmFormApiKey('')
        setLlmFormModel('')
        setLlmFormMaxOutputTokens('')
        setLlmFormThinkingMode('auto')
        showToast(t('configDeleted'), 'success')
      } else {
        setLlmError(data.error || '删除失败')
        showToast(data.error || '删除失败', 'error')
      }
    } catch {
      setLlmError('网络错误')
      showToast(t('networkError'), 'error')
    }
    setLlmSaving(false)
  }, [fetchLLMConfig, showToast])

  const handleLLMMaxContextSave = useCallback(async () => {
    setLlmMaxContextSaving(true)
    setLlmError('')
    try {
      const resp = await fetch('/api/llm-max-context', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ max_context: llmMaxContext }),
      })
      const data = await resp.json()
      if (!data.ok) {
        setLlmError(data.error || '保存失败')
        showToast(data.error || '保存失败', 'error')
      } else {
        showToast(t('configSaved'), 'success')
      }
    } catch {
      setLlmError('网络错误')
      showToast(t('networkError'), 'error')
    }
    setLlmMaxContextSaving(false)
  }, [llmMaxContext, showToast])

  const sectionClass = 'settings-section'
  const sectionTitleClass = 'settings-section-title'
  const providerLabel = PROVIDER_OPTIONS.find(p => p.value === llmConfig?.provider)?.label || llmConfig?.provider

  return (
    <>
    <ConfirmDialog
      open={confirmLLMDelete}
      message="确认删除个人 LLM 配置？删除后将恢复使用系统默认模型。"
      onConfirm={executeLLMDelete}
      onCancel={() => setConfirmLLMDelete(false)}
    />
    <div className={sectionClass}>
      <div className={sectionTitleClass}>{t('llmTitle')}</div>

      {llmConfigLoading ? (
        <div className="settings-loading">加载中...</div>
      ) : llmConfig && !llmConfig.is_global ? (
        /* ── 个人配置：显示当前配置 + 模型切换 + 删除 ── */
        <>
          <div className="settings-desc">
            当前使用个人模型。可切换模型或删除配置以恢复系统默认。
          </div>

          <div className="settings-item">
            <label className="settings-label">提供商 Provider</label>
            <div className="settings-value">{providerLabel}</div>
          </div>

          <div className="settings-item">
            <label className="settings-label">Base URL</label>
            <div className="settings-value-mono">{llmConfig.base_url}</div>
          </div>

          <div className="settings-item">
            <label className="settings-label">当前模型 Model</label>
            {llmConfig.models.length > 0 ? (
              <select
                className="settings-select"
                value={llmConfig.model}
                onChange={(e) => handleLLMSetModel(e.target.value)}
                disabled={llmSaving}
              >
                {llmConfig.models.map(m => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </select>
            ) : (
              <div className="settings-value">{llmConfig.model || '默认'}</div>
            )}
          </div>

          {llmError && <p className="settings-error">{llmError}</p>}

          <div className="flex gap-2 mt-3">
            <button
              className="settings-action-btn settings-action-danger"
              onClick={requestLLMDelete}
              disabled={llmSaving}
            >
              🗑️ 删除配置
            </button>
          </div>

        </>
      ) : llmConfig && llmConfig.is_global ? (
        /* ── 全局配置：显示当前模型 + 添加配置入口 ── */
        <>
          <div className="settings-desc">
            当前使用系统全局模型。配置个人 LLM 后可自由选择模型。
          </div>

          {llmConfig.model && (
            <div className="settings-item">
              <label className="settings-label">当前模型 Model</label>
              <div className="settings-value">{llmConfig.model}</div>
            </div>
          )}

          {llmError && <p className="settings-error">{llmError}</p>}

          <button
            className="settings-action-btn w-full mt-2"
            onClick={() => setLlmConfig(null)}
          >
            ➕ 添加配置
          </button>
        </>
      ) : (
        /* ── 无配置：新增表单 ── */
        <>
          <div className="settings-desc">
            当前使用系统默认模型。配置个人 LLM 后可自由选择模型。
          </div>

          <div className="settings-item">
            <label className="settings-label">提供商 Provider</label>
            <select
              className="settings-select"
              value={llmFormProvider}
              onChange={(e) => setLlmFormProvider(e.target.value)}
            >
              {PROVIDER_OPTIONS.map(p => (
                <option key={p.value} value={p.value}>{p.label}</option>
              ))}
            </select>
          </div>

          <div className="settings-item">
            <label className="settings-label">Base URL *</label>
            <input
              type="text"
              className="settings-input"
              placeholder="例如: https://api.openai.com/v1"
              value={llmFormBaseUrl}
              onChange={(e) => setLlmFormBaseUrl(e.target.value)}
            />
          </div>

          <div className="settings-item">
            <label className="settings-label">API Key *</label>
            <input
              type="password"
              className="settings-input"
              placeholder="sk-..."
              value={llmFormApiKey}
              onChange={(e) => setLlmFormApiKey(e.target.value)}
            />
            <p className="settings-muted mt-1">⚠️ API Key 仅存储在服务端，不会返回到前端</p>
          </div>

          <div className="settings-item">
            <label className="settings-label">模型 Model</label>
            <input
              type="text"
              className="settings-input"
              placeholder="例如: gpt-4o, claude-sonnet-4-20250514（可选，默认用提供商推荐模型）"
              value={llmFormModel}
              onChange={(e) => setLlmFormModel(e.target.value)}
            />
          </div>

          <div className="settings-item">
            <label className="settings-label">Max Output Tokens</label>
            <input
              type="number"
              className="settings-input"
              placeholder="0 = use default"
              value={llmFormMaxOutputTokens}
              onChange={e => setLlmFormMaxOutputTokens(e.target.value)}
              min={0}
              step={256}
            />
          </div>

          <div className="settings-item">
            <label className="settings-label">Thinking Mode</label>
            <select
              className="settings-select"
              value={llmFormThinkingMode}
              onChange={e => setLlmFormThinkingMode(e.target.value)}
            >
              <option value="auto">Auto</option>
              <option value="enabled">Enabled</option>
              <option value="disabled">Disabled</option>
            </select>
          </div>

          {llmError && <p className="settings-error">{llmError}</p>}

          <button
            className="settings-action-btn w-full mt-2"
            onClick={handleLLMAdd}
            disabled={llmSaving}
          >
            {llmSaving ? '保存中...' : '💾 保存配置'}
          </button>
        </>
      )}

      {/* ── Max Context 设置（独立于 LLM 配置） ── */}
      {!llmConfigLoading && (
        <div className="settings-divider">
          <label className="settings-label">最大上下文 Max Context</label>
          <div className="flex items-center gap-2">
            <input
              type="number"
              className="settings-input flex-1"
              min={0}
              step={1000}
              value={llmMaxContext || 0}
              onChange={(e) => setLlmMaxContext(parseInt(e.target.value) || 0)}
              placeholder="0 = 使用系统默认"
            />
            <button
              className="settings-action-btn"
              onClick={handleLLMMaxContextSave}
              disabled={llmMaxContextSaving}
            >
              {llmMaxContextSaving ? '⏳' : '💾'} 保存
            </button>
          </div>
          <div className="settings-muted mt-1">
            Token 数量，0 表示使用系统默认值。值越大，可用的对话上下文越长，但消耗的 Token 越多。
          </div>
        </div>
      )}

    </div>
    </>
  )
}