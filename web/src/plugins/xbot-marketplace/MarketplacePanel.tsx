/**
 * xbot.marketplace — 组件市场面板。
 *
 * 从 git-based registry 浏览、搜索、安装 skill / agent / plugin。
 * 支持管理 registry（列出/添加/删除），空状态一键添加官方市场。
 * REST: /api/registry/{search,install,list-installed,config-list,config-add,config-remove}
 */
import { useCallback, useEffect, useState } from 'react'
import { postAPI } from '@/lib/api'
import { useI18n } from '@/providers/i18n'
import { Button } from '@/components/ui/button'
import {
  Loader2,
  Search,
  Download,
  Check,
  Package,
  Bot,
  Sparkles,
  Puzzle,
  Settings2,
  Plus,
  Trash2,
  X,
  RefreshCw,
} from 'lucide-react'

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

interface RegistryRef {
  name: string
  url: string
  branch?: string
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

const OFFICIAL_NAME = 'official'
const OFFICIAL_URL = 'https://github.com/ai-pivot/market'

export function MarketplacePanel() {
  const { t } = useI18n()
  const [items, setItems] = useState<RegistryItem[]>([])
  const [installed, setInstalled] = useState<Set<string>>(new Set())
  const [registries, setRegistries] = useState<RegistryRef[]>([])
  const [loading, setLoading] = useState(false)
  const [installing, setInstalling] = useState<string | null>(null)
  const [uninstalling, setUninstalling] = useState<string | null>(null)
  const [error, setError] = useState('')
  const [query, setQuery] = useState('')
  const [debouncedQuery, setDebouncedQuery] = useState('')
  const [typeFilter, setTypeFilter] = useState('')

  // registry 管理 UI 状态
  const [showRegistries, setShowRegistries] = useState(false)
  const [addName, setAddName] = useState('')
  const [addUrl, setAddUrl] = useState('')
  const [addBranch, setAddBranch] = useState('')
  const [busy, setBusy] = useState(false)

  const loadInstalled = useCallback(async () => {
    const installedRes = await postAPI<{ items: InstalledItem[] }>(
      '/api/registry/list-installed',
      {},
    )
    const installedSet = new Set<string>()
    for (const it of installedRes.items || []) {
      installedSet.add(`${it.type}:${it.name}`)
    }
    setInstalled(installedSet)
  }, [])

  const load = useCallback(async (force = false) => {
    setLoading(true)
    setError('')
    try {
      const [configRes, searchRes] = await Promise.all([
        postAPI<{ registries?: RegistryRef[] }>('/api/registry/config-list', {}),
        postAPI<{ items: RegistryItem[] }>('/api/registry/search', {
          query: debouncedQuery,
          type: typeFilter,
          refresh: force,
        }),
      ])
      setRegistries(configRes.registries || [])
      setItems(searchRes.items || [])
      await loadInstalled()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [debouncedQuery, typeFilter, loadInstalled])

  // Debounce the search box so typing doesn't hit the RPC on every keystroke.
  useEffect(() => {
    const h = setTimeout(() => setDebouncedQuery(query), 300)
    return () => clearTimeout(h)
  }, [query])

  useEffect(() => {
    void load()
  }, [load])

  const refreshAfterRegistryChange = useCallback(async () => {
    // registry 变更后重新拉配置 + 搜索（强制 pull）+ 已安装
    const [configRes, searchRes] = await Promise.all([
      postAPI<{ registries?: RegistryRef[] }>('/api/registry/config-list', {}),
      postAPI<{ items: RegistryItem[] }>('/api/registry/search', {
        query: debouncedQuery,
        type: typeFilter,
        refresh: true,
      }),
    ])
    setRegistries(configRes.registries || [])
    setItems(searchRes.items || [])
  }, [debouncedQuery, typeFilter])

  const handleInstall = useCallback(
    async (item: RegistryItem) => {
      setInstalling(item.name)
      setError('')
      try {
        await postAPI('/api/registry/install', { name: item.name })
        await loadInstalled()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setInstalling(null)
      }
    },
    [loadInstalled],
  )

  const handleUninstall = useCallback(
    async (item: RegistryItem) => {
      if (!window.confirm(`卸载 ${item.name}？`)) return
      setUninstalling(item.name)
      setError('')
      try {
        await postAPI('/api/registry/uninstall', { type: item.type, name: item.name })
        await loadInstalled()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setUninstalling(null)
      }
    },
    [loadInstalled],
  )

  const handleAddRegistry = useCallback(
    async (name: string, url: string, branch: string) => {
      if (!name || !url) {
        setError('名称和 URL 不能为空')
        return
      }
      setBusy(true)
      setError('')
      try {
        await postAPI('/api/registry/config-add', { name, url, branch })
        setAddName('')
        setAddUrl('')
        setAddBranch('')
        await refreshAfterRegistryChange()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setBusy(false)
      }
    },
    [refreshAfterRegistryChange],
  )

  const handleRemoveRegistry = useCallback(
    async (name: string) => {
      if (!window.confirm(`移除 registry "${name}"？`)) return
      setBusy(true)
      setError('')
      try {
        await postAPI('/api/registry/config-remove', { name })
        await refreshAfterRegistryChange()
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        setBusy(false)
      }
    },
    [refreshAfterRegistryChange],
  )

  const handleAddOfficial = useCallback(async () => {
    await handleAddRegistry(OFFICIAL_NAME, OFFICIAL_URL, '')
  }, [handleAddRegistry])

  const filterTabs = [
    { label: '全部', value: '' },
    { label: 'Skills', value: 'skill' },
    { label: 'Agents', value: 'agent' },
    { label: 'Plugins', value: 'plugin' },
  ]

  const noRegistries = registries.length === 0

  return (
    <div className="flex h-full flex-col">
      {/* 搜索栏 + 源管理入口 */}
      <div className="border-b p-3 space-y-2">
        <div className="flex gap-2">
          <div className="relative flex-1">
            <Search className="absolute left-2 top-2.5 h-4 w-4 text-muted-foreground" />
            <input
              type="text"
              placeholder={t('marketplace.search', { defaultValue: '搜索组件...' })}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              className="w-full rounded-md border bg-background pl-8 pr-3 py-2 text-sm"
            />
          </div>
          <Button
            size="sm"
            variant="outline"
            onClick={() => void load(true)}
            disabled={loading}
            title="刷新组件源（git pull）"
            className="shrink-0"
          >
            {loading ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />}
          </Button>
          <Button
            size="sm"
            variant={showRegistries ? 'default' : 'outline'}
            onClick={() => setShowRegistries((v) => !v)}
            title="管理组件源"
            className="shrink-0"
          >
            <Settings2 className="h-4 w-4" />
            <span className="ml-1 hidden sm:inline">源</span>
            {registries.length > 0 && (
              <span className="ml-1 rounded bg-background/20 px-1 text-xs">{registries.length}</span>
            )}
          </Button>
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

      {/* Registry 管理面板 */}
      {showRegistries && (
        <div className="border-b bg-muted/30 p-3 space-y-2">
          <div className="flex items-center justify-between">
            <span className="text-xs font-medium text-muted-foreground">组件源 (registries)</span>
            <button onClick={() => setShowRegistries(false)} className="text-muted-foreground hover:text-foreground">
              <X className="h-3.5 w-3.5" />
            </button>
          </div>

          {/* 已配置列表 */}
          {registries.length === 0 ? (
            <div className="text-xs text-muted-foreground py-1">还没有配置组件源</div>
          ) : (
            <div className="space-y-1">
              {registries.map((r) => (
                <div key={r.name} className="flex items-center justify-between gap-2 rounded border bg-background px-2 py-1">
                  <div className="min-w-0">
                    <div className="text-xs font-medium truncate">{r.name}</div>
                    <div className="text-xs text-muted-foreground truncate">{r.url}</div>
                  </div>
                  <button
                    disabled={busy}
                    onClick={() => handleRemoveRegistry(r.name)}
                    className="shrink-0 text-muted-foreground hover:text-destructive disabled:opacity-50"
                    title="移除"
                  >
                    <Trash2 className="h-3.5 w-3.5" />
                  </button>
                </div>
              ))}
            </div>
          )}

          {/* 添加表单 */}
          <div className="space-y-1.5 pt-1">
            <div className="flex gap-1.5">
              <input
                type="text"
                placeholder="名称 (如 official)"
                value={addName}
                onChange={(e) => setAddName(e.target.value)}
                className="w-1/3 rounded-md border bg-background px-2 py-1 text-xs"
              />
              <input
                type="text"
                placeholder="git URL (https://github.com/owner/repo)"
                value={addUrl}
                onChange={(e) => setAddUrl(e.target.value)}
                className="flex-1 rounded-md border bg-background px-2 py-1 text-xs"
              />
            </div>
            <div className="flex gap-1.5">
              <input
                type="text"
                placeholder="分支 (可选，默认 main)"
                value={addBranch}
                onChange={(e) => setAddBranch(e.target.value)}
                className="flex-1 rounded-md border bg-background px-2 py-1 text-xs"
              />
              <Button
                size="sm"
                disabled={busy || !addName || !addUrl}
                onClick={() => handleAddRegistry(addName, addUrl, addBranch)}
                className="h-7"
              >
                {busy ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Plus className="h-3.5 w-3.5" />}
                添加
              </Button>
            </div>
          </div>
        </div>
      )}

      {/* 错误 */}
      {error && <div className="px-3 py-2 text-sm text-destructive border-b">{error}</div>}

      {/* 空状态：无组件源 */}
      {noRegistries && !loading && !showRegistries && (
        <div className="flex-1 flex flex-col items-center justify-center gap-3 p-6 text-center">
          <Package className="h-10 w-10 text-muted-foreground/50" />
          <div className="text-sm font-medium">还没有配置组件源</div>
          <div className="text-xs text-muted-foreground max-w-[16rem]">
            添加一个 registry 来浏览并安装技能、代理和插件。
          </div>
          <Button size="sm" onClick={handleAddOfficial} disabled={busy}>
            {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}
            添加官方市场
          </Button>
          <button
            onClick={() => setShowRegistries(true)}
            className="text-xs text-muted-foreground underline hover:text-foreground"
          >
            手动添加其他组件源
          </button>
        </div>
      )}

      {/* 组件列表 */}
      {(!noRegistries || showRegistries) && (
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
              const isThisUninstalling = uninstalling === item.name
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
                      <div className="flex shrink-0 items-center gap-1">
                        <span className="flex items-center gap-1 text-xs text-green-600">
                          <Check className="h-3.5 w-3.5" />
                          已安装
                        </span>
                        <button
                          disabled={isThisUninstalling}
                          onClick={() => handleUninstall(item)}
                          className="text-muted-foreground hover:text-destructive disabled:opacity-50"
                          title="卸载"
                        >
                          {isThisUninstalling ? (
                            <Loader2 className="h-3.5 w-3.5 animate-spin" />
                          ) : (
                            <Trash2 className="h-3.5 w-3.5" />
                          )}
                        </button>
                      </div>
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
                  <div className="flex items-center gap-2 text-xs text-muted-foreground flex-wrap">
                    <span>{item.author}</span>
                    <span>v{item.version}</span>
                    {item.registry && <span className="rounded bg-muted px-1">{item.registry}</span>}
                    {item.tags && item.tags.length > 0 && (
                      <span className="flex gap-1 flex-wrap">
                        {item.tags.slice(0, 3).map((tag) => (
                          <span key={tag} className="rounded bg-muted px-1">
                            {tag}
                          </span>
                        ))}
                      </span>
                    )}
                  </div>
                </div>
              )
            })
          )}
        </div>
      )}
    </div>
  )
}
