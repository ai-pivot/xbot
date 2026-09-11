/**
 * mobileNav — 面板组件打开"agent 会话/子代理"的宿主桥。
 *
 * 桌面端用 dockview tab（`tabManager.openTab`），手机端没有 dockview：面板
 * （如 TasksPanel 的 SubAgent 行）必须请求宿主切到 agent 视图并选中对应的
 * 子代理会话，否则点击毫无反应（用户报告："手机端 task view 里 subagent
 * 无法点开交互"）。
 *
 * 模块级桥（与 plugin-runtime/editorTabs 同一模式）：MobileAppShell 挂载时
 * 注册 opener，面板调用 `openMobileAgent` —— 注册存在即手机端，返回 true。
 * 不用 window 事件（ESLint 禁 per-session 代码监听全局 window 事件）。
 */

export interface MobileAgentTarget {
  subAgentRole?: string
  subAgentInstance?: string
  parentChatID?: string
  parentChannel?: string
  agentChatID?: string
}

type MobileAgentOpener = (target: MobileAgentTarget) => void

let opener: MobileAgentOpener | null = null

export function registerMobileAgentOpener(fn: MobileAgentOpener | null): void {
  opener = fn
}

/** Open a SubAgent session in the mobile shell. Returns false when no mobile
 *  shell is registered (desktop) so callers can fall back to a dockview tab. */
export function openMobileAgent(target: MobileAgentTarget): boolean {
  if (!opener) return false
  opener(target)
  return true
}
