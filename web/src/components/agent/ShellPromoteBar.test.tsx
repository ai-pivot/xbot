/**
 * ShellPromoteBar interaction tests — the promote-to-background flow on the
 * running Shell tool card.
 *
 * Covers: render gating (running + session identity + call id), the
 * promote_shell RPC call shape, success/failure UI transitions, and that
 * completed tools never show the bar.
 */
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { screen, fireEvent, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { ToolRender } from '@/components/agent/ToolRender'
import { ToolSessionContext } from '@/components/agent/ToolSessionContext'
import { postAPI } from '@/lib/api'
import type { WebToolProgress } from '@/types/shared'

vi.mock('@/lib/api', () => ({
  postAPI: vi.fn(),
}))

const postAPIMock = vi.mocked(postAPI)

import { toast } from 'sonner'
vi.mock('sonner', () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}))
const toastMock = vi.mocked(toast)

function makeTool(overrides: Partial<WebToolProgress> = {}): WebToolProgress {
  return {
    name: 'Shell',
    label: 'Shell: npm run build',
    status: 'running',
    elapsedMs: 4200,
    summary: '',
    detail: '',
    args: '',
    toolHints: '',
    ...overrides,
  }
}

function renderRunningShell(tool: WebToolProgress, session: { channel: string; chatID: string | null }) {
  return renderWithProviders(
    <ToolSessionContext.Provider value={session}>
      <ToolRender tool={tool} />
    </ToolSessionContext.Provider>,
  )
}

describe('ShellPromoteBar', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    postAPIMock.mockResolvedValue({ ok: true, task_id: 'ab12cd34' } as never)
  })

  it('shows the promote button on a RUNNING shell with session + call id', () => {
    renderRunningShell(
      makeTool({ callID: 'call_1', args: '{"command":"npm run build"}' }),
      { channel: 'web', chatID: 'chat-1' },
    )
    expect(screen.getByRole('button', { name: /转后台|把这条命令转入后台/ })).toBeInTheDocument()
  })

  it('hides the promote button for completed tools (history render)', () => {
    renderRunningShell(
      makeTool({ status: 'done', callID: 'call_1', summary: 'build ok' }),
      { channel: 'web', chatID: 'chat-1' },
    )
    expect(screen.queryByRole('button', { name: /转后台|把这条命令转入后台/ })).not.toBeInTheDocument()
  })

  it('hides the promote button when there is no session identity (chatID null)', () => {
    renderRunningShell(
      makeTool({ callID: 'call_1' }),
      { channel: 'web', chatID: null },
    )
    const btn = screen.queryByRole('button', { name: /转后台|把这条命令转入后台/ })
    // The bar renders but the button is disabled (no RPC possible).
    expect(btn).toBeDisabled()
  })

  it('hides the promote button when the running tool has no call id (legacy event)', () => {
    renderRunningShell(
      makeTool({ callID: undefined }),
      { channel: 'web', chatID: 'chat-1' },
    )
    const btn = screen.queryByRole('button', { name: /转后台|把这条命令转入后台/ })
    expect(btn).toBeDisabled()
  })

  it('calls promote_shell with session_key + tool_call_id and shows the done bar on success', async () => {
    const dispatchSpy = vi.spyOn(window, 'dispatchEvent')
    renderRunningShell(
      makeTool({ callID: 'call_9', args: '{"command":"npm run build"}' }),
      { channel: 'web', chatID: 'chat-1' },
    )
    const btn = screen.getByRole('button', { name: /把这条命令转入后台执行/ })
    fireEvent.click(btn)

    await waitFor(() => {
      expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', {
        method: 'promote_shell',
        params: { session_key: 'web:chat-1', tool_call_id: 'call_9' },
      })
    })

    // Success → done bar with the task id + toast.
    await waitFor(() => {
      expect(screen.getByText('已在后台运行')).toBeInTheDocument()
    })
    expect(screen.getByText('ab12cd34')).toBeInTheDocument()
    expect(toastMock.success).toHaveBeenCalled()
    // Task panels refresh immediately via the 'bg-task-promoted' window event
    // (same pattern as 'bg-task-output').
    expect(dispatchSpy).toHaveBeenCalledWith(
      expect.objectContaining({ type: 'bg-task-promoted' }),
    )
    dispatchSpy.mockRestore()
  })

  it('shows an error toast and stays retryable when the RPC fails', async () => {
    postAPIMock.mockRejectedValueOnce(new Error('no running foreground shell in this session'))
    renderRunningShell(
      makeTool({ callID: 'call_x', args: '{"command":"sleep 100"}' }),
      { channel: 'web', chatID: 'chat-2' },
    )
    const btn = screen.getByRole('button', { name: /把这条命令转入后台执行/ })
    fireEvent.click(btn)

    await waitFor(() => {
      expect(toastMock.error).toHaveBeenCalledWith(
        '转入后台失败',
        expect.objectContaining({ description: 'no running foreground shell in this session' }),
      )
    })
    // Back to the retryable button (not the done bar).
    expect(screen.getByRole('button', { name: /把这条命令转入后台执行/ })).toBeInTheDocument()
    expect(screen.queryByText('已在后台运行')).not.toBeInTheDocument()
  })

  it('renders the promoted result badges for a finished shell (PROMOTED + task id)', () => {
    renderRunningShell(
      makeTool({
        status: 'done',
        summary: '[PROMOTED to background by user] Command moved to the background [task_id: "9adfa651"]\nPartial output so far:\nok\n\n- task_wait',
      }),
      { channel: 'web', chatID: 'chat-1' },
    )
    expect(screen.getByText('已转后台')).toBeInTheDocument()
    expect(screen.getByText('9adfa651')).toBeInTheDocument()
  })
})
