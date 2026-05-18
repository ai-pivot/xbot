import { describe, it, expect, vi, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import type { Virtualizer } from '@tanstack/react-virtual'

import SearchPanel from '../components/SearchPanel'
import type { Turn } from '../types'

const mockVirtualizer = {
  scrollToIndex: vi.fn(),
} as unknown as Virtualizer<HTMLDivElement, Element>

const containerRef = { current: document.createElement('div') }

const mockTurns: Turn[] = [
  {
    type: 'user',
    message: { id: 'msg-1', type: 'user', content: 'Hello world this is a test message', ts: Date.now() },
  },
  {
    type: 'assistant',
    messages: [
      { id: 'msg-2', type: 'assistant', content: 'This is an assistant response with test content', ts: Date.now() },
      { id: 'msg-3', type: 'assistant', content: 'Another response without matching text', ts: Date.now() - 2 * 86400000 },
    ],
  },
]

describe('SearchPanel — advanced search', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
  })

  it('renders search input when open=true', () => {
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    expect(screen.getByPlaceholderText('搜索消息历史...')).toBeInTheDocument()
  })

  it('shows matching results for search query', () => {
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    fireEvent.change(screen.getByPlaceholderText('搜索消息历史...'), { target: { value: 'test' } })
    // "test" matches msg-1 and msg-2 → 2 results
    expect(screen.getByText('2 个结果')).toBeInTheDocument()
  })

  it('shows no results message for non-matching query', () => {
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    fireEvent.change(screen.getByPlaceholderText('搜索消息历史...'), { target: { value: 'xyznonexistent' } })
    expect(screen.getByText('未找到匹配结果')).toBeInTheDocument()
  })

  it('filters by role — user only', () => {
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    fireEvent.change(screen.getByPlaceholderText('搜索消息历史...'), { target: { value: 'test' } })
    expect(screen.getByText('2 个结果')).toBeInTheDocument()
    // Click user filter
    fireEvent.click(screen.getByTestId('filter-role-user'))
    expect(screen.getByText('1 个结果')).toBeInTheDocument()
  })

  it('filters by date — today only', () => {
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    // "response" matches msg-2 (today) and msg-3 (2 days ago)
    fireEvent.change(screen.getByPlaceholderText('搜索消息历史...'), { target: { value: 'response' } })
    expect(screen.getByText('2 个结果')).toBeInTheDocument()
    // Click today filter
    fireEvent.click(screen.getByTestId('filter-date-today'))
    expect(screen.getByText('1 个结果')).toBeInTheDocument()
  })

  it('calls onClose and scrollToIndex when result is clicked', () => {
    const onClose = vi.fn()
    render(
      <SearchPanel open={true} onClose={onClose} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    fireEvent.change(screen.getByPlaceholderText('搜索消息历史...'), { target: { value: 'test' } })
    // Click result via its toggle-context button → parent container
    const toggleBtn = screen.getByTestId('toggle-context-msg-1')
    const resultContainer = toggleBtn.closest('.cursor-pointer')!
    fireEvent.click(resultContainer)
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(mockVirtualizer.scrollToIndex).toHaveBeenCalled()
  })

  it('shows search history after previous search', () => {
    localStorage.setItem('xbot-search-history', JSON.stringify(['hello']))
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    // History visible because query is empty and history exists
    expect(screen.getByText('hello')).toBeInTheDocument()
    expect(screen.getByText('搜索记录')).toBeInTheDocument()
  })

  it('clears search history', () => {
    localStorage.setItem('xbot-search-history', JSON.stringify(['hello', 'world']))
    render(
      <SearchPanel open={true} onClose={vi.fn()} messagesContainerRef={containerRef} virtualizer={mockVirtualizer} turns={mockTurns} />
    )
    expect(screen.getByText('hello')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('clear-search-history'))
    expect(screen.queryByText('hello')).not.toBeInTheDocument()
    expect(screen.queryByText('搜索记录')).not.toBeInTheDocument()
  })
})
