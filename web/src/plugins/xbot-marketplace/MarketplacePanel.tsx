/**
 * xbot.marketplace — 组件市场面板。
 *
 * 从 git-based registry 浏览、搜索、安装 skill / agent / plugin。
 * REST: /api/registry/search, /api/registry/install, /api/registry/list-installed
 */
import { useCallback, useEffect, useState } from 'react'
import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { Button } from '@/components/ui/button'
import { Loader2, Search, Download, Check, Package, Bot, Sparkles, Puzzle } from 'lucide-react'

interface RegistryItem {
  type: string
  name: string
  description: string
  author: string
  version: string
  tags?: string[]
  registry?: string
}

interface InstalledItem {
  type: string
  name: string
}

const TYPE_ICONS: Record<string, typeof Sparkles> = {
  skill: Sparkles,
  agent: Bot,
  plugin: Puzzle,
  package: Package,
}

const TYPE_LABELS: Record<string, string> = {
  skill: 'Skill',
  agent: 'Agent',
  plugin: 'Plugin',
  package: 'Package',
}

export function MarketplacePanel() {
  const { t } = useI18n()
  const [items, setItems] = useState<RegistryItem[]>([])
  const [installed, setInstalled] = useState<Set<string>>(new Set())
  const [loading, setLoading] = useState(false)
  const [installing, setInstalling] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [typeFilter, setTypeFilter] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    setError('')
    try {
      const [searchRes, installedRes] = await Promise.all([
        postAPI<{ items: RegistryItem[] }>('/api/registry/search', { query, type: typeFilter }),
        postAPI<{ items: InstalledItem[] }>('/api/registry/list-installed', {}),
      ])
      setItems(searchRes.items || [])
      const installedSet = new Set<string>()
      for (const it of installedRes.items || []) {
        installedSet.add(`${it.type}:${it.name}`)
      }
      setInstalled(installedSet)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [query, typeFilter])

  useEffect(() => {
    load()
  }, [load])

  const handleInstall = useCallback(
    async (item: RegistryItem) => {
      setInstalling(item.name)
      setError('')
      try {
        await postAPI('/api/registry/install', { name: item.name })
        // Refresh installed list
        const installedRes = await postAPI<{ items: InstalledItem[] }>(
          '/api/registry/list-installed',
          {},
        )
        const installedSet = new Set<string>()
        for (const it of installedRes.items || []) {
          installedSet.add(`${it.type}:${it.name}`)
        }
        setInstalled(installedSet)
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setInstalling(null)
      }
    },
    [],
  )

  const filterTabs = [
    { label: '全部', value: '' },
    { label: 'Skills', value: 'skill' },
    { label: 'Agents', value: 'agent' },
    { label: 'Plugins', value: 'plugin' },
  ]

  return (
    <div className="flex h-full flex-col">
      {/* Search bar */}
      <div className="border-b p-3 space-y-2">
        <div className="relative">
          <Search className="absolute left-2 top-2.5 h-4 w-4 text-muted-foreground" />
          <input
            type="text"
            placeholder={t('marketplace.search', { defaultValue: '搜索组件...' })}
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            className="w-full rounded-md border bg-background pl-8 pr-3 py-2 text-sm"
          />
        </div>
        <div className="flex gap-1">
          {filterTabs.map((tab) => (
            <button
              key={tab.value}
              onClick={() => setTypeFilter(tab.value)}
              className={`rounded px-2 py-1 text-xs font-medium transition-colors ${
                typeFilter === tab.value
                  ? 'bg-primary text-primary-foreground'
                  : 'text-muted-foreground hover:bg-muted'
              }`}
            >
              {tab.label}
            </button>
          ))}
        </div>
      </div>

      {/* Error */}
      {error && (
        <div className="px-3 py-2 text-sm text-destructive border-b">
          {error}
        </div>
      )}

      {/* Items list */}
      <div className="flex-1 overflow-y-auto p-2 space-y-2">
        {loading ? (
          <div className="flex items-center justify-center py-8">
            <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
          </div>
        ) : items.length === 0 ? (
          <div className="py-8 text-center text-sm text-muted-foreground">
            {t('marketplace.empty', { defaultValue: '暂无可用组件' })}
          </div>
        ) : (
          items.map((item) => {
            const Icon = TYPE_ICONS[item.type] || Package
            const isInstalled = installed.has(`${item.type}:${item.name}`)
            const isThisInstalling = installing === item.name

            return (
              <div
                key={`${item.type}:${item.name}`}
                className="rounded-lg border p-3 space-y-2 hover:bg-muted/50 transition-colors"
              >
                <div className="flex items-start justify-between gap-2">
                  <div className="flex items-center gap-2 min-w-0">
                    <Icon className="h-4 w-4 shrink-0 text-muted-foreground" />
                    <span className="font-medium text-sm truncate">{item.name}</span>
                    <span className="shrink-0 rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                      {TYPE_LABELS[item.type] || item.type}
                    </span>
                  </div>
                  {isInstalled ? (
                    <span className="flex shrink-0 items-center gap-1 text-xs text-green-600">
                      <Check className="h-3.5 w-3.5" />
                      已安装
                    </span>
                  ) : (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={isThisInstalling}
                      onClick={() => handleInstall(item)}
                      className="shrink-0 h-7"
                    >
                      {isThisInstalling ? (
                        <Loader2 className="h-3.5 w-3.5 animate-spin" />
                      ) : (
                        <Download className="h-3.5 w-3.5" />
                      )}
                    </Button>
                  )}
                </div>
                {item.description && (
                  <p className="text-xs text-muted-foreground line-clamp-2">{item.description}</p>
                )}
                <div className="flex items-center gap-2 text-xs text-muted-foreground">
                  <span>{item.author}</span>
                  <span>v{item.version}</span>
                  {item.tags && item.tags.length > 0 && (
                    <div className="flex gap-1 flex-wrap">
                      {item.tags.slice(0, 3).map((tag) => (
                        <span key={tag} className="rounded bg-muted px-1 text-xs">
                          {tag}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              </div>
            )
          })
        )}
      </div>
    </div>
  )
}
