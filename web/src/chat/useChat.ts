/**
 * useChat — ChatStore 的 React 绑定（useSyncExternalStore）。
 *
 * immutable 快照 + 引用相等 → React 自动跳过未变化渲染。
 * chatID 切换：新 store 实例（per-chat 隔离 —— Bug 2 根治点：
 * turn_id 是 per-chat 序，全局 ref 残留拦截新会话事件在架构上不可发生）。
 */

import { useMemo, useSyncExternalStore } from 'react'
import { deriveRows, type Row } from './derive'
import { ChatStore } from './store'
import type { ChatState } from './types'

export function useChatStore(chatID: string): ChatStore {
  // per-chat 实例；chatID 变化即替换（旧实例由 GC 回收，无全局残留）。
  const store = useMemo(() => new ChatStore(chatID), [chatID])
  return store
}

/** 订阅状态机快照。 */
export function useChatState(store: ChatStore): ChatState {
  return useSyncExternalStore(store.subscribe, store.getSnapshot, store.getSnapshot)
}

/** 订阅渲染行（deriveRows 在 getSnapshot 内联 —— memo 缓存避免每帧重算）。 */
export function useChatRows(store: ChatStore): readonly Row[] {
  const subscribe = useMemo(
    () =>
      (l: () => void) => {
        const un = store.subscribe(l)
        return un
      },
    [store],
  )
  const getRows = useMemo(() => {
    let last: ChatState | null = null
    let rows: readonly Row[] = []
    return (): readonly Row[] => {
      const s = store.getSnapshot()
      if (s !== last) {
        last = s
        rows = deriveRows(s)
      }
      return rows
    }
  }, [store])
  return useSyncExternalStore(subscribe, getRows, getRows)
}
