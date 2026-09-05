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
 */
import { useCallback, useEffect, useState } from 'react'

import { getSettings, setSetting } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useI18n } from '@/providers/i18n'
import { Switch } from '@/components/ui/switch'

import { SettingsSection } from './SettingsSection'

export function SettingsAgent() {
  const { t } = useI18n()
  const conn = useWSConnection()
  const [selfCompact, setSelfCompact] = useState(false)
  const [loaded, setLoaded] = useState(false)
  const [saving, setSaving] = useState(false)

  // Read the current value (DB value wins; get_settings injects the
  // config.json state as the default when the user never saved a value).
  useEffect(() => {
    if (!conn.connected) return
    let cancelled = false
    getSettings(conn, 'cli')
      .then((settings) => {
        if (cancelled) return
        setSelfCompact(settings['allow_self_compact'] === 'true')
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
    </div>
  )
}
