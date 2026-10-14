import { beforeAll, describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import i18n from '@/i18n'
import { TerminalList } from './TerminalList'
import type { TerminalManager } from '@/hooks/useTerminal'
import type { TerminalSession } from '@/types/terminal'

// 默认语言在 jsdom 里由 `navigator.language` 探测（= en）⇒ 断言中文串前显式固定语言
// （仓内惯例，见 `PanelLayout.test.tsx:30`）。
beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

vi.mock('@/components/ui/tooltip', () => ({
  Tooltip: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipTrigger: ({ children }: { children: React.ReactNode }) => <>{children}</>,
  TooltipContent: ({ children }: { children: React.ReactNode }) => <>{children}</>,
}))

function manager(terminals: TerminalSession[]): TerminalManager {
  return {
    terminals,
    createTerminal: vi.fn(async () => null),
    closeTerminal: vi.fn(),
    focusTerminal: vi.fn(),
  }
}

function term(id: string, title: string): TerminalSession {
  return {
    id,
    tid: `t-${id}`,
    chatID: 'cli:/repo',
    cwd: '/repo',
    title,
    status: 'connected',
    createdAt: 1,
  }
}

describe('TerminalList header', () => {
  it('does not restate the empty state with a redundant zero count', () => {
    renderWithProviders(<TerminalList terminalManager={manager([])} />)

    // 回归：头部此前**恒**显示 `{{count}} 个终端` ⇒ 空列表时「0 个终端」正压在空态
    // 「暂无终端」上方，同一事实说两遍（观感 = 没做完）。
    expect(screen.getByText('暂无终端')).toBeInTheDocument()
    expect(screen.queryByText('0 个终端')).not.toBeInTheDocument()
  })

  it('still shows the count once there is at least one terminal', () => {
    renderWithProviders(
      <TerminalList terminalManager={manager([term('a', 'zsh'), term('b', 'bash')])} />,
    )

    expect(screen.getByText('2 个终端')).toBeInTheDocument()
    expect(screen.queryByText('暂无终端')).not.toBeInTheDocument()
  })
})
