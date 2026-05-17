import { describe, it, expect, vi, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import TabBar from '../components/TabBar'
import ReplyPreview from '../components/ReplyPreview'
import MessageActions from '../components/MessageActions'
import type { ReplyInfo } from '../types'

// ─── TabBar ───

describe('TabBar', () => {
  const mockTabs = [
    { chatId: 'chat-1', label: 'Session 1' },
    { chatId: 'chat-2', label: 'Session 2' },
    { chatId: 'chat-3', label: 'Session 3' },
  ]

  it('renders nothing when tabs array is empty', () => {
    const { container } = render(
      <TabBar tabs={[]} activeTabId="" onTabClick={vi.fn()} onTabClose={vi.fn()} onReorder={vi.fn()} />
    )
    expect(container.innerHTML).toBe('')
  })

  it('renders all tabs with correct labels', () => {
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-1" onTabClick={vi.fn()} onTabClose={vi.fn()} onReorder={vi.fn()} />
    )
    expect(screen.getByText('Session 1')).toBeInTheDocument()
    expect(screen.getByText('Session 2')).toBeInTheDocument()
    expect(screen.getByText('Session 3')).toBeInTheDocument()
  })

  it('highlights the active tab', () => {
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-2" onTabClick={vi.fn()} onTabClose={vi.fn()} onReorder={vi.fn()} />
    )
    const activeTab = screen.getByTestId('tab-item-chat-2')
    expect(activeTab.className).toContain('tab-item-active')
  })

  it('calls onTabClick when tab label is clicked', () => {
    const handleClick = vi.fn()
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-1" onTabClick={handleClick} onTabClose={vi.fn()} onReorder={vi.fn()} />
    )
    fireEvent.click(screen.getByText('Session 2'))
    expect(handleClick).toHaveBeenCalledWith('chat-2')
  })

  it('calls onTabClose when close button is clicked', () => {
    const handleClose = vi.fn()
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-1" onTabClick={vi.fn()} onTabClose={handleClose} onReorder={vi.fn()} />
    )
    // Find close buttons - they have ✕ text
    const closeButtons = screen.getAllByText('✕')
    fireEvent.click(closeButtons[1]) // Close tab-2
    expect(handleClose).toHaveBeenCalledWith('chat-2')
  })

  it('does not call onTabClick when close button is clicked', () => {
    const handleClick = vi.fn()
    const handleClose = vi.fn()
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-1" onTabClick={handleClick} onTabClose={handleClose} onReorder={vi.fn()} />
    )
    const closeButtons = screen.getAllByText('✕')
    fireEvent.click(closeButtons[0])
    expect(handleClick).not.toHaveBeenCalled()
    expect(handleClose).toHaveBeenCalled()
  })

  it('renders tab bar with role="tablist"', () => {
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-1" onTabClick={vi.fn()} onTabClose={vi.fn()} onReorder={vi.fn()} />
    )
    expect(screen.getByRole('tablist')).toBeInTheDocument()
  })

  it('renders each tab with role="tab"', () => {
    render(
      <TabBar tabs={mockTabs} activeTabId="chat-1" onTabClick={vi.fn()} onTabClose={vi.fn()} onReorder={vi.fn()} />
    )
    const tabs = screen.getAllByRole('tab')
    expect(tabs).toHaveLength(3)
  })
})

// ─── ReplyPreview ───

describe('ReplyPreview', () => {
  const mockReply: ReplyInfo = {
    id: 'msg-1',
    content: 'This is a reply message content',
    type: 'user',
  }

  it('renders reply content', () => {
    render(<ReplyPreview replyTo={mockReply} onClick={vi.fn()} />)
    expect(screen.getByText('This is a reply message content')).toBeInTheDocument()
  })

  it('renders user icon for user type', () => {
    render(<ReplyPreview replyTo={mockReply} onClick={vi.fn()} />)
    expect(screen.getByText('👤')).toBeInTheDocument()
  })

  it('renders assistant icon for assistant type', () => {
    const assistantReply: ReplyInfo = { ...mockReply, type: 'assistant' }
    render(<ReplyPreview replyTo={assistantReply} onClick={vi.fn()} />)
    expect(screen.getByText('🤖')).toBeInTheDocument()
  })

  it('truncates content over 80 characters', () => {
    const longReply: ReplyInfo = {
      id: 'msg-2',
      content: 'A'.repeat(100),
      type: 'user',
    }
    render(<ReplyPreview replyTo={longReply} onClick={vi.fn()} />)
    const textEl = screen.getByTestId('reply-preview')
    // Should contain truncated text ending with ...
    expect(textEl.textContent).toContain('...')
    expect(textEl.textContent!.length).toBeLessThan(120)
  })

  it('calls onClick when clicked', () => {
    const handleClick = vi.fn()
    render(<ReplyPreview replyTo={mockReply} onClick={handleClick} />)
    fireEvent.click(screen.getByTestId('reply-preview'))
    expect(handleClick).toHaveBeenCalledOnce()
  })

  it('has data-testid="reply-preview"', () => {
    render(<ReplyPreview replyTo={mockReply} onClick={vi.fn()} />)
    expect(screen.getByTestId('reply-preview')).toBeInTheDocument()
  })
})

// ─── MessageActions ───

describe('MessageActions', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('renders copy button', () => {
    render(<MessageActions onCopy={vi.fn()} copied={false} />)
    expect(screen.getByTestId('copy-btn')).toBeInTheDocument()
  })

  it('shows copied checkmark when copied=true', () => {
    render(<MessageActions onCopy={vi.fn()} copied={true} />)
    expect(screen.getByText('✓')).toBeInTheDocument()
  })

  it('shows clipboard icon when copied=false', () => {
    render(<MessageActions onCopy={vi.fn()} copied={false} />)
    expect(screen.getByText('📋')).toBeInTheDocument()
  })

  it('calls onCopy when copy button clicked', () => {
    const handleCopy = vi.fn()
    render(<MessageActions onCopy={handleCopy} copied={false} />)
    fireEvent.click(screen.getByTestId('copy-btn'))
    expect(handleCopy).toHaveBeenCalledOnce()
  })

  it('shows more actions button when onDelete provided', () => {
    render(<MessageActions onCopy={vi.fn()} copied={false} onDelete={vi.fn()} />)
    expect(screen.getByTestId('more-actions-btn')).toBeInTheDocument()
  })

  it('shows more actions button when onRegenerate provided', () => {
    render(<MessageActions onCopy={vi.fn()} copied={false} onRegenerate={vi.fn()} />)
    expect(screen.getByTestId('more-actions-btn')).toBeInTheDocument()
  })

  it('shows more actions button when onReply provided', () => {
    render(<MessageActions onCopy={vi.fn()} copied={false} onReply={vi.fn()} />)
    expect(screen.getByTestId('more-actions-btn')).toBeInTheDocument()
  })

  it('does not show more actions when no extra handlers', () => {
    render(<MessageActions onCopy={vi.fn()} copied={false} />)
    expect(screen.queryByTestId('more-actions-btn')).not.toBeInTheDocument()
  })

  it('opens dropdown menu on more-actions click', () => {
    render(
      <MessageActions onCopy={vi.fn()} copied={false} onDelete={vi.fn()} />
    )
    fireEvent.click(screen.getByTestId('more-actions-btn'))
    expect(screen.getByTestId('delete-btn')).toBeInTheDocument()
  })

  it('calls onDelete when delete button clicked', () => {
    const handleDelete = vi.fn()
    render(
      <MessageActions onCopy={vi.fn()} copied={false} onDelete={handleDelete} />
    )
    fireEvent.click(screen.getByTestId('more-actions-btn'))
    fireEvent.click(screen.getByTestId('delete-btn'))
    expect(handleDelete).toHaveBeenCalledOnce()
  })

  it('calls onRegenerate when regenerate button clicked', () => {
    const handleRegen = vi.fn()
    render(
      <MessageActions onCopy={vi.fn()} copied={false} onRegenerate={handleRegen} />
    )
    fireEvent.click(screen.getByTestId('more-actions-btn'))
    fireEvent.click(screen.getByTestId('regenerate-btn'))
    expect(handleRegen).toHaveBeenCalledOnce()
  })

  it('calls onReply when reply button clicked', () => {
    const handleReply = vi.fn()
    render(
      <MessageActions onCopy={vi.fn()} copied={false} onReply={handleReply} />
    )
    fireEvent.click(screen.getByTestId('more-actions-btn'))
    fireEvent.click(screen.getByTestId('reply-btn'))
    expect(handleReply).toHaveBeenCalledOnce()
  })

  it('closes menu after action', () => {
    render(
      <MessageActions onCopy={vi.fn()} copied={false} onDelete={vi.fn()} />
    )
    fireEvent.click(screen.getByTestId('more-actions-btn'))
    expect(screen.getByTestId('delete-btn')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('delete-btn'))
    // Menu should be closed - delete button no longer in document
    expect(screen.queryByTestId('delete-btn')).not.toBeInTheDocument()
  })
})
