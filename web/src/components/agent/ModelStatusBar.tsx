/**
 * ModelStatusBar — Agent panel status bar showing model + subscription name,
 * token usage, and a quick-switch popover for model selection (Spec D §3).
 *
 * Data sources:
 *   - getSessionSubscription(ws, channel, chatID) → (subID, model)
 *   - listSubscriptions(ws) → subName lookup
 *   - listAllModelEntries(ws) → tree for quick switch popover
 *   - progressSnapshot.tokenUsage → promptTokens + completionTokens
 */
import { useCallback, useEffect, useState } from 'react'
import { toast } from 'sonner'
import { Settings2, Search, Check } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from '@/components/ui/popover'
import { useI18n } from '@/providers/i18n'
import { useWSConnection } from '@/hooks/useWSConnection'
import {
  getSessionSubscription,
  listSubscriptions,
  listAllModelEntries,
  selectModel,
} from '@/components/agent/api'
import type { Subscription, ModelEntry } from '@/types/shared'

interface ModelStatusBarProps {
  channel: string
  chatID: string | null
  tokenUsage?: { prompt: number; completion: number } | null
  thinkingMode?: string
  onOpenSettings?: () => void
  /** If provided, skips redundant getSessionSubscription/listSubscriptions RPCs */
  preloadedSubID?: string
  preloadedModel?: string
  preloadedSubs?: Subscription[]
}

function formatTokens(n: number): string {
  if (n >= 1000) return `${(n / 1000).toFixed(1)}K`
  return String(n)
}

export function ModelStatusBar({
  channel,
  chatID,
  tokenUsage,
  thinkingMode,
  onOpenSettings,
  preloadedSubID,
  preloadedModel,
  preloadedSubs,
}: ModelStatusBarProps) {
  const { t } = useI18n()
  const ws = useWSConnection()
  const [currentSubID, setCurrentSubID] = useState(preloadedSubID ?? '')
  const [currentModel, setCurrentModel] = useState(preloadedModel ?? '')
  const [subscriptions, setSubscriptions] = useState<Subscription[]>(preloadedSubs ?? [])
  const [modelEntries, setModelEntries] = useState<ModelEntry[]>([])
  const [search, setSearch] = useState('')
  const [popoverOpen, setPopoverOpen] = useState(false)

  // Load model entries (always needed for popover). Session sub + list skipped if preloaded.
  const loadState = useCallback(async () => {
    if (!chatID || !ws.connected) return
    try {
      if (preloadedSubID !== undefined && preloadedSubs !== undefined) {
        // Use preloaded data, only fetch model entries
        const entries = await listAllModelEntries(ws)
        setModelEntries(Array.isArray(entries) ? entries : [])
      } else {
        const [sessionSub, subs, entries] = await Promise.all([
          getSessionSubscription(ws, channel, chatID),
          listSubscriptions(ws),
          listAllModelEntries(ws),
        ])
        setCurrentSubID(sessionSub.subscription_id ?? '')
        setCurrentModel(sessionSub.model ?? '')
        setSubscriptions(Array.isArray(subs) ? subs : [])
        setModelEntries(Array.isArray(entries) ? entries : [])
      }
    } catch {
      // Silent fail — status bar is non-critical
    }
  }, [ws, channel, chatID, preloadedSubID, preloadedSubs])

  useEffect(() => {
    void loadState()
  }, [loadState])

  const currentSubName = subscriptions.find((s) => s.id === currentSubID)?.name ?? ''
  const thinkingLabel = thinkingMode
    ? thinkingMode === 'enabled'
      ? 'think+'
      : thinkingMode === 'disabled'
        ? 'think-'
        : 'think'
    : ''

  // Filtered entries for popover
  const filteredEntries = modelEntries.filter((e) => {
    if (!search) return true
    const q = search.toLowerCase()
    return (
      e.model.toLowerCase().includes(q) ||
      e.sub_name.toLowerCase().includes(q)
    )
  })

  // Group by subscription
  const grouped = filteredEntries.reduce<Record<string, ModelEntry[]>>((acc, e) => {
    if (!acc[e.sub_id]) acc[e.sub_id] = []
    acc[e.sub_id].push(e)
    return acc
  }, {})

  const handleSelectModel = async (entry: ModelEntry) => {
    if (entry.status === 'disabled') return
    if (!chatID) return
    try {
      await selectModel(ws, channel, entry.sub_id, entry.model, chatID)
      setCurrentSubID(entry.sub_id)
      setCurrentModel(entry.model)
      setPopoverOpen(false)
      toast.success(t('settings.saved'))
    } catch (e) {
      toast.error(e instanceof Error ? e.message : t('settings.saveFailed'))
    }
  }

  return (
    <div className="flex items-center gap-2 px-3 py-1 text-xs text-muted-foreground border-t border-border bg-bg-primary">
      {/* Model name + quick switch */}
      <Popover open={popoverOpen} onOpenChange={setPopoverOpen}>
        <PopoverTrigger asChild>
          <button
            type="button"
            className="flex items-center gap-1 hover:text-foreground transition-colors"
            disabled={!chatID}
          >
            {currentModel || '—'}
            {currentSubName && (
              <span className="text-muted-foreground/70">({currentSubName})</span>
            )}
          </button>
        </PopoverTrigger>
        <PopoverContent className="w-80 max-h-[60vh] p-0 flex flex-col" align="start" collisionPadding={8}>
          <div className="flex flex-col min-h-0">
            {/* Search */}
            <div className="border-b border-border p-2 shrink-0">
              <div className="relative">
                <Search className="absolute left-2 top-1/2 size-3 -translate-y-1/2 text-muted-foreground" />
                <Input
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  placeholder={t('settings.searchModels')}
                  className="h-7 pl-7 text-xs"
                />
              </div>
            </div>
            {/* Model list grouped by subscription */}
            <div className="overflow-y-auto overflow-x-hidden min-h-0">
              {Object.entries(grouped).map(([subID, entries]) => {
                const subName = entries[0]?.sub_name ?? subID
                return (
                  <div key={subID} className="flex flex-col">
                    <div className="bg-bg-secondary px-2 py-1 text-[10px] font-medium text-muted-foreground">
                      {subName}
                    </div>
                    {entries.map((entry) => {
                      const isActive =
                        entry.sub_id === currentSubID && entry.model === currentModel
                      return (
                        <button
                          key={`${entry.sub_id}-${entry.model}`}
                          type="button"
                          disabled={entry.status === 'disabled'}
                          onClick={() => void handleSelectModel(entry)}
                          className={`flex items-center gap-2 px-3 py-1.5 text-left text-sm hover:bg-accent/10 ${
                            entry.status === 'disabled'
                              ? 'cursor-not-allowed opacity-50'
                              : 'cursor-pointer'
                          } ${isActive ? 'text-accent font-medium' : ''}`}
                        >
                          <span className={`size-1.5 rounded-full ${
                            entry.status === 'normal'
                              ? 'bg-green-500'
                              : entry.status === 'offline'
                                ? 'bg-yellow-500'
                                : 'bg-gray-400'
                          }`} />
                          <span className="flex-1 truncate">{entry.model}</span>
                          {isActive && <Check className="size-3 text-accent" />}
                        </button>
                      )
                    })}
                  </div>
                )
              })}
              {Object.keys(grouped).length === 0 && (
                <div className="px-3 py-4 text-center text-xs text-muted-foreground">
                  {t('agent.none')}
                </div>
              )}
            </div>
            {/* Footer: manage subscriptions */}
            {onOpenSettings && (
              <div className="border-t border-border p-2 shrink-0">
                <Button
                  variant="ghost"
                  size="sm"
                  className="w-full gap-1 text-xs"
                  onClick={() => {
                    setPopoverOpen(false)
                    onOpenSettings()
                  }}
                >
                  <Settings2 className="size-3" />
                  {t('settings.manageSubscriptions')}
                </Button>
              </div>
            )}
          </div>
        </PopoverContent>
      </Popover>

      {/* Thinking indicator */}
      {thinkingLabel && (
        <span className="text-muted-foreground/70">{thinkingLabel}</span>
      )}

      {/* Token usage */}
      {tokenUsage && (
        <span className="ml-auto tabular-nums">
          {formatTokens(tokenUsage.prompt)} + {formatTokens(tokenUsage.completion)}
        </span>
      )}
    </div>
  )
}
