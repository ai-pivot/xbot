import { describe, it, expect, vi, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, act, waitFor } from '@testing-library/react'
import { useState, useEffect } from 'react'

import MessageReactions from '../components/MessageReactions'
import ThreadPanel from '../components/ThreadPanel'
import NotificationPanel from '../components/NotificationPanel'
import SnapshotShare from '../components/SnapshotShare'
import { NotificationProvider, useNotificationContext } from '../contexts/NotificationContext'
import type { Reaction, Message, ThreadMessage } from '../types'

// ═══════════════════════════════════════════════════════════════════════════════
// 1. MessageReactions
// ═══════════════════════════════════════════════════════════════════════════════

describe('MessageReactions', () => {
  const mockReactions: Reaction[] = [
    { id: 'r1', emoji: '👍', users: ['user1'], byMe: false },
    { id: 'r2', emoji: '❤️', users: ['user1', 'user2'], byMe: true },
  ]

  it('renders existing reactions as chips', () => {
    render(<MessageReactions reactions={mockReactions} onToggle={vi.fn()} />)
    expect(screen.getByTestId('reaction-👍')).toBeInTheDocument()
    expect(screen.getByTestId('reaction-❤️')).toBeInTheDocument()
    // Count badge only for >1 users
    expect(screen.getByTestId('reaction-❤️')).toHaveTextContent('2')
  })

  it('calls onToggle when clicking an existing reaction chip', () => {
    const onToggle = vi.fn()
    render(<MessageReactions reactions={mockReactions} onToggle={onToggle} />)
    fireEvent.click(screen.getByTestId('reaction-👍'))
    expect(onToggle).toHaveBeenCalledWith('👍')
  })

  it('opens picker when add button is clicked', () => {
    render(<MessageReactions reactions={mockReactions} onToggle={vi.fn()} />)
    expect(screen.queryByTestId('reaction-picker')).not.toBeInTheDocument()
    fireEvent.click(screen.getByTestId('reaction-add-btn'))
    expect(screen.getByTestId('reaction-picker')).toBeInTheDocument()
  })

  it('calls onToggle and closes picker when emoji selected from picker', () => {
    const onToggle = vi.fn()
    render(<MessageReactions reactions={mockReactions} onToggle={onToggle} />)
    fireEvent.click(screen.getByTestId('reaction-add-btn'))
    expect(screen.getByTestId('reaction-picker')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('pick-😂'))
    expect(onToggle).toHaveBeenCalledWith('😂')
    expect(screen.queryByTestId('reaction-picker')).not.toBeInTheDocument()
  })

  it('shows add button even with no reactions', () => {
    render(<MessageReactions reactions={[]} onToggle={vi.fn()} />)
    expect(screen.getByTestId('reaction-add-btn')).toBeInTheDocument()
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 2. ThreadPanel
// ═══════════════════════════════════════════════════════════════════════════════

describe('ThreadPanel', () => {
  const mockParent: Message = { id: 'msg-1', type: 'user', content: 'Hello world' }
  const mockThreadMessages: ThreadMessage[] = [
    { id: 'tm-1', parentId: 'msg-1', type: 'assistant', content: 'Hi there', ts: Date.now() },
  ]

  beforeEach(() => {
    Element.prototype.scrollIntoView = vi.fn()
  })

  it('renders nothing when open=false', () => {
    const { container } = render(
      <ThreadPanel open={false} onClose={vi.fn()} parentMessage={mockParent} threadMessages={[]} onSendReply={vi.fn()} />
    )
    expect(container.innerHTML).toBe('')
  })

  it('renders panel and parent message when open=true', () => {
    render(
      <ThreadPanel open={true} onClose={vi.fn()} parentMessage={mockParent} threadMessages={[]} onSendReply={vi.fn()} />
    )
    expect(screen.getByTestId('thread-panel')).toBeInTheDocument()
    expect(screen.getByText('Hello world')).toBeInTheDocument()
  })

  it('calls onSendReply with trimmed input on submit', () => {
    const onSendReply = vi.fn()
    render(
      <ThreadPanel open={true} onClose={vi.fn()} parentMessage={mockParent} threadMessages={[]} onSendReply={onSendReply} />
    )
    fireEvent.change(screen.getByTestId('thread-input'), { target: { value: '  My reply  ' } })
    fireEvent.click(screen.getByTestId('thread-send-btn'))
    expect(onSendReply).toHaveBeenCalledWith('My reply')
  })

  it('calls onClose when close button is clicked', () => {
    const onClose = vi.fn()
    render(
      <ThreadPanel open={true} onClose={onClose} parentMessage={mockParent} threadMessages={[]} onSendReply={vi.fn()} />
    )
    fireEvent.click(screen.getByTestId('thread-close-btn'))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('shows empty state when no thread messages', () => {
    render(
      <ThreadPanel open={true} onClose={vi.fn()} parentMessage={mockParent} threadMessages={[]} onSendReply={vi.fn()} />
    )
    expect(screen.getByText('话题暂无回复')).toBeInTheDocument()
  })

  it('renders existing thread messages', () => {
    render(
      <ThreadPanel open={true} onClose={vi.fn()} parentMessage={mockParent} threadMessages={mockThreadMessages} onSendReply={vi.fn()} />
    )
    expect(screen.getByTestId('thread-msg-tm-1')).toBeInTheDocument()
    expect(screen.getByText('Hi there')).toBeInTheDocument()
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 3. NotificationPanel (needs NotificationProvider wrapper)
// ═══════════════════════════════════════════════════════════════════════════════

function NotificationTestWrapper({ open, onClose, shouldAdd }: {
  open: boolean
  onClose: () => void
  shouldAdd?: boolean
}) {
  const { addNotification } = useNotificationContext()
  const [added, setAdded] = useState(false)
  useEffect(() => {
    if (shouldAdd && !added) {
      addNotification({ type: 'message', title: 'Test Title', body: 'Test body content' })
      setAdded(true)
    }
  }, [shouldAdd, added, addNotification])
  return <NotificationPanel open={open} onClose={onClose} />
}

describe('NotificationPanel', () => {
  it('renders nothing when open=false', () => {
    const { container } = render(
      <NotificationProvider>
        <NotificationPanel open={false} onClose={vi.fn()} />
      </NotificationProvider>
    )
    expect(container.innerHTML).toBe('')
  })

  it('renders notification center title when open=true', () => {
    render(
      <NotificationProvider>
        <NotificationPanel open={true} onClose={vi.fn()} />
      </NotificationProvider>
    )
    expect(screen.getByText('通知中心')).toBeInTheDocument()
  })

  it('shows empty state when no notifications', () => {
    render(
      <NotificationProvider>
        <NotificationPanel open={true} onClose={vi.fn()} />
      </NotificationProvider>
    )
    expect(screen.getByText('暂无通知')).toBeInTheDocument()
  })

  it('marks all as read when markAllRead is clicked', async () => {
    render(
      <NotificationProvider>
        <NotificationTestWrapper open={true} onClose={vi.fn()} shouldAdd={true} />
      </NotificationProvider>
    )
    await waitFor(() => {
      expect(document.querySelector('.notification-unread-dot')).toBeInTheDocument()
    })
    fireEvent.click(screen.getByTestId('mark-all-read-btn'))
    await waitFor(() => {
      expect(document.querySelector('.notification-unread-dot')).not.toBeInTheDocument()
    })
  })

  it('clears all notifications when clear button is clicked', async () => {
    render(
      <NotificationProvider>
        <NotificationTestWrapper open={true} onClose={vi.fn()} shouldAdd={true} />
      </NotificationProvider>
    )
    await waitFor(() => {
      expect(screen.getByText('Test Title')).toBeInTheDocument()
    })
    fireEvent.click(screen.getByTestId('clear-notifications-btn'))
    await waitFor(() => {
      expect(screen.getByText('暂无通知')).toBeInTheDocument()
    })
  })

  it('toggles between All and Unread filters', async () => {
    render(
      <NotificationProvider>
        <NotificationTestWrapper open={true} onClose={vi.fn()} shouldAdd={true} />
      </NotificationProvider>
    )
    await waitFor(() => {
      expect(screen.getByText('Test Title')).toBeInTheDocument()
    })

    // Click unread filter — notification is unread, still visible
    fireEvent.click(screen.getByTestId('filter-unread'))
    expect(screen.getByText('Test Title')).toBeInTheDocument()

    // Click notification to mark as read
    const notifItem = screen.getByText('Test Title').closest('.notification-item')!
    fireEvent.click(notifItem)

    // Switch to all then back to unread — read notification filtered out
    fireEvent.click(screen.getByTestId('filter-all'))
    fireEvent.click(screen.getByTestId('filter-unread'))
    await waitFor(() => {
      expect(screen.getByText('暂无通知')).toBeInTheDocument()
    })
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 4. SnapshotShare
// ═══════════════════════════════════════════════════════════════════════════════

describe('SnapshotShare', () => {
  const mockMessage: Message = { id: 'msg-snap', type: 'user', content: 'Snapshot this message' }

  beforeEach(() => {
    const mockCtx = {
      fillRect: vi.fn(),
      strokeRect: vi.fn(),
      fillText: vi.fn(),
      measureText: vi.fn().mockReturnValue({ width: 50 }),
      font: '',
      fillStyle: '',
      strokeStyle: '',
      lineWidth: 0,
    }
    HTMLCanvasElement.prototype.getContext = vi.fn().mockReturnValue(mockCtx)
    HTMLCanvasElement.prototype.toBlob = vi.fn().mockImplementation(
      (callback: (blob: Blob | null) => void) => {
        callback(new Blob(['test'], { type: 'image/png' }))
      }
    )
    // ClipboardItem must be a real constructor (class) since source uses `new ClipboardItem(...)`
    globalThis.ClipboardItem = class MockClipboardItem {
      data: Record<string, Blob>
      constructor(data: Record<string, Blob>) { this.data = data }
    } as unknown as typeof ClipboardItem
    Object.assign(navigator, {
      clipboard: { write: vi.fn().mockResolvedValue(undefined) },
    })
  })

  it('renders the snapshot button', () => {
    render(<SnapshotShare message={mockMessage} />)
    expect(screen.getByTestId('snapshot-btn')).toBeInTheDocument()
  })

  it('calls clipboard.write when snapshot button is clicked', async () => {
    render(<SnapshotShare message={mockMessage} />)
    await act(async () => {
      fireEvent.click(screen.getByTestId('snapshot-btn'))
    })
    await waitFor(() => {
      expect(navigator.clipboard.write).toHaveBeenCalled()
    })
  })

  it('shows success message after snapshot', async () => {
    render(<SnapshotShare message={mockMessage} />)
    await act(async () => {
      fireEvent.click(screen.getByTestId('snapshot-btn'))
    })
    await waitFor(() => {
      expect(screen.getByTestId('snapshot-success')).toBeInTheDocument()
    })
    expect(screen.getByTestId('snapshot-success')).toHaveTextContent('快照已复制到剪贴板')
  })
})
