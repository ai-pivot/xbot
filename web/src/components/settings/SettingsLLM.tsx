/**
 * SettingsLLM — LLM config entry (Spec 7 §3.6).
 *
 * Wires four user-level knobs to WS RPCs via useLLMSettings:
 *   - model      : list_models + get_default_model + switch_model
 *   - maxContext : get/set_user_max_context
 *   - maxTokens  : get/set_user_max_output_tokens
 *   - thinking   : get/set_user_thinking_mode ("" = auto)
 *
 * Numbers commit on blur/Enter; thinking mode commits on blur. A disconnected
 * server shows a friendly notice instead of an empty form.
 */
import { useEffect, useState } from 'react'
import { toast } from 'sonner'

import { Input } from '@/components/ui/input'
import { Skeleton } from '@/components/ui/skeleton'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useLLMSettings } from '@/hooks/useLLMSettings'
import { useI18n } from '@/providers/i18n'

import { SettingsSection } from './SettingsSection'

interface SettingsLLMProps {
  settings: ReturnType<typeof useLLMSettings>
}

/** Local numeric input that buffers edits and commits a validated number on blur/Enter. */
function NumberField({
  value,
  disabled,
  onCommit,
}: {
  value: number
  disabled?: boolean
  onCommit: (n: number) => Promise<boolean>
}) {
  const { t } = useI18n()
  const [text, setText] = useState(String(value))
  useEffect(() => setText(String(value)), [value])

  const commit = () => {
    const n = Number(text)
    if (!Number.isFinite(n) || n < 0 || n === value) return
    void onCommit(n).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  return (
    <Input
      type="number"
      min={0}
      inputMode="numeric"
      value={text}
      disabled={disabled}
      onChange={(e) => setText(e.target.value)}
      onBlur={commit}
      onKeyDown={(e) => {
        if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
      }}
      className="max-w-[200px]"
    />
  )
}

export function SettingsLLM({ settings }: SettingsLLMProps) {
  const { t } = useI18n()
  const { data, loading, error, saving } = settings
  // Disable editing while a save is in flight or when the initial load failed
  // (e.g. disconnected) so users can't mutate from an empty/stale state.
  const disabled = saving || !!error

  const [thinking, setThinking] = useState(data.thinkingMode)
  useEffect(() => setThinking(data.thinkingMode), [data.thinkingMode])

  const commitThinking = () => {
    if (thinking === data.thinkingMode) return
    void settings.setThinkingMode(thinking).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  const onModelChange = (model: string) => {
    void settings.setModel(model).then((ok) => {
      toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
    })
  }

  return (
    <div className="flex flex-col">
      {/* Model */}
      <SettingsSection title={t('settings.model')} description={t('settings.modelDesc')}>
        {loading ? (
          <Skeleton className="h-9 w-full max-w-[320px]" />
        ) : (
          <Select value={data.model} onValueChange={onModelChange} disabled={disabled}>
            <SelectTrigger className="w-full max-w-[320px]">
              <SelectValue placeholder={t('settings.model')} />
            </SelectTrigger>
            <SelectContent>
              {data.models.map((m) => (
                <SelectItem key={m} value={m}>
                  {m}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </SettingsSection>

      {/* Max context */}
      <SettingsSection
        title={t('settings.maxContext')}
        description={t('settings.maxContextDesc')}
      >
        {loading ? (
          <Skeleton className="h-9 w-[200px]" />
        ) : (
          <NumberField value={data.maxContext} disabled={disabled} onCommit={settings.setMaxContext} />
        )}
      </SettingsSection>

      {/* Max output tokens */}
      <SettingsSection
        title={t('settings.maxOutputTokens')}
        description={t('settings.maxOutputTokensDesc')}
      >
        {loading ? (
          <Skeleton className="h-9 w-[200px]" />
        ) : (
          <NumberField
            value={data.maxOutputTokens}
            disabled={disabled}
            onCommit={settings.setMaxOutputTokens}
          />
        )}
      </SettingsSection>

      {/* Thinking mode */}
      <SettingsSection
        title={t('settings.thinkingMode')}
        description={t('settings.thinkingModeDesc')}
      >
        {loading ? (
          <Skeleton className="h-9 w-full max-w-[320px]" />
        ) : (
          <Input
            value={thinking}
            spellCheck={false}
            autoComplete="off"
            placeholder={t('settings.thinkingModeDesc')}
            disabled={disabled}
            onChange={(e) => setThinking(e.target.value)}
            onBlur={commitThinking}
            onKeyDown={(e) => {
              if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
            }}
            className="max-w-[320px] font-mono"
          />
        )}
      </SettingsSection>

      {error && error !== 'not_connected' ? (
        <p className="px-5 py-3 text-xs text-destructive">{t('settings.loadFailed')}: {error}</p>
      ) : null}
      {error === 'not_connected' ? (
        <p className="px-5 py-3 text-xs text-muted-foreground">{t('settings.notConnected')}</p>
      ) : null}
    </div>
  )
}
