/**
 * SettingsTools（设置 → 工具）单测。
 *
 * 契约（AGENTS.md「工具只在正确时机注入」）：
 *   - 列表来自 `get_tools_settings`（名称/说明/激活态）；
 *   - MCP 工具按**后端给的真实 server 名**分组（`server_name`，不靠名字前缀猜），
 *     服务器级开关 = 批量 `set_tool_enabled`；
 *   - 开关是乐观更新 + 失败回滚 + 可见错误（不能静默）。
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const postAPIMock = vi.hoisted(() => vi.fn())
vi.mock('@/lib/api', () => ({ postAPI: postAPIMock }))
vi.mock('@/providers/i18n', () => ({
  useI18n: () => ({ t: (k: string, o?: Record<string, unknown>) => (o ? `${k}:${JSON.stringify(o)}` : k) }),
}))

import { SettingsTools } from './SettingsTools'

const TOOLS = [
  { name: 'Shell', description: 'run commands', enabled: true },
  { name: 'WebSearch', description: '', enabled: false },
  { name: 'mcp_linear_list_issues', description: '', enabled: true, server_name: 'linear' },
]

describe('SettingsTools（设置 → 工具）', () => {
  beforeEach(() => {
    postAPIMock.mockReset()
  })

  it('列出内置工具与汇总，并把 MCP 工具按真实 server 名分组', async () => {
    postAPIMock.mockResolvedValue({ tools: TOOLS })
    render(<SettingsTools />)

    expect(await screen.findByTestId('tool-row-Shell')).toBeTruthy()
    expect(screen.getByTestId('tool-row-WebSearch')).toBeTruthy()
    // 汇总（激活数/总数）走 i18n 插值
    expect(screen.getByTestId('tools-summary').textContent).toContain('"active":2')
    // MCP：分组区 + server 行用的是后端给的 server 名（linear），不是从工具名拆出来的
    expect(screen.getByTestId('mcp-section')).toBeTruthy()
    expect(screen.getByTestId('mcp-server-linear')).toBeTruthy()
    expect(screen.getByTestId('tool-row-mcp_linear_list_issues')).toBeTruthy()
  })

  it('切换工具开关 → set_tool_enabled(name, enabled) 且乐观生效', async () => {
    postAPIMock.mockResolvedValue({ tools: TOOLS })
    render(<SettingsTools />)

    const shell = await screen.findByRole('switch', { name: 'Shell' })
    expect(shell.getAttribute('aria-checked')).toBe('true')

    fireEvent.click(shell)

    await waitFor(() =>
      expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
        method: 'set_tool_enabled',
        params: { name: 'Shell', enabled: false },
      }),
    )
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Shell' }).getAttribute('aria-checked')).toBe('false'))
  })

  it('RPC 失败 → 回滚开关并显示可见错误（不静默）', async () => {
    postAPIMock.mockResolvedValue({ tools: TOOLS })
    render(<SettingsTools />)

    const shell = await screen.findByRole('switch', { name: 'Shell' })
    postAPIMock.mockImplementation(async (_path: string, body: { method: string }) => {
      if (body.method === 'set_tool_enabled') throw new Error('boom')
      return { tools: TOOLS }
    })

    fireEvent.click(shell)

    await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('boom'))
    expect(screen.getByRole('switch', { name: 'Shell' }).getAttribute('aria-checked')).toBe('true')
  })

  it('MCP 服务器级开关批量启停该 server 的全部工具', async () => {
    postAPIMock.mockResolvedValue({ tools: TOOLS })
    render(<SettingsTools />)

    const serverSwitch = await screen.findByRole('switch', { name: 'linear' })
    expect(serverSwitch.getAttribute('aria-checked')).toBe('true') // 该 server 的工具全激活

    fireEvent.click(serverSwitch)

    await waitFor(() =>
      expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
        method: 'set_tool_enabled',
        params: { name: 'mcp_linear_list_issues', enabled: false },
      }),
    )
  })
})
