import { act, fireEvent, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom/vitest'

const postAPIMock = vi.hoisted(() => vi.fn())
const connection = vi.hoisted(() => ({
  connected: false,
  listeners: new Set<(connected: boolean) => void>(),
}))
vi.mock('@/lib/api', () => ({ postAPI: postAPIMock }))
vi.mock('@/hooks/useWSConnection', () => ({
  useWSConnection: () => ({
    get connected() { return connection.connected },
    onConnectionChange: (handler: (connected: boolean) => void) => {
      connection.listeners.add(handler)
      return () => connection.listeners.delete(handler)
    },
  }),
}))

import { renderWithProviders } from '@/test-utils'
import { UpdateReminder } from './UpdateReminder'

function setConnected(value: boolean) {
  act(() => {
    connection.connected = value
    connection.listeners.forEach((listener) => listener(value))
  })
}

describe('UpdateReminder', () => {
  beforeEach(() => {
    localStorage.clear()
    postAPIMock.mockReset()
    connection.connected = false
    connection.listeners.clear()
  })

  it('checks after connection, shows both versions, and opens update settings', async () => {
    const openSettings = vi.fn()
    postAPIMock.mockResolvedValue({ current: 'v0.0.57', latest: 'v0.0.58', hasUpdate: true, skipped: false })
    renderWithProviders(<UpdateReminder onOpenSettings={openSettings} />)
    expect(postAPIMock).not.toHaveBeenCalled()

    setConnected(true)
    const notice = await screen.findByRole('button', { name: /v0\.0\.57.*v0\.0\.58/ })
    expect(postAPIMock).toHaveBeenCalledWith('/api/rpc', { method: 'check_update', params: {} }, expect.any(Object))
    fireEvent.click(notice)
    expect(openSettings).toHaveBeenCalledOnce()

    setConnected(false)
    expect(screen.queryByRole('button', { name: /v0\.0\.57.*v0\.0\.58/ })).not.toBeInTheDocument()
  })

  it('dismisses only the latest version shown, including after a reload', async () => {
    postAPIMock.mockResolvedValue({ current: 'v0.0.57', latest: 'v0.0.58', hasUpdate: true, skipped: false })
    const first = renderWithProviders(<UpdateReminder onOpenSettings={vi.fn()} />)
    setConnected(true)
    expect(await screen.findByRole('button', { name: /v0\.0\.57.*v0\.0\.58/ })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /关闭更新提醒|Dismiss update reminder/ }))
    expect(screen.queryByRole('button', { name: /v0\.0\.57.*v0\.0\.58/ })).not.toBeInTheDocument()
    first.unmount()

    const second = renderWithProviders(<UpdateReminder onOpenSettings={vi.fn()} />)
    expect(screen.queryByRole('button', { name: /v0\.0\.57.*v0\.0\.58/ })).not.toBeInTheDocument()
    second.unmount()

    postAPIMock.mockResolvedValue({ current: 'v0.0.57', latest: 'v0.0.59', hasUpdate: true, skipped: false })
    renderWithProviders(<UpdateReminder onOpenSettings={vi.fn()} />)
    expect(await screen.findByRole('button', { name: /v0\.0\.57.*v0\.0\.59/ })).toBeInTheDocument()
  })

  it('does not show a reminder when there is no applicable release', async () => {
    postAPIMock.mockResolvedValue({ current: 'v0.0.57', latest: 'v0.0.57', hasUpdate: false, skipped: false })
    renderWithProviders(<UpdateReminder onOpenSettings={vi.fn()} />)
    setConnected(true)
    await act(async () => { await Promise.resolve() })
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })
})
