/**
 * SettingsAgent — agent behavior switches（设置 → 智能体）.
 *
 * Hosts the allow_self_compact toggle: lets the agent call compact_context
 * to compress its own context on demand. The switch writes through the
 * set_setting RPC → SettingHandlerRegistry runtime handler, which registers/
 * unregisters the compact_context tool LIVE and persists the value to
 * config.json (saveServerConfig) so the boot-time registration survives
 * restarts. Threshold-driven auto compression is NOT affected — this switch
 * only gates the agent-initiated tool.
 *
 * Vision / image preprocessing settings (视觉/图片处理) control how images are
 * resized/re-encoded before being sent to the LLM API:
 *   - max_image_edge_px: longest-edge cap (default 1024)
 *   - max_image_bytes: byte budget (default 4MB)
 *   - output_format: "auto" (transparent→PNG, else JPEG) | "jpeg" (always JPEG)
 *   - jpeg_quality: JPEG encoder quality (default 85)
 */
import { useCallback, useEffect, useState } from 'react'

import { getSettings, setSetting } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useI18n } from '@/providers/i18n'
import { Switch } from '@/components/ui/switch'

import { SettingsSection } from './SettingsSection'

/** 视觉/图片预处理 — 单行设置（数字输入 + 标签 + 说明） */
function VisionNumberRow({
  label,
  desc,
  value,
  placeholder,
  suffix,
  min,
  max,
  disabled,
  onChange,
}: {
  label: string
  desc: string
  value: string
  placeholder: string
  suffix?: string
  min: number
  max: number
  disabled: boolean
  onChange: (v: string) => void
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="min-w-0 flex-1">
        <p className="text-sm text-text-primary">{label}</p>
        <p className="mt-0.5 text-xs text-text-muted">{desc}</p>
      </div>
      <div className="flex shrink-0 items-center gap-1.5">
        <input
          type="number"
          min={min}
          max={max}
          value={value}
          placeholder={placeholder}
          disabled={disabled}
          onChange={(e) => onChange(e.target.value)}
          className="w-24 rounded-lg border border-border bg-bg-secondary px-2.5 py-1.5 text-sm text-text-primary
                     placeholder:text-text-muted focus:border-accent focus:outline-none disabled:opacity-50"
          aria-label={label}
        />
        {suffix ? <span className="text-xs text-text-muted">{suffix}</span> : null}
      </div>
    </div>
  )
}

/** 视觉/图片预处理 — 单行设置（下拉） */
function VisionSelectRow({
  label,
  desc,
  value,
  options,
  disabled,
  onChange,
}: {
  label: string
  desc: string
  value: string
  options: { value: string; label: string }[]
  disabled: boolean
  onChange: (v: string) => void
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="min-w-0 flex-1">
        <p className="text-sm text-text-primary">{label}</p>
        <p className="mt-0.5 text-xs text-text-muted">{desc}</p>
      </div>
      <select
        value={value}
        disabled={disabled}
        onChange={(e) => onChange(e.target.value)}
        className="w-36 shrink-0 rounded-lg border border-border bg-bg-secondary px-2.5 py-1.5 text-sm text-text-primary
                   focus:border-accent focus:outline-none disabled:opacity-50"
        aria-label={label}
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>{o.label}</option>
        ))}
      </select>
    </div>
  )
}

export function SettingsAgent() {
  const { t } = useI18n()
  const conn = useWSConnection()
  const [selfCompact, setSelfCompact] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)

  // Vision settings state
  const [maxEdgePx, setMaxEdgePx] = useState('')
  const [maxImageMB, setMaxImageMB] = useState('')
  const [outputFormat, setOutputFormat] = useState('auto')
  const [jpegQuality, setJpegQuality] = useState('')
  const [visionSaving, setVisionSaving] = useState(false)
  const [visionDirty, setVisionDirty] = useState(false)

  // Read the current value (DB value wins; get_settings injects the
  // config.json state as the default when the user never saved a value).
  useEffect(() => {
    if (!conn.connected) return
    let cancelled = false
    getSettings(conn, 'cli')
      .then((settings) => {
        if (cancelled) return
        setSelfCompact(settings['allow_self_compact'] === 'true')
        // Vision settings: empty = default (don't show placeholder value)
        setMaxEdgePx(settings['vision_max_image_edge_px'] || '')
        const bytes = settings['vision_max_image_bytes'] || ''
        setMaxImageMB(bytes ? String(Math.round(parseInt(bytes, 10) / (1024 * 1024) * 10) / 10) : '')
        setOutputFormat(settings['vision_output_format'] || 'auto')
        setJpegQuality(settings['vision_jpeg_quality'] || '')
        setLoaded(true)
      })
      .catch(() => {
        // non-fatal — the switch stays disabled until a successful read
      })
    return () => { cancelled = true }
  }, [conn])

  const toggleSelfCompact = useCallback(async (next: boolean) => {
    setSaving(true)
    setSelfCompact(next) // optimistic — RPC round-trip is fast
    try {
      await setSetting(conn, 'cli', 'allow_self_compact', next ? 'true' : 'false')
    } catch {
      setSelfCompact(!next) // revert on failure
    } finally {
      setSaving(false)
    }
  }, [conn])

  /** 视觉设置：变更标记（脏检测） */
  const markVisionDirty = useCallback(() => setVisionDirty(true), [])

  /** 视觉设置：保存（一次 RPC 全部写入） */
  const saveVisionSettings = useCallback(async () => {
    if (!visionDirty) return
    setVisionSaving(true)
    try {
      const writes: Array<[string, string]> = []
      if (maxEdgePx.trim() !== '') {
        const n = parseInt(maxEdgePx, 10)
        if (n >= 256 && n <= 8192) writes.push(['vision_max_image_edge_px', String(n)])
      } else {
        writes.push(['vision_max_image_edge_px', '1024'])
      }
      if (maxImageMB.trim() !== '') {
        const mb = parseFloat(maxImageMB)
        if (mb > 0 && mb <= 20) writes.push(['vision_max_image_bytes', String(Math.round(mb * 1024 * 1024))])
      } else {
        writes.push(['vision_max_image_bytes', String(4 * 1024 * 1024)])
      }
      writes.push(['vision_output_format', outputFormat || 'auto'])
      if (jpegQuality.trim() !== '') {
        const q = parseInt(jpegQuality, 10)
        if (q >= 50 && q <= 100) writes.push(['vision_jpeg_quality', String(q)])
      } else {
        writes.push(['vision_jpeg_quality', '85'])
      }
      for (const [key, value] of writes) {
        await setSetting(conn, 'cli', key, value)
      }
      setVisionDirty(false)
    } catch {
      // non-fatal — user can retry
    } finally {
      setVisionSaving(false)
    }
  }, [conn, visionDirty, maxEdgePx, maxImageMB, outputFormat, jpegQuality])

  return (
    <div className="flex flex-col gap-2.5 p-4">
      <SettingsSection title={t('settings.agentBehavior')}>
        <div className="flex items-center justify-between gap-3">
          <div className="min-w-0 flex-1">
            <p className="text-sm text-text-primary">{t('settings.selfCompact')}</p>
            <p className="mt-0.5 text-xs text-text-muted">{t('settings.selfCompactDesc')}</p>
          </div>
          <Switch
            checked={selfCompact}
            onCheckedChange={(v) => void toggleSelfCompact(v)}
            disabled={!loaded || saving}
            aria-label={t('settings.selfCompact')}
          />
        </div>
      </SettingsSection>

      <SettingsSection title="视觉 / 图片处理" description="控制发给 LLM API 的图片如何预处理（缩放/重编码）。影响所有 vision 已开启的模型。">
        <div className="flex flex-col gap-3">
          <VisionNumberRow
            label="最长边上限 (px)"
            desc="超过此值自动缩放。1024 适合大多数 API（端还会二次降采样）；设 2048 保留更多细节。"
            value={maxEdgePx}
            placeholder="1024"
            suffix="px"
            min={256}
            max={8192}
            disabled={!loaded || visionSaving}
            onChange={(v) => { setMaxEdgePx(v); markVisionDirty() }}
          />
          <VisionNumberRow
            label="单图大小上限 (MB)"
            desc="预处理后超过此值降级为文本占位（含 GIF 与无法解码的格式）。"
            value={maxImageMB}
            placeholder="4"
            suffix="MB"
            min={1}
            max={20}
            disabled={!loaded || visionSaving}
            onChange={(v) => { setMaxImageMB(v); markVisionDirty() }}
          />
          <VisionSelectRow
            label="输出格式"
            desc="JPEG 体积最小（推荐）；自动 = 有透明通道保留 PNG。"
            value={outputFormat}
            disabled={!loaded || visionSaving}
            onChange={(v) => { setOutputFormat(v); markVisionDirty() }}
            options={[
              { value: 'auto', label: '自动（透明→PNG，其余 JPEG）' },
              { value: 'jpeg', label: '统一 JPEG（体积最小）' },
            ]}
          />
          <VisionNumberRow
            label="JPEG 质量"
            desc="85 是体积与质量的平衡点；70 更小但有可见压缩痕迹。"
            value={jpegQuality}
            placeholder="85"
            min={50}
            max={100}
            disabled={!loaded || visionSaving}
            onChange={(v) => { setJpegQuality(v); markVisionDirty() }}
          />
          <div className="flex items-center justify-end gap-2 pt-1">
            <button
              type="button"
              onClick={() => void saveVisionSettings()}
              disabled={!visionDirty || visionSaving || !loaded}
              className="rounded-lg bg-accent px-3.5 py-1.5 text-sm font-medium text-white
                         transition-colors hover:bg-accent/85 disabled:cursor-not-allowed disabled:opacity-40"
            >
              {visionSaving ? '…' : '保存'}
            </button>
          </div>
        </div>
      </SettingsSection>
    </div>
  )
}
