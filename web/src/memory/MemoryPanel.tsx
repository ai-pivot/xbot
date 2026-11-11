/**
 * MemoryPanel —— 长期记忆面板（列出 / 编辑 / 删除）。
 *
 * 之前没有这个面板：记忆只能靠 `memory_search` / `memory_manage` 工具从对话里看，
 * 用户在 UI 上既看不到注入到上下文里的到底是什么，也没法删掉一条垃圾记忆
 * （实测遇到 importance=24440 的乱码行霸榜，只能靠工具逐条处理）。
 *
 * 取数 scope 跟 activeSession 走（服务端按会话解析 tenant）。
 */
import { useCallback, useEffect, useMemo, useState } from 'react'

import { useSessionStore } from '@/hooks/useSessionStore'

import { deleteMemoryItem, fetchMemories, updateMemoryItem, type MemoryItem } from './api'

const TYPES = ['all', 'fact', 'preference', 'decision', 'event', 'skill'] as const

const TYPE_LABEL: Record<string, string> = {
  all: '全部',
  fact: '事实',
  preference: '偏好',
  decision: '决策',
  event: '事件',
  skill: '技能',
}

interface Draft {
  content: string
  keywords: string
  tags: string
  importance: number
}

export function MemoryPanel() {
  const { activeSession } = useSessionStore()
  const scope = useMemo(
    () => ({ channel: activeSession?.channel ?? 'web', chat_id: activeSession?.chatID ?? '' }),
    [activeSession?.channel, activeSession?.chatID],
  )

  const [items, setItems] = useState<MemoryItem[]>([])
  const [type, setType] = useState<string>('all')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<number | null>(null)
  const [draft, setDraft] = useState<Draft | null>(null)
  const [busy, setBusy] = useState<number | null>(null)

  const load = useCallback(async () => {
    if (!scope.chat_id) {
      setItems([])
      return
    }
    setLoading(true)
    setError(null)
    try {
      setItems(await fetchMemories(scope, { type, limit: 200 }))
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [scope, type])

  useEffect(() => {
    void load()
  }, [load])

  const beginEdit = (m: MemoryItem) => {
    setEditing(m.id)
    setDraft({
      content: m.content,
      keywords: m.keywords ?? '',
      tags: m.tags ?? '',
      importance: m.importance,
    })
  }

  const save = async (id: number) => {
    if (!draft) return
    setBusy(id)
    setError(null)
    try {
      await updateMemoryItem(scope, { id, ...draft })
      setEditing(null)
      setDraft(null)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  const remove = async (id: number) => {
    if (!window.confirm('删除这条记忆？删除后不可恢复，也不会再被注入到上下文。')) return
    setBusy(id)
    setError(null)
    try {
      await deleteMemoryItem(scope, id)
      await load()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="flex h-full flex-col" data-testid="memory-panel">
      <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
        <span className="text-[13px] font-medium">记忆</span>
        <span className="rounded-full bg-app-accent/15 px-1.5 py-0.5 text-[11px] text-app-accent">
          {items.length}
        </span>
        <select
          value={type}
          onChange={(e) => setType(e.target.value)}
          className="rounded border border-border bg-transparent px-1.5 py-0.5 text-[11px]"
          data-testid="memory-type-filter"
        >
          {TYPES.map((t) => (
            <option key={t} value={t}>
              {TYPE_LABEL[t]}
            </option>
          ))}
        </select>
        <button
          type="button"
          onClick={() => void load()}
          disabled={loading}
          className="text-[11px] text-app-accent hover:underline disabled:opacity-40"
        >
          刷新
        </button>
      </div>

      {error ? (
        <div className="border-b border-destructive/30 bg-destructive/10 px-3 py-1.5 text-[11px] text-destructive">
          {error}
        </div>
      ) : null}

      {!scope.chat_id ? (
        <div className="p-4 text-[12px] text-slate-500">先选中一个会话，再看它所属 owner 的长期记忆。</div>
      ) : (
        <ul className="min-h-0 flex-1 overflow-y-auto">
          {items.map((m) => (
            <li key={m.id} className="border-b border-border/60 px-3 py-2" data-testid="memory-item">
              <div className="flex items-center gap-2 text-[11px] text-slate-500">
                <span className="rounded bg-slate-500/15 px-1.5 py-0.5">{TYPE_LABEL[m.type] ?? m.type}</span>
                <span title="importance">★ {m.importance.toFixed(2)}</span>
                <span>{m.created_at}</span>
                <span className="ml-auto flex items-center gap-2">
                  <button
                    type="button"
                    onClick={() => beginEdit(m)}
                    disabled={busy === m.id}
                    className="text-app-accent hover:underline disabled:opacity-40"
                  >
                    编辑
                  </button>
                  <button
                    type="button"
                    onClick={() => void remove(m.id)}
                    disabled={busy === m.id}
                    className="text-destructive hover:underline disabled:opacity-40"
                    data-testid="memory-delete"
                  >
                    删除
                  </button>
                </span>
              </div>

              {editing === m.id && draft ? (
                <div className="mt-1.5 space-y-1.5">
                  <textarea
                    value={draft.content}
                    onChange={(e) => setDraft({ ...draft, content: e.target.value })}
                    rows={4}
                    className="w-full rounded border border-border bg-transparent px-2 py-1 text-[12px]"
                    data-testid="memory-edit-content"
                  />
                  <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
                    <input
                      value={draft.keywords}
                      onChange={(e) => setDraft({ ...draft, keywords: e.target.value })}
                      placeholder="keywords"
                      className="rounded border border-border bg-transparent px-1.5 py-0.5"
                    />
                    <input
                      value={draft.tags}
                      onChange={(e) => setDraft({ ...draft, tags: e.target.value })}
                      placeholder="tags"
                      className="rounded border border-border bg-transparent px-1.5 py-0.5"
                    />
                    <input
                      type="number"
                      min={0}
                      max={1}
                      step={0.05}
                      value={draft.importance}
                      onChange={(e) => setDraft({ ...draft, importance: Number(e.target.value) })}
                      className="w-16 rounded border border-border bg-transparent px-1.5 py-0.5"
                      title="importance 0–1（超出会被服务端钳制）"
                    />
                    <button
                      type="button"
                      onClick={() => void save(m.id)}
                      disabled={busy === m.id}
                      className="rounded bg-app-accent px-2 py-0.5 text-white disabled:opacity-40"
                      data-testid="memory-save"
                    >
                      保存
                    </button>
                    <button
                      type="button"
                      onClick={() => {
                        setEditing(null)
                        setDraft(null)
                      }}
                      className="rounded border border-border px-2 py-0.5"
                    >
                      取消
                    </button>
                  </div>
                </div>
              ) : (
                <p className="mt-1 whitespace-pre-wrap break-words text-[12px] leading-relaxed">
                  {m.content}
                </p>
              )}
            </li>
          ))}
          {items.length === 0 && !loading ? (
            <li className="p-4 text-[12px] text-slate-500">没有记忆。</li>
          ) : null}
        </ul>
      )}
    </div>
  )
}
