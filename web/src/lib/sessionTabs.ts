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
import { sessionSwitch } from '@/lib/sessionSwitch'

/** 打开（或聚焦）主会话的 desktop agent tab。 */
export function openAgentSessionTab(
  tabManager: TabManager,
  chatID: string,
  channel: string = 'web',
  title?: string,
): void {
  if (!chatID) return
  // ⛔ 契约（用户，多次）：「会话只要开始切换就应该渲染 loading」。
  // 在点击的**同一帧**同步进入切换态：所有可见面板据此只渲染 loading，直到目标面板
  // 历史就绪（AgentPanel 里 end）。切换窗口期里 dockview 建/激活 tab 的布局变化帧
  // 不再可能把旧面板（正常态：历史区+托盘+输入框）以未兑现尺寸画出来 —— 用户截图
  // 「一闪而过的错乱帧」从结构上消失。key 与 tab 逻辑键同构。
  sessionSwitch.begin(`agent:${channel || 'web'}:${chatID}`)
  tabManager.openTab({
    type: 'agent',
    title: title || chatID,
    icon: 'bot',
    closable: true,
    data: { filePath: chatID, channel: channel || 'web' },
  })
}
