/**
 * SessionItem / SessionGroup / SessionSidebar —— D-04「跨会话待答提问角标」契约。
 *
 * 走真实链路：store.askUserPrompts（键 = `channel:chatID`，即 sessionKey）
 *   → SessionSidebar 派生 pendingAskIds → SessionList → SessionGroup → SessionItem 角标。
 * 因此把 SessionGroup 的 `pendingAsk={pendingAskIds?.has(...)}` 改成恒 true，
 * 或用恒等 normalize 之外的「猜键」把集合放大，本文件都会红。
 *
 * 覆盖（防什么）：
 *  1. 只有「有提问的那一个会话」亮角标 —— 同时断言角标数量恰好 1、
 *     aria-label 指向 A 的标题、且角标所在行就是 A 那一行（B 行内 0 个）。
 *     这样「恒亮」→ 数量 2 红；「张冠李戴」（A 的集合键命中了 B）→ 行断言红。
 *  2. askUserPrompts 为空 → 角标数量 0（防恒亮；并断言两行都渲染了，避免空树假通过）。
 *  3. SubAgent 行经侧栏链路不亮角标：`SubAgentTreeItem`（SessionGroup.tsx）**没有**把
 *     `pendingAsk` 传给 `SessionItem`（默认 false），所以这条锁的是「当前接线」——
 *     即使 `askUserPrompts` 里有子代理的键也不会亮。
 *     ⚠️ 覆盖边界：它**不**覆盖 `SessionItem` 的 `isSubAgent` / `synthetic` 守卫
 *     （把守卫变异掉，本用例仍然绿）；那两个守卫由第 4 条直渲用例负责。
 *     同时断言 SubAgent 行确实渲染了，否则 0 角标会因行根本不渲染而假通过。
 *
 * 不覆盖（不防什么）：
 *  - 不防 store 侧的生命周期：askUserPrompts 的 hydrate / clear / markAskUserAnswered
 *    何时落键、何时删除（由 ask_user 后端用例与 AskUserFooter / useSessionStore 用例覆盖）。
 *    本文件只断言「map 里已有该键 → 该会话亮角标」的渲染映射。
 *  - 不防 CSS 可见性：jsdom 不算样式，角标被 `display:none` / 0 尺寸盖住也能通过。
 *  - 不防历史脏键：用户裁定 D-04 的恒等 normalize 只能删、不得恢复，
 *    所以这里只用生产形态的键（`<channel>:<chatID>`，chatID 内无冒号）；带前缀的假键不在契约内。
 *  - 不防点击/跳转行为：当前角标无点击语义（仅 role=status + title）。
 */
import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { SessionSidebar } from './SessionSidebar'
import { SessionItem } from './SessionItem'
import type { SessionStore } from '@/hooks/useSessionStore'
import type { SessionInfo } from '@/types/shared'
import type { TabManager } from '@/hooks/useTabManager'

// useSessionStore 的返回值由每个用例自己装配（vi.mock 工厂被提升，故用 hoisted 容器）。
const hoisted = vi.hoisted(() => ({ store: {} as Record<string, unknown> }))

vi.mock('@/hooks/useSessionStore', () => ({
  useSessionStore: () => hoisted.store,
}))

vi.mock('@/components/ui/tooltip', () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))

vi.mock('@/components/ui/scroll-area', () => ({
  ScrollArea: ({ children, className }: { children: React.ReactNode; className?: string }) => (
    <div className={className}>{children}</div>
  ),
}))

const BADGE = 'session-pending-badge'

function session(overrides: Partial<SessionInfo> & { chatID: string; channel: string; label: string }): SessionInfo {
  return {
    chatID: overrides.chatID,
    channel: overrides.channel,
    label: overrides.label,
    lastActive: overrides.lastActive ?? '2026-07-08T00:00:00Z',
    preview: overrides.preview ?? '',
    status: overrides.status ?? 'idle',
    isCurrent: overrides.isCurrent ?? false,
    type: overrides.type,
    role: overrides.role,
    instance: overrides.instance,
    parentChatID: overrides.parentChatID,
    parentChannel: overrides.parentChannel,
    agentChatID: overrides.agentChatID,
    running: overrides.running ?? false,
    historical: overrides.historical,
    synthetic: overrides.synthetic,
    children: overrides.children,
  }
}

/** 两个普通会话 A / B —— 都是「用户可以直接回话」的真实会话行。 */
const A = session({ chatID: 'chat-A', channel: 'web', label: 'Chat A', type: 'main' })
const B = session({ chatID: 'chat-B', channel: 'web', label: 'Chat B', type: 'main' })

function makeStore(sessions: SessionInfo[], askUserPrompts: Map<string, unknown>): SessionStore {
  return {
    sessions,
    groups: [{ key: 'today', sessions }],
    sortedSessions: sessions,
    activeSessionId: null,
    activeSession: null,
    starredIds: [],
    category: 'time',
    unreadIds: [],
    activeChannel: null,
    loading: false,
    error: null,
    subAgents: [],
    askUserPrompts,
    setCategory: vi.fn(),
    collapsedGroups: new Set<string>(),
    toggleGroupCollapsed: vi.fn(),
    setGroupsCollapsed: vi.fn(),
    setActiveChannel: vi.fn(),
    markRead: vi.fn(),
    refresh: vi.fn(),
    toggleStar: vi.fn(),
    createSession: vi.fn().mockResolvedValue('web:chat-new'),
    forkSession: vi.fn(),
    switchSession: vi.fn(),
    activateSession: vi.fn(),
    renameSession: vi.fn(),
    deleteSession: vi.fn(),
    clearAskUserPrompt: vi.fn(),
    markAskUserAnswered: vi.fn(),
    hydrateAskUserPrompt: vi.fn(),
    reorderSessions: vi.fn(),
    setStatus: vi.fn(),
    hasMore: false,
    loadMore: vi.fn(),
  } as unknown as SessionStore
}

const tabManager = {
  tabs: [],
  activeTabId: null,
  openTab: vi.fn(),
  closeTab: vi.fn(),
  setActiveTab: vi.fn(),
  splitRight: vi.fn(),
  resetWorkGroup: vi.fn(),
  bindApi: vi.fn(),
  getLayoutJSON: vi.fn(() => null),
  applyLayoutJSON: vi.fn(),
  getWorkLayoutJSON: vi.fn(() => null),
  groupTabsOf: vi.fn(() => []),
  closeTabsInGroup: vi.fn(),
} satisfies TabManager

/** 角标所在的那一行（SessionItem 的 row 根节点带 tailwind `group` 类）。 */
function rowOf(el: Element): HTMLElement {
  const row = el.closest('div.group')
  if (!row) throw new Error('角标不在任何 SessionItem 行内')
  return row as HTMLElement
}

describe('D-04 pending-ask 角标（askUserPrompts → 侧栏）', () => {
  it('只有有提问的会话 A 亮角标：数量恰好 1，aria-label 与所在行都指向 A', () => {
    hoisted.store = makeStore([A, B], new Map([['web:chat-A', { prompt: 'x' }]])) as unknown as Record<string, unknown>

    renderWithProviders(<SessionSidebar tabManager={tabManager} />)

    // 两行都必须在（否则「只有 1 个角标」会因少渲染一行而假通过）。
    const rowA = screen.getByText('Chat A').closest('div.group') as HTMLElement
    const rowB = screen.getByText('Chat B').closest('div.group') as HTMLElement

    const badges = screen.getAllByTestId(BADGE)
    expect(badges).toHaveLength(1)

    // ① aria-label 指向 A（区分「亮在错的会话上」）。
    const label = badges[0].getAttribute('aria-label') ?? ''
    expect(label).toContain('Chat A')
    expect(label).not.toContain('Chat B')

    // ② 关联元素：角标必须落在 A 那一行，B 行内一个都没有。
    expect(rowOf(badges[0])).toBe(rowA)
    expect(rowB.querySelectorAll(`[data-testid="${BADGE}"]`)).toHaveLength(0)
    expect(rowA.querySelectorAll(`[data-testid="${BADGE}"]`)).toHaveLength(1)
  })

  it('askUserPrompts 为空 → 角标数量 0（两行仍渲染，排除空树假通过）', () => {
    hoisted.store = makeStore([A, B], new Map()) as unknown as Record<string, unknown>

    renderWithProviders(<SessionSidebar tabManager={tabManager} />)

    expect(screen.getByText('Chat A')).toBeInTheDocument()
    expect(screen.getByText('Chat B')).toBeInTheDocument()
    expect(screen.queryAllByTestId(BADGE)).toHaveLength(0)
  })

  it('SubAgent 行不亮角标（即使它的键在 askUserPrompts 里）—— 走侧栏真实链路', () => {
    const sub = session({
      chatID: 'cli:/repo:Agent-main/review:1',
      channel: 'agent',
      label: 'review/1',
      type: 'agent',
      parentChannel: 'cli',
      parentChatID: '/repo:Agent-main',
    })
    const parent = session({
      chatID: '/repo:Agent-main',
      channel: 'cli',
      label: 'Agent-main',
      type: 'main',
      children: [sub],
    })

    hoisted.store = makeStore(
      [parent],
      // 键按 sessionKey 形态给全 —— 角标的隐藏必须来自行类型，而不是键没命中。
      new Map([['agent:cli:/repo:Agent-main/review:1', { prompt: 'x' }]]),
    ) as unknown as Record<string, unknown>

    renderWithProviders(<SessionSidebar tabManager={tabManager} />)

    // 覆盖边界（首辅/门下省 P3 指出的措辞问题，2026-10-29 修正）：
    // SessionGroup 的 SubAgentTreeItem（SessionGroup.tsx:139-185）调用 SessionItem 时
    // **根本没有传 pendingAsk**，所以本用例证明的是「当前侧栏接线不会给子代理行喂角标」，
    // 键命中也不亮；它**不能**证明 SessionItem 的 isSubAgent/synthetic 守卫存在
    // （变异掉守卫后本用例仍绿）。真正的守卫由下一个直渲用例覆盖。
    // 这里仍然断言子代理行确实渲染了 —— 否则 0 角标可能只是「行根本没画出来」。
    expect(screen.getByText('review/1')).toBeInTheDocument()
    expect(screen.queryAllByTestId(BADGE)).toHaveLength(0)
  })

  it('SessionItem：synthetic / isSubAgent 即使 pendingAsk=true 也不渲染，普通行同 prop 必亮（对照组）', () => {
    // 说明：synthetic 会话在 SessionList 层就被过滤掉（SessionList.tsx:234 只保留
    // 非 synthetic 主会话），侧栏链路到不了它，所以这里直接钉 SessionItem 自身的守卫
    // —— 防御历史/其它宿主（面板、tab、搜索）把 synthetic 行喂进来。
    const plain = session({ chatID: 'chat-A', channel: 'web', label: 'Plain', type: 'main' })
    const synthetic = session({ chatID: 'chat-summary', channel: 'web', label: 'Summary', synthetic: true })

    const props = {
      starred: false,
      unread: false,
      active: false,
      pendingAsk: true,
      onSelect: vi.fn(),
      onToggleStar: vi.fn(),
      onRename: vi.fn(),
      onDelete: vi.fn(),
    }

    // 对照：同 prop 下普通行必须亮，证明下面的 0 不是「角标根本没实现」。
    const control = renderWithProviders(<SessionItem {...props} session={plain} />)
    expect(control.queryAllByTestId(BADGE)).toHaveLength(1)
    control.unmount()

    const syn = renderWithProviders(<SessionItem {...props} session={synthetic} />)
    expect(syn.container.textContent).toContain('Summary')
    expect(syn.queryAllByTestId(BADGE)).toHaveLength(0)
    syn.unmount()

    const subRender = renderWithProviders(<SessionItem {...props} session={plain} isSubAgent />)
    expect(subRender.container.textContent).toContain('Plain')
    expect(subRender.queryAllByTestId(BADGE)).toHaveLength(0)
  })
})
