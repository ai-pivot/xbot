import { useState } from 'react'
import { Download, FileJson, Radio, Terminal } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Switch } from '@/components/ui/switch'
import { exportSession, downloadSession } from '@/components/agent/api'
import { useDeveloperMode } from '@/hooks/useDeveloperMode'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useI18n } from '@/providers/i18n'
import { SettingsSection } from './SettingsSection'

/**
 * Settings → 开发者 — developer-only tools (hidden from the default UI).
 *
 * - 开发者工具开关：启用后 AgentPanel 顶部显示 SSE REC 按钮（录制所有 WS/SSE
 *   消息为 .ev 文件，供重放测试复现 bug）。
 * - 导出 Turn+Iter 顺序 / benchmark JSONL：从原「关于」tab 迁移过来的
 *   调试导出功能（排查线性一致性 / benchmark 复现）。
 */
export function SettingsDeveloper() {
  const { enabled, setEnabled } = useDeveloperMode()
  const { t } = useI18n()
  const [devExporting, setDevExporting] = useState(false)
  const [devExportResult, setDevExportResult] = useState('')
  const sessionStore = useSessionStore()

  return (
    <div className="flex flex-col gap-2.5 p-4">
      <SettingsSection
        title={t('settings.developer.toolsTitle')}
        description={t('settings.developer.toolsDesc')}
      >
        <div className="flex items-center justify-between">
          <span className="flex items-center gap-2 text-sm">
            <Radio className="size-4 text-text-muted" />
            {t('settings.developer.enableRec')}
          </span>
          <Switch checked={enabled} onCheckedChange={setEnabled} />
        </div>
      </SettingsSection>

      <SettingsSection title={t('settings.developer.exportSection')} description={t('settings.developer.exportDesc')}>
        <div className="flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <Terminal className="size-4 shrink-0 text-text-muted" />
            <span className="text-text-secondary">{t('settings.developer.exportTurnIterDesc')}</span>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            disabled={devExporting || !sessionStore.activeSession}
            onClick={async () => {
              const s = sessionStore.activeSession
              if (!s) return
              setDevExporting(true)
              setDevExportResult('')
              try {
                const data = await exportSession({ channel: s.channel, chatID: s.chatID })
                const lines: string[] = []
                lines.push(`# Session: ${s.channel}:${s.chatID}`)
                lines.push(`# Model: ${data.model || 'unknown'}`)
                lines.push(`# Messages: ${data.messages.length}`)
                lines.push(`# Exported: ${new Date().toISOString()}`)
                lines.push('')
                for (const msg of data.messages) {
                  const content = typeof msg.content === 'string' ? msg.content : JSON.stringify(msg.content)
                  const preview = content.slice(0, 80).replace(/\n/g, ' ')
                  const iterInfo = msg.detail ? ' [has detail]' : ''
                  lines.push(`role=${msg.role} content="${preview}${content.length > 80 ? '...' : ''}"${iterInfo}`)
                }
                lines.push('')
                lines.push('# Records (append-only history):')
                if (data.records && data.records.length > 0) {
                  for (const r of data.records) {
                    const content = (r.content || '').slice(0, 60).replace(/\n/g, ' ')
                    lines.push(`  hid=${r.history_id} type=${r.record_type} turn=${r.turn_id ?? 0} role=${r.role || '-'} content="${content}${(r.content || '').length > 60 ? '...' : ''}"`)
                  }
                } else {
                  lines.push('  (no records)')
                }
                const text = lines.join('\n')
                const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
                const url = URL.createObjectURL(blob)
                const a = document.createElement('a')
                a.href = url
                a.download = `session-${s.chatID.replace(/[^a-zA-Z0-9_-]/g, '_').slice(0, 40)}-turn-iter.txt`
                document.body.appendChild(a)
                a.click()
                document.body.removeChild(a)
                URL.revokeObjectURL(url)
                setDevExportResult(t('settings.developer.exportedMsgs', { count: data.messages.length }))
              } catch (err) {
                setDevExportResult(t('settings.developer.exportFailed', { msg: err instanceof Error ? err.message : String(err) }))
              } finally {
                setDevExporting(false)
              }
            }}
          >
            <Download className="size-4" />
            {devExporting ? t('settings.developer.exporting') : t('settings.developer.exportTurnIterBtn')}
          </Button>

          <div className="mt-1 flex items-center gap-2">
            <FileJson className="size-4 shrink-0 text-text-muted" />
            <span className="text-text-secondary">{t('settings.developer.exportMulticaDesc')}</span>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            disabled={devExporting || !sessionStore.activeSession}
            onClick={async () => {
              const s = sessionStore.activeSession
              if (!s) return
              setDevExporting(true)
              setDevExportResult('')
              try {
                await downloadSession({ channel: s.channel, chatID: s.chatID }, 'multica')
                setDevExportResult(t('settings.developer.exportedMultica'))
              } catch (err) {
                setDevExportResult(t('settings.developer.exportFailed', { msg: err instanceof Error ? err.message : String(err) }))
              } finally {
                setDevExporting(false)
              }
            }}
          >
            <Download className="size-4" />
            {devExporting ? t('settings.developer.exporting') : t('settings.developer.exportMulticaBtn')}
          </Button>

          <div className="mt-1 flex items-center gap-2">
            <FileJson className="size-4 shrink-0 text-text-muted" />
            <span className="text-text-secondary">{t('settings.developer.exportBenchmarkDesc')}</span>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            disabled={devExporting || !sessionStore.activeSession}
            onClick={async () => {
              const s = sessionStore.activeSession
              if (!s) return
              setDevExporting(true)
              setDevExportResult('')
              try {
                await downloadSession({ channel: s.channel, chatID: s.chatID }, 'benchmark')
                setDevExportResult(t('settings.developer.exportedBenchmark'))
              } catch (err) {
                setDevExportResult(t('settings.developer.exportFailed', { msg: err instanceof Error ? err.message : String(err) }))
              } finally {
                setDevExporting(false)
              }
            }}
          >
            <Download className="size-4" />
            {devExporting ? t('settings.developer.exporting') : t('settings.developer.exportBenchmarkBtn')}
          </Button>

          <div className="mt-1 flex items-center gap-2">
            <FileJson className="size-4 shrink-0 text-text-muted" />
            <span className="text-text-secondary">{t('settings.developer.exportOpenAIDesc')}（{`{model, messages:[...]}`}）</span>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            disabled={devExporting || !sessionStore.activeSession}
            onClick={async () => {
              const s = sessionStore.activeSession
              if (!s) return
              setDevExporting(true)
              setDevExportResult('')
              try {
                await downloadSession({ channel: s.channel, chatID: s.chatID }, 'openai')
                setDevExportResult(t('settings.developer.exportedOpenAI'))
              } catch (err) {
                setDevExportResult(t('settings.developer.exportFailed', { msg: err instanceof Error ? err.message : String(err) }))
              } finally {
                setDevExporting(false)
              }
            }}
          >
            <Download className="size-4" />
            {devExporting ? t('settings.developer.exporting') : t('settings.developer.exportOpenAIBtn')}
          </Button>

          <div className="mt-1 flex items-center gap-2">
            <FileJson className="size-4 shrink-0 text-text-muted" />
            <span className="text-text-secondary">{t('settings.developer.exportCodexDesc')}</span>
          </div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="w-fit gap-2 border-border bg-bg-tertiary hover:bg-bg-hover"
            disabled={devExporting || !sessionStore.activeSession}
            onClick={async () => {
              const s = sessionStore.activeSession
              if (!s) return
              setDevExporting(true)
              setDevExportResult('')
              try {
                await downloadSession({ channel: s.channel, chatID: s.chatID }, 'codex')
                setDevExportResult(t('settings.developer.exportedCodex'))
              } catch (err) {
                setDevExportResult(t('settings.developer.exportFailed', { msg: err instanceof Error ? err.message : String(err) }))
              } finally {
                setDevExporting(false)
              }
            }}
          >
            <Download className="size-4" />
            {devExporting ? t('settings.developer.exporting') : t('settings.developer.exportCodexBtn')}
          </Button>
          {devExportResult && (
            <span className="text-text-muted">{devExportResult}</span>
          )}
          {!sessionStore.activeSession && (
            <span className="text-text-muted">{t('settings.developer.noActiveSession')}</span>
          )}
        </div>
      </SettingsSection>
    </div>
  )
}
