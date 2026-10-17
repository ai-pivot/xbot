/**
 * useRegionWindow —— 单个 turn 的「更早展示区域」按需取回**编排**（D1 线）。
 *
 * ⛔⛔ 不变量（用户明确要求，2026-10-11 —— AGENTS.md 最高优先级）：**消息/上下文不连续
 * 零容忍**。用户看得见的相邻两条内容必须在真实时间线上相邻；`iter x → 加载哨兵 →
 * iter x+y（y≠1）`（中间跳过 y 条）绝不可接受。
 *
 * 契约由此变成「**在用户看到边界之前把它补上**」：
 *   ① **预取**：观察哨兵的 rootMargin 扩到 ±2 屏 ⇒ 边界还在视口之上时就开拉；
 *   ② **链式**：一段到位后若哨兵仍在预取带内，立刻续拉下一段（`scheduleChain`）——
 *      用户上滚时内容像「本就一次加载好」一样从下往上连续生长，边界始终停在视口之上；
 *      新内容把边界推离预取带后自然停止（+ 进展判定防死循环）。
 * 做不到时**宁可不渲染**：见 `RegionsDivider`（不可见哨兵）与 `derive.ts`
 * （`regionsBefore > 0` ⇒ 该 turn 的 user 行不渲染）。
 *
 * 触发源 = IO 哨兵（`RegionsDivider` 的根节点），状态机**逐字复刻**
 * `MessageList.tsx` 的 `loadMoreArmedRef` arm/disarm 模式 —— 历史事故（一次手势 11 次
 * 请求 / observe=2037）的根因是「observer 每次 loading 翻转都重建 + 新建 observer 立刻
 * 投递一次初始回调」，因此铁律三条：
 *   ① observer effect 的依赖**只含哨兵存在性**（`enabled`），其余状态一律 ref 现读；
 *   ② **触发即 disarm**；re-arm 仅两条路径 —— 哨兵离开预取带（下一次回到带内是新
 *      手势），或「不可见 → 可见」（含 observer 首次回调就看到可见）；链式续拉走
 *      `scheduleChain`，**不再**依赖 observer 重复回调；
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
import { usePrependAnchor } from '@/components/agent/PrependAnchorContext'

/** 分隔条三态（与 RegionsDivider 的 data-state 一一对应）。 */
export type RegionWindowStatus = 'idle' | 'loading' | 'error'

/** 连续失败到此值即「保守停止」自动触发（手动重试永远可用）。 */
export const MAX_AUTO_REGION_FAILURES = 2

/**
 * 预取带 = 视口上下各扩 2 屏。边界（哨兵）还在视口之上时就开拉 ⇒ 用户**永远看不到**
 * 「跳过 y 条」的缺口。链式续拉使上滚像连续加载。
 */
const PREFETCH_ROOT_MARGIN = '200% 0px 200% 0px'

export interface RegionWindowOptions {
  /** 目标 turn（0 = 无 turn 归属 ⇒ 永不触发：standalone / legacy 行）。 */
  turnID: number
  /** 该 turn 仍剩的更早展示区域数（后端 `regions_before`；0/undefined = 到顶）。 */
  regionsBefore: number | undefined
  /** 段请求游标（`POST /api/regions` 的 before_iteration）：无洞 = 窗口最小迭代号；
   *  有洞 = 洞上边界（见 AssistantMessage 的 gapTop 计算）。 */
  beforeIteration: number | undefined
  /** 已加载迭代序列中的第一个洞的上边界（iterations[i] > iterations[i-1]+1 处的
   *  iterations[i].iteration；undefined = 无洞）。熄屏恢复 × 折叠窗口会产生这种洞
   *  —— 驱动下方「自动追赶」effect（不等 IO）。 */
  gapTop?: number
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
  gapTop,
}: RegionWindowOptions): RegionWindow {
  const actions = useRegionActions()
  // prepend 锚通道（由 MessageList 提供）：段请求**发出之前**必须拍下「不许动的锚」——
  // 否则新加载的历史会把下面整体顶下去（用户 2026-10-12 明确要求：视角一动不动）。
  const prependAnchor = usePrependAnchor()
  const beginAnchorRef = useRef(prependAnchor.beginPrependAnchor)
  beginAnchorRef.current = prependAnchor.beginPrependAnchor
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
    // ⛔ 拍锚必须在**发请求之前**（MessageList 的 prepend 锚）：段落地后由列表在同一帧把
    // 「用户看到的那一行」钉回原位 ⇒ 视角一动不动（用户 2026-10-12 硬要求）。
    beginAnchorRef.current()
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

  // ── 哨兵 observer：IO 触发源（arm/disarm 状态机；deps 只含 enabled）─────
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
          // ① 哨兵离开预取带 ⇒ 归还触发权（下次回到带内是一次新手势）
          armedRef.current = true
          return
        }
        // ② 由不可见变为可见（`wasVisible === null` 也算：observer 首次回调就看到"可见"）
        if (wasVisible !== true) armedRef.current = true
        if (!armedRef.current) return // 本轮「可见回合」已触发过 ⇒ 不再发请求
        if (statusRef.current === 'loading') return
        if (failStreakRef.current >= MAX_AUTO_REGION_FAILURES) return // 保守停止
        armedRef.current = false // 触发即 disarm
        run()
      },
      // 预取带（±2 屏）—— 见 PREFETCH_ROOT_MARGIN 注释。
      { root: null, rootMargin: PREFETCH_ROOT_MARGIN, threshold: 0 },
    )
    observer.observe(el)
    return () => observer.disconnect()
  }, [enabled, run])

  const retry = useCallback(() => {
    run()
  }, [run])

  // ── 自动追赶（2026-10-02 P0：熄屏恢复 × 折叠窗口的洞）────────────────────────
  // 检测到洞（gapTop 有值）⇒ 立即自动取段填洞，**不等 IO 哨兵**——熄屏恢复场景洞的
  // 两侧 = 本地熄屏前的低号迭代 + 恢复 reload 的折叠窗口；contiguous 在洞处截断 ⇒
  // 历史冻结在熄屏时刻、所有后续完成迭代（含 tool done）都在截断区外「消失」。此时
  // 用户在底部看 live、分隔条不在视口，IO 永远不会触发——必须自动追赶。
  // 防风暴（与 IO 路径共用）：run() 的 in-flight 去重 + failStreak 保守停止 +
  // 洞填上后 gapTop 变 undefined ⇒ 本 effect 自然停止（deps 含 gapTop）。
  useEffect(() => {
    if (gapTop === undefined) return
    if (statusRef.current === 'loading') return
    if (failStreakRef.current >= MAX_AUTO_REGION_FAILURES) return
    run()
  }, [gapTop, run])

  return { enabled, status, sentinelRef, retry }
}
