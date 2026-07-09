import type { TabManager } from '@/hooks/useTabManager'
import type { SessionSelector, Tab } from '@/types/shared'

export function sessionForFocusedAgent(tabManager: TabManager | undefined, fallback: SessionSelector | null): SessionSelector | null {
  const activeTab = activeAgentTab(tabManager)
  if (activeTab?.data?.subAgentRole) {
    const chatID = activeTab.data.agentChatID || buildLiveAgentChatID(activeTab)
    if (chatID) return { channel: 'agent', chatID }
  }
  return fallback
}

export function parentSessionForFocusedAgent(tabManager: TabManager | undefined, fallback: SessionSelector | null): SessionSelector | null {
  const activeTab = activeAgentTab(tabManager)
  if (activeTab?.data?.subAgentRole && activeTab.data.parentChatID) {
    return { channel: activeTab.data.parentChannel ?? 'web', chatID: activeTab.data.parentChatID }
  }
  return fallback
}

export function activeAgentTab(tabManager: TabManager | undefined): Tab | null {
  if (!tabManager?.activeTabId) return null
  return tabManager.tabs.find((tab) => tab.id === tabManager.activeTabId && tab.type === 'agent') ?? null
}

export function buildLiveAgentChatID(tab: Tab): string {
  const role = tab.data?.subAgentRole
  const parentChatID = tab.data?.parentChatID
  if (!role || !parentChatID) return ''
  const parentChannel = tab.data?.parentChannel ?? 'web'
  const instance = tab.data?.subAgentInstance ? `:${tab.data.subAgentInstance}` : ''
  return `${parentChannel}:${parentChatID}/${role}${instance}`
}
