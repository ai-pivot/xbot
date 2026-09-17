/**
 * sessionTabs —— 「把某个主会话切到主编辑区」的唯一入口。
 *
 * 为什么需要（session-per-tab 架构的必然后果）：desktop 的 AgentPanel 会话身份
 * 来自**它自己 tab 的 params.sessionId**（`DockviewContainer` 用 `filePath` →
 * `sessionId` 写入 panel params），而不是全局 activeSession。只改
 * `store.activeSession`（`switchSession`/`activateSession`）只会让侧栏高亮变化，
 * 主区当前 tab 依旧绑着旧 sessionId —— 用户看到「侧栏切了、窗口没切」。
 * 真正完成切换 = 打开/聚焦那个会话的 agent tab。
 *
 * tab 逻辑键由 `tabLogicalKey` 定义为 `agent:<channel>:<chatID>`，所以同一会话
 * 重复调用只会聚焦已有 tab，不会开重复 tab。
 *
 * 手机端没有 dockview（`tabManager.openTab` 会进 pending 队列静默丢失）——
 * 手机端 AgentPanel 跟随 activeSession，因此不要在那里调用本函数。
 */
import type { TabManager } from '@/hooks/useTabManager'

/** 打开（或聚焦）主会话的 desktop agent tab。 */
export function openAgentSessionTab(
  tabManager: TabManager,
  chatID: string,
  channel: string = 'web',
  title?: string,
): void {
  if (!chatID) return
  tabManager.openTab({
    type: 'agent',
    title: title || chatID,
    icon: 'bot',
    closable: true,
    data: { filePath: chatID, channel: channel || 'web' },
  })
}
