/**
 * EmbedPanelShell —— **宿主嵌入模式的根**：只挂面板本体，不挂任何 web 自己的 chrome。
 *
 * 用户原话（2026-10-14）：「这种嵌套 web view 应该代理原 web 的导航事件，并且**去掉多余的 ui**」
 * 「嵌套 webview **不要做多余的渲染**」。
 *
 * ── 它到底少渲染了什么（对照 `MobileAppShell`）────────────────────────────────
 * ⛔ **不**渲染：会话列表 / 消息区 / 抽屉 / `← 工具` 头部 / 页签行（文件·搜索·信息·任务·终端）
 *    / 底部 InfoBar / 全屏 work 视图 —— 这些在原生壳里由**原生 chrome** 提供，重复渲一份
 *    就是用户截图里那套"套娃"。
 * ✅ **只**渲染：`renderPanelBody()` 返回的面板本体（与移动端外壳**同一份**实现）。
 *
 * 进入条件：`?xbot_panel=<id>&xbot_embed=1`（解析见 `workspace/panelRequest.ts`；
 * 由 `App.tsx` 判定并分流，⛔ 本文件不做 URL 解析，避免第二份真源）。
 *
 * 导航代理：挂载时向宿主发一条 `nav/ready`（宿主据此设标题、确认嵌入已生效），
 * 见 `workspace/hostBridge.ts`。
 */
import { lazy, Suspense, useEffect, useMemo, useState } from 'react'

import { usePluginViewPanels } from '@/plugin-runtime/usePluginViewPanels'
import { useActiveSSESubscription } from '@/hooks/useActiveSSESubscription'
import { useSessionStore } from '@/hooks/useSessionStore'
import { useTabManager } from '@/hooks/useTabManager'
import { useTerminal } from '@/hooks/useTerminal'
import { useWSConnection } from '@/hooks/useWSConnection'
import { mobileTerminalProps } from '@/workspace/panelProps'
import { postHostNav } from '@/workspace/hostBridge'
import { renderPanelBody, type PluginViewRef } from '@/workspace/panelRender'
import type { SidebarPanel } from '@/components/sidebar/RightSidebar'

/** 终端会话面板按需加载（与移动端外壳同一策略：不进首屏包）。 */
const TerminalPanel = lazy(() =>
  import('@/workspace/panels/TerminalPanel').then((m) => ({ default: m.TerminalPanel })))

export function EmbedPanelShell({ panelId }: { panelId: SidebarPanel }) {
  const tabManager = useTabManager()
  // 终端面板：点列表项 ⇒ 在本体内切到会话（**web 内部**导航，不惊动宿主 ——
  // 宿主嵌入的就是这个面板，会话仍属于 web 的终端实现）。
  const [activeTerminalId, setActiveTerminalId] = useState<string | null>(null)
  const terminalManager = useTerminal(tabManager, (terminalId) => setActiveTerminalId(terminalId))
  const pluginPanels = usePluginViewPanels('right_sidebar')

  // ── 实时通道（2026-10-14 修）：嵌入态此前**从不建立 SSE 订阅** ──────────────────────
  // 订阅的唯一建立点是 `useActiveSSESubscription`，它此前**只有 `AgentPanel` 一个消费方**
  // ⇒ 嵌入态（只渲面板本体，不挂 `AgentPanel`）无人 `addSubscription`
  // ⇒ `MultiSSEManager.connect()` 因 `!chatID` 直接 return（`providers/sseConnection.ts:200-202`）
  // ⇒ `ws.connected` 恒 `false` ⇒ 任务/终端/文件面板**只吃打开那一刻的 REST 快照**
  //（`TasksPanel` 因此显示 `sidebar.disconnectedHint`「实时连接已断开，状态可能延迟」）。
  // 嵌入态**本就有**会话上下文（`App.tsx` 里 `SessionStoreProvider` 同样挂载）⇒ 用它订阅即可。
  const ws = useWSConnection()
  const session = useSessionStore()
  const activeSession = session.activeSession
  useActiveSSESubscription({
    ws,
    chatID: activeSession?.chatID ?? null,
    channel: activeSession?.channel ?? 'web',
  })

  const pluginViewsMap = useMemo(
    () => new Map<string, PluginViewRef>(pluginPanels.map((p) => [p.id, { pluginId: p.pluginId, view: p.view }])),
    [pluginPanels],
  )

  // 挂载握手：`nav/ready`（宿主不可用时静默 no-op —— 纯浏览器打开本页也不报错）。
  useEffect(() => {
    postHostNav({ kind: 'ready', text: panelId })
  }, [panelId])

  if (panelId === 'terminal' && activeTerminalId) {
    return (
      <div className="h-full min-h-0 overflow-hidden">
        <Suspense fallback={null}>
          <TerminalPanel {...mobileTerminalProps(activeTerminalId)} />
        </Suspense>
      </div>
    )
  }

  return (
    <div className="h-full min-h-0 overflow-hidden">
      {renderPanelBody(panelId, tabManager, pluginViewsMap, terminalManager)}
    </div>
  )
}
