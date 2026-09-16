/**
 * SessionItem waiting_input contract (F3): waiting_input (an AskUser prompt is
 * pending — the turn is PAUSED) and running are mutually exclusive. A stale
 * `running` flag on the row must never light the sidebar spinner while the
 * session waits for user input; the row must show the waiting dot instead.
 */
import { describe, expect, it, vi } from 'vitest'
import '@testing-library/jest-dom'

import { renderWithProviders } from '@/test-utils'
import { SessionItem } from './SessionItem'
import type { SessionInfo } from '@/types/shared'

function session(overrides: Partial<SessionInfo>): SessionInfo {
  return {
    chatID: overrides.chatID ?? 'web-chat-1',
    channel: overrides.channel ?? 'web',
    label: overrides.label ?? 'My Chat',
    lastActive: overrides.lastActive ?? '2026-07-08T00:00:00Z',
    preview: overrides.preview ?? '',
    status: overrides.status ?? 'idle',
    isCurrent: overrides.isCurrent ?? false,
    type: overrides.type,
    running: overrides.running ?? false,
    children: overrides.children,
  }
}

const baseProps = {
  starred: false,
  unread: false,
  active: false,
  onSelect: vi.fn(),
  onToggleStar: vi.fn(),
  onRename: vi.fn(),
  onDelete: vi.fn(),
}

describe('SessionItem waiting_input', () => {
  it('shows the waiting dot — NOT the running spinner — even when a stale running flag is set', () => {
    // The backend row historically carried running=true while the turn was
    // paused on an AskUser. Even if such a row reaches the component directly
    // (cache / older payload), waiting_input must win over the spinner.
    const { container } = renderWithProviders(
      <SessionItem {...baseProps} session={session({ status: 'waiting_input', running: true })} />,
    )
    expect(container.querySelector('.animate-spin')).toBeNull()
    // Sanity: the waiting dot itself is rendered (not an empty row).
    expect(container.querySelector('[style*="--status-waiting"]')).not.toBeNull()
  })

  it('control: a genuinely running session still renders the spinner', () => {
    // Mutation discrimination — without this the first assertion could pass
    // vacuously (e.g. if the spinner never rendered at all).
    const { container } = renderWithProviders(
      <SessionItem {...baseProps} session={session({ status: 'running', running: true })} />,
    )
    expect(container.querySelector('.animate-spin')).not.toBeNull()
  })
})
