import { useEffect, useMemo, useState } from 'react'
import { Check, ChevronDown, Search } from 'lucide-react'
import { toast } from 'sonner'

import { Input } from '@/components/ui/input'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { selectModel } from '@/components/agent/api'
import { useWSConnection } from '@/hooks/useWSConnection'
import { useI18n } from '@/providers/i18n'
import type { ModelEntry, Subscription } from '@/types/shared'
import { ThinkingModeControl, thinkingModeLabel, type ThinkingModeValue } from './ThinkingModeControl'

interface ModelSelectorProps {
  channel: string
  chatID: string
  currentSubID: string
  currentModel: string
  subscriptions: Subscription[]
  modelEntries: ModelEntry[]
  thinkingMode: string
  busy: boolean
  saving?: boolean
  onModelSelected: () => void | Promise<unknown>
  onThinkingModeChange: (mode: ThinkingModeValue) => Promise<boolean>
}

export function ModelSelector({
  channel,
  chatID,
  currentSubID,
  currentModel,
  subscriptions,
  modelEntries,
  thinkingMode,
  busy,
  saving = false,
  onModelSelected,
  onThinkingModeChange,
}: ModelSelectorProps) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [selecting, setSelecting] = useState(false)
  const disabled = busy || saving || selecting || !chatID

  useEffect(() => {
    if (disabled) setOpen(false)
  }, [disabled])

  const groups = useMemo(() => {
    const query = search.trim().toLowerCase()
    const grouped = new Map<string, { subName: string; entries: ModelEntry[] }>()
    for (const entry of modelEntries) {
      if (query && !entry.model.toLowerCase().includes(query) && !entry.sub_name.toLowerCase().includes(query)) {
        continue
      }
      const group = grouped.get(entry.sub_id)
      if (group) group.entries.push(entry)
      else grouped.set(entry.sub_id, { subName: entry.sub_name || entry.sub_id, entries: [entry] })
    }
    return Array.from(grouped, ([subID, group]) => ({ subID, ...group }))
  }, [modelEntries, search])

  const currentSubName = subscriptions.find((sub) => sub.id === currentSubID)?.name ?? ''

  const handleSelect = async (entry: ModelEntry) => {
    if (disabled || entry.status === 'disabled') return
    setSelecting(true)
    try {
      await selectModel(ws, channel, entry.sub_id, entry.model, chatID)
      await onModelSelected()
      setOpen(false)
      toast.success(t('sidebar.modelSwitched', { model: entry.model }))
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t('sidebar.modelSwitchFailed'))
    } finally {
      setSelecting(false)
    }
  }

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          disabled={disabled}
          aria-label={t('agent.modelSelector')}
          title={busy ? t('agent.busy') : currentSubName || currentModel}
          className="flex h-7 min-w-0 max-w-48 items-center gap-1 rounded-md px-2 text-xs text-text-secondary transition-colors hover:bg-bg-tertiary hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-50"
        >
          <span className="min-w-0 truncate font-mono">{currentModel || '—'}</span>
          <span className="shrink-0 font-mono text-[10px] text-text-muted">{thinkingModeLabel(thinkingMode)}</span>
          <ChevronDown className="size-3 shrink-0 text-text-muted" />
        </button>
      </PopoverTrigger>
      <PopoverContent className="w-80 max-h-[60vh] p-0 flex flex-col" align="end" sideOffset={6} collisionPadding={8}>
        <div className="border-b border-border p-2 shrink-0">
          <div className="relative">
            <Search className="absolute left-2 top-1/2 size-3 -translate-y-1/2 text-muted-foreground" />
            <Input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder={t('settings.searchModels')}
              className="h-7 pl-7 text-xs"
            />
          </div>
        </div>
        <div className="overflow-y-auto overflow-x-hidden min-h-0">
          {groups.map((group) => (
            <div key={group.subID}>
              <div className="bg-bg-secondary px-2 py-1 text-[10px] font-medium text-muted-foreground">
                {group.subName}
              </div>
              {group.entries.map((entry) => {
                const active = entry.sub_id === currentSubID && entry.model === currentModel
                return (
                  <button
                    key={`${entry.sub_id}-${entry.model}`}
                    type="button"
                    disabled={disabled || entry.status === 'disabled'}
                    onClick={() => void handleSelect(entry)}
                    className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm transition-colors hover:bg-accent/10 disabled:cursor-not-allowed disabled:opacity-50 ${active ? 'font-medium text-accent' : 'text-text-secondary'}`}
                  >
                    <span className={`size-1.5 shrink-0 rounded-full ${entry.status === 'normal' ? 'bg-status-done' : entry.status === 'offline' ? 'bg-status-waiting' : 'bg-text-muted'}`} />
                    <span className="min-w-0 flex-1 truncate">{entry.model}</span>
                    {active ? <Check className="size-3 shrink-0" /> : null}
                  </button>
                )
              })}
            </div>
          ))}
          {groups.length === 0 ? (
            <div className="px-3 py-4 text-center text-xs text-muted-foreground">{t('agent.none')}</div>
          ) : null}
        </div>
        <div className="border-t border-border p-3 shrink-0">
          <ThinkingModeControl
            value={thinkingMode}
            disabled={disabled}
            onValueCommit={async (mode) => {
              const ok = await onThinkingModeChange(mode)
              toast[ok ? 'success' : 'error'](ok ? t('settings.saved') : t('settings.saveFailed'))
              return ok
            }}
          />
        </div>
      </PopoverContent>
    </Popover>
  )
}
