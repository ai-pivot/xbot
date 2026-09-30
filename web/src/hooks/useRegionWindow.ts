/**
 * useRegionWindow —— 单个 turn 的「更早展示区域」按需取回**编排**（D1 线）。
 *
 * 触发源 = IO 哨兵（分隔条自身），状态机**逐字复刻** `MessageList.tsx:1233-1275`
 * 的 `loadMoreArmedRef` arm/disarm 模式 —— 历史事故（一次手势 11 次请求 /
 * observe=2037）的根因是「observer 每次 loading 翻转都重建 + 新建 observer 立刻
 * 投递一次初始回调」，因此铁律三条：
 *   ① observer effect 的依赖**只含哨兵存在性**（`enabled`），其余状态一律 ref 现读；
 *   ② **触发即 disarm**；re-arm 仅两条路径 —— 哨兵离开视口（下一次回到视口是新
 *      手势），或「不可见 → 可见」（含 observer 首次回调就看到可见：内容短到
 *      视口不可滚动时"离开视口"物理上不可达，留这条退路，否则分页永久锁死）；
 *   ③ 请求失败**不自动重试**（进入 retry 态，用户点击重试）；连续失败
 *      `MAX_AUTO_FAILURES` 次后连「新手势」也不再自动发请求（保守停止），
 *      只保留手动重试 —— 避免弱网下的自动请求循环。
 *
 * 数据通路（唯一）：`RegionActionsContext.loadRegionSegment` →
 * `POST /api/regions` → `dispatchIterationsLoaded`（状态机单通道；hook 不碰 store）。
 * in-flight 去重（同一 turn 同时只允许一个段请求）在 Provider 侧（按 turnID 的
 * promise 表）—— 行组件可能被虚拟列表反复挂卸，去重必须活在行之外。
 */
import { useCallback, useEffect, useRef, useState } from 'react'

import { useRegionActions } from '@/components/agent/RegionActionsContext'

/** 分隔条三态（与 RegionsDivider 的 data-state 一一对应）。 */
export type RegionWindowStatus = 'idle' | 'loading' | 'error'

/** 连续失败到此值即「保守停止」自动触发（手动重试永远可用）。 */
export const MAX_AUTO_REGION_FAILURES = 2

export interface RegionWindowOptions {
  /** 目标 turn（0 = 无 turn 归属 ⇒ 永不触发：standalone / legacy 行）。 */
  turnID: number
  /** 该 turn 仍剩的更早展示区域数（后端 `regions_before`；0/undefined = 到顶）。 */
  regionsBefore: number | undefined
  /** 当前已加载窗口的**最小迭代号**（`POST /api/regions` 的 before_iteration）。 */
  beforeIteration: number | undefined
}

export interface RegionWindow {
  /** 是否挂哨兵（= 分隔条是否渲染）。false ⇒ 无新增 DOM、无 observer、零请求。 */
  enabled: boolean
  status: RegionWindowStatus
  /** 交给 `RegionsDivider` 挂在自己根节点上的哨兵 ref。 */
  sentinelRef: React.RefObject<HTMLDivElement | null>
  /** 手动重试（retry 态按钮）。 */
  retry: () => void
}

export function useRegionWindow({
  turnID,
  regionsBefore,
  beforeIteration,
}: RegionWindowOptions): RegionWindow {
  const actions = useRegionActions()
  const enabled = (regionsBefore ?? 0) > 0 && beforeIteration !== undefined && turnID > 0

  const [status, setStatus] = useState<RegionWindowStatus>('idle')
  const sentinelRef = useRef<HTMLDivElement | null>(null)

  // ── ref 现读（observer 回调绝不捕获过期闭包）────────────────────────────
  const armedRef = useRef(false)
  const visibleRef = useRef<boolean | null>(null)
  const failStreakRef = useRef(0)
  const statusRef = useRef<RegionWindowStatus>('idle')
  statusRef.current = status
  const turnIDRef = useRef(turnID)
  turnIDRef.current = turnID
  const beforeIterRef = useRef(beforeIteration)
  beforeIterRef.current = beforeIteration
  const loadRef = useRef(actions.loadRegionSegment)
  loadRef.current = actions.loadRegionSegment

  /** 一次段请求（唯一入口：IO 触发与手动重试共用）。 */
  const run = useCallback(() => {
    if (statusRef.current === 'loading') return
    const before = beforeIterRef.current
    if (before === undefined) return
    statusRef.current = 'loading'
    setStatus('loading')
    void (async () => {
      let ok = false
      try {
        ok = await loadRef.current(turnIDRef.current, before)
      } catch {
        ok = false
      }
      if (ok) {
        failStreakRef.current = 0
        statusRef.current = 'idle'
        setStatus('idle')
        return
      }
      failStreakRef.current += 1
      statusRef.current = 'error'
      setStatus('error')
    })()
  }, [])

  // ── 哨兵 observer：唯一触发源（arm/disarm 状态机；deps 只含 enabled）─────
  useEffect(() => {
    armedRef.current = false
    visibleRef.current = null
    if (!enabled) return
    const el = sentinelRef.current
    if (!el || typeof IntersectionObserver === 'undefined') return

    const observer = new IntersectionObserver(
      (entries) => {
        const entry = entries[0]
        if (!entry) return
        const visible = entry.isIntersecting
        const wasVisible = visibleRef.current
        visibleRef.current = visible
        if (!visible) {
          // ① 哨兵离开视口 ⇒ 归还触发权（下次回到视口是一次新手势）
          armedRef.current = true
          return
        }
        // ② 由不可见变为可见（wasVisible === null 也算：首次回调就看到可见）
        if (wasVisible !== true) armedRef.current = true
        if (!armedRef.current) return // 本轮「可见回合」已触发过 ⇒ 不再发请求
        if (statusRef.current === 'loading') return
        if (failStreakRef.current >= MAX_AUTO_REGION_FAILURES) return // 保守停止
        armedRef.current = false // 触发即 disarm
        run()
      },
      { root: null, threshold: 0 },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [enabled, run])

  const retry = useCallback(() => {
    run()
  }, [run])

  return { enabled, status, sentinelRef, retry }
}
