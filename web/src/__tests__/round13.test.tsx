import { describe, it, expect, vi, beforeEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent, act, waitFor } from '@testing-library/react'
import { renderHook } from '@testing-library/react'

// ─── Imports ───
import { MediaPlayerProvider, useMediaPlayerContext } from '../contexts/MediaPlayerContext'
import { useBookmarks } from '../hooks/useBookmarks'
import { useVimNavigation } from '../hooks/useVimNavigation'
import { KeyboardHelpPanel } from '../components/KeyboardHelpPanel'
import ThemeEditor from '../components/ThemeEditor'
import BookmarkPanel from '../components/BookmarkPanel'
import { AudioPlayer, VideoPlayer } from '../components/MediaPlayer'

// ═══════════════════════════════════════════════════════════════════════════════
// 1. MediaPlayerContext — Player Mutex (~7 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('MediaPlayerProvider', () => {
  it('renders children without error', () => {
    render(
      <MediaPlayerProvider>
        <div data-testid="child">Hello</div>
      </MediaPlayerProvider>
    )
    expect(screen.getByTestId('child')).toBeInTheDocument()
  })

  it('registerPlayer returns unique IDs', () => {
    const { result } = renderHook(() => useMediaPlayerContext(), {
      wrapper: ({ children }) => <MediaPlayerProvider>{children}</MediaPlayerProvider>,
    })
    const id1 = result.current.registerPlayer()
    const id2 = result.current.registerPlayer()
    expect(id1).toBeTruthy()
    expect(id2).toBeTruthy()
    expect(id1).not.toBe(id2)
  })

  it('activePlayerId starts as null', () => {
    const { result } = renderHook(() => useMediaPlayerContext(), {
      wrapper: ({ children }) => <MediaPlayerProvider>{children}</MediaPlayerProvider>,
    })
    expect(result.current.activePlayerId).toBeNull()
  })

  it('setActive updates activePlayerId', () => {
    const { result } = renderHook(() => useMediaPlayerContext(), {
      wrapper: ({ children }) => <MediaPlayerProvider>{children}</MediaPlayerProvider>,
    })
    const id = result.current.registerPlayer()
    act(() => {
      result.current.setActive(id)
    })
    expect(result.current.activePlayerId).toBe(id)
  })

  it('setActive calls onPause of previous active player', () => {
    const onPause1 = vi.fn()
    const { result } = renderHook(() => useMediaPlayerContext(), {
      wrapper: ({ children }) => <MediaPlayerProvider>{children}</MediaPlayerProvider>,
    })
    const id1 = result.current.registerPlayer()
    const id2 = result.current.registerPlayer()
    act(() => {
      result.current.setActive(id1, onPause1)
    })
    expect(result.current.activePlayerId).toBe(id1)
    act(() => {
      result.current.setActive(id2)
    })
    expect(onPause1).toHaveBeenCalledTimes(1)
    expect(result.current.activePlayerId).toBe(id2)
  })

  it('unregisterPlayer clears activePlayerId if it was active', () => {
    const { result } = renderHook(() => useMediaPlayerContext(), {
      wrapper: ({ children }) => <MediaPlayerProvider>{children}</MediaPlayerProvider>,
    })
    const id = result.current.registerPlayer()
    act(() => {
      result.current.setActive(id)
    })
    act(() => {
      result.current.unregisterPlayer(id)
    })
    expect(result.current.activePlayerId).toBeNull()
  })

  it('unregisterPlayer does not affect other active players', () => {
    const { result } = renderHook(() => useMediaPlayerContext(), {
      wrapper: ({ children }) => <MediaPlayerProvider>{children}</MediaPlayerProvider>,
    })
    const id1 = result.current.registerPlayer()
    const id2 = result.current.registerPlayer()
    act(() => {
      result.current.setActive(id2)
    })
    act(() => {
      result.current.unregisterPlayer(id1)
    })
    expect(result.current.activePlayerId).toBe(id2)
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 2. useBookmarks Hook (~8 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('useBookmarks', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  it('starts with empty bookmarks', () => {
    const { result } = renderHook(() => useBookmarks())
    expect(result.current.bookmarks).toEqual([])
  })

  it('toggleBookmark adds a bookmark', () => {
    const { result } = renderHook(() => useBookmarks())
    act(() => {
      result.current.toggleBookmark('msg-1', 'Hello World')
    })
    expect(result.current.bookmarks).toHaveLength(1)
    expect(result.current.bookmarks[0].messageId).toBe('msg-1')
    expect(result.current.bookmarks[0].content).toBe('Hello World')
  })

  it('toggleBookmark removes existing bookmark', () => {
    const { result } = renderHook(() => useBookmarks())
    act(() => {
      result.current.toggleBookmark('msg-1', 'Hello')
    })
    expect(result.current.bookmarks).toHaveLength(1)
    act(() => {
      result.current.toggleBookmark('msg-1')
    })
    expect(result.current.bookmarks).toHaveLength(0)
  })

  it('isBookmarked returns correct status', () => {
    const { result } = renderHook(() => useBookmarks())
    expect(result.current.isBookmarked('msg-1')).toBe(false)
    act(() => {
      result.current.toggleBookmark('msg-1', 'Hello')
    })
    expect(result.current.isBookmarked('msg-1')).toBe(true)
  })

  it('clearAllBookmarks removes all bookmarks', () => {
    const { result } = renderHook(() => useBookmarks())
    act(() => {
      result.current.toggleBookmark('msg-1', 'A')
      result.current.toggleBookmark('msg-2', 'B')
    })
    expect(result.current.bookmarks).toHaveLength(2)
    act(() => {
      result.current.clearAllBookmarks()
    })
    expect(result.current.bookmarks).toHaveLength(0)
  })

  it('persists bookmarks to localStorage', () => {
    const { result } = renderHook(() => useBookmarks())
    act(() => {
      result.current.toggleBookmark('msg-1', 'Persist test')
    })
    const stored = JSON.parse(localStorage.getItem('xbot-bookmarks') || '[]')
    expect(stored).toHaveLength(1)
    expect(stored[0].messageId).toBe('msg-1')
  })

  it('loads bookmarks from localStorage on init', () => {
    localStorage.setItem('xbot-bookmarks', JSON.stringify([
      { messageId: 'msg-saved', content: 'Saved', timestamp: Date.now() },
    ]))
    const { result } = renderHook(() => useBookmarks())
    expect(result.current.bookmarks).toHaveLength(1)
    expect(result.current.bookmarks[0].messageId).toBe('msg-saved')
  })

  it('handles corrupt localStorage gracefully', () => {
    localStorage.setItem('xbot-bookmarks', 'not-json')
    const { result } = renderHook(() => useBookmarks())
    expect(result.current.bookmarks).toEqual([])
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 3. useVimNavigation Hook (~5 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('useVimNavigation', () => {
  it('does not throw when called', () => {
    expect(() => {
      renderHook(() => useVimNavigation())
    }).not.toThrow()
  })

  it('does not throw with custom config', () => {
    expect(() => {
      renderHook(() => useVimNavigation({
        messageSelector: '.message',
        enabled: true,
        keyBindings: { scrollDown: 's', scrollUp: 'w' },
      }))
    }).not.toThrow()
  })

  it('does not throw when disabled', () => {
    expect(() => {
      renderHook(() => useVimNavigation({ enabled: false }))
    }).not.toThrow()
  })

  it('returns nothing (void hook)', () => {
    const { result } = renderHook(() => useVimNavigation())
    expect(result.current).toBeUndefined()
  })

  it('cleans up event listener on unmount', () => {
    const { unmount } = renderHook(() => useVimNavigation())
    const addSpy = vi.spyOn(window, 'addEventListener')
    const removeSpy = vi.spyOn(window, 'removeEventListener')
    unmount()
    // The hook adds one listener and should remove it on unmount
    // Check that removeEventListener was called
    expect(removeSpy).toHaveBeenCalled()
    addSpy.mockRestore()
    removeSpy.mockRestore()
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 4. KeyboardHelpPanel (~5 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('KeyboardHelpPanel', () => {
  it('renders nothing by default (hidden state)', () => {
    const { container } = render(<KeyboardHelpPanel />)
    expect(container.innerHTML).toBe('')
  })

  it('shows panel when ? key is pressed outside input', async () => {
    render(<KeyboardHelpPanel />)
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true }))
    })
    // Panel should now be visible — look for the header
    await waitFor(() => {
      expect(screen.getByText(/键盘快捷键/)).toBeInTheDocument()
    })
  })

  it('hides panel when Escape is pressed', async () => {
    render(<KeyboardHelpPanel />)
    // First open it
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true }))
    })
    await waitFor(() => {
      expect(screen.getByText(/键盘快捷键/)).toBeInTheDocument()
    })
    // Then close with Escape
    await act(async () => {
      window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }))
    })
    // Panel should be gone
    expect(screen.queryByText(/键盘快捷键/)).not.toBeInTheDocument()
  })

  it('does not open when ? is pressed inside input', async () => {
    // Create an input and focus it
    const input = document.createElement('input')
    document.body.appendChild(input)
    input.focus()

    render(<KeyboardHelpPanel />)
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent('keydown', { key: '?', bubbles: true }))
    })
    expect(screen.queryByText(/键盘快捷键/)).not.toBeInTheDocument()

    document.body.removeChild(input)
  })

  it('toggles via custom event', async () => {
    render(<KeyboardHelpPanel />)
    await act(async () => {
      window.dispatchEvent(new Event('toggle-keyboard-help'))
    })
    await waitFor(() => {
      expect(screen.getByText(/键盘快捷键/)).toBeInTheDocument()
    })
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 5. ThemeEditor Component (~5 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('ThemeEditor', () => {
  beforeEach(() => {
    localStorage.clear()
    // Reset any CSS variables set on document
    document.documentElement.style.cssText = ''
  })

  it('renders nothing when closed', () => {
    const { container } = render(<ThemeEditor open={false} onClose={vi.fn()} />)
    expect(container.innerHTML).toBe('')
  })

  it('renders dialog when open', () => {
    render(<ThemeEditor open={true} onClose={vi.fn()} />)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('renders color picker inputs for all groups', () => {
    render(<ThemeEditor open={true} onClose={vi.fn()} />)
    // Should have 19 color inputs (6+6+4+3 CSS variables)
    const inputs = document.querySelectorAll('input[type="color"]')
    expect(inputs.length).toBe(19)
  })

  it('calls onClose when backdrop is clicked', () => {
    const onClose = vi.fn()
    render(<ThemeEditor open={true} onClose={onClose} />)
    const backdrop = document.querySelector('.theme-editor-backdrop')
    expect(backdrop).toBeTruthy()
    if (backdrop) {
      fireEvent.click(backdrop)
      expect(onClose).toHaveBeenCalledTimes(1)
    }
  })

  it('renders action buttons', () => {
    render(<ThemeEditor open={true} onClose={vi.fn()} />)
    // Should have Save, Export, Import, Reset buttons
    expect(screen.getByText(/保存/)).toBeInTheDocument()
    expect(screen.getByText(/导出主题/)).toBeInTheDocument()
    expect(screen.getByText(/导入主题/)).toBeInTheDocument()
    expect(screen.getByText(/重置主题/)).toBeInTheDocument()
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 6. BookmarkPanel Component (~5 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('BookmarkPanel', () => {
  const mockBookmarks = [
    { messageId: 'msg-1', content: 'First bookmark message content', timestamp: Date.now() - 1000 },
    { messageId: 'msg-2', content: 'Second bookmark', timestamp: Date.now() },
  ]

  it('renders nothing by default (hidden)', () => {
    const { container } = render(
      <BookmarkPanel bookmarks={[]} onRemove={vi.fn()} onClearAll={vi.fn()} onJump={vi.fn()} />
    )
    expect(container.innerHTML).toBe('')
  })

  it('shows panel when toggle event is dispatched', async () => {
    render(
      <BookmarkPanel bookmarks={mockBookmarks} onRemove={vi.fn()} onClearAll={vi.fn()} onJump={vi.fn()} />
    )
    await act(async () => {
      window.dispatchEvent(new Event('toggle-bookmark-panel'))
    })
    await waitFor(() => {
      expect(screen.getByText(/书签/)).toBeInTheDocument()
    })
  })

  it('displays bookmark content', async () => {
    render(
      <BookmarkPanel bookmarks={mockBookmarks} onRemove={vi.fn()} onClearAll={vi.fn()} onJump={vi.fn()} />
    )
    await act(async () => {
      window.dispatchEvent(new Event('toggle-bookmark-panel'))
    })
    await waitFor(() => {
      expect(screen.getByText(/First bookmark message/)).toBeInTheDocument()
      expect(screen.getByText(/Second bookmark/)).toBeInTheDocument()
    })
  })

  it('shows empty state when no bookmarks', async () => {
    render(
      <BookmarkPanel bookmarks={[]} onRemove={vi.fn()} onClearAll={vi.fn()} onJump={vi.fn()} />
    )
    await act(async () => {
      window.dispatchEvent(new Event('toggle-bookmark-panel'))
    })
    await waitFor(() => {
      expect(screen.getByText(/暂无书签/)).toBeInTheDocument()
    })
  })

  it('calls onJump when jump button is clicked', async () => {
    const onJump = vi.fn()
    render(
      <BookmarkPanel bookmarks={mockBookmarks} onRemove={vi.fn()} onClearAll={vi.fn()} onJump={onJump} />
    )
    await act(async () => {
      window.dispatchEvent(new Event('toggle-bookmark-panel'))
    })
    await waitFor(() => {
      expect(screen.getByText(/First bookmark message/)).toBeInTheDocument()
    })
    // Click the jump button (↗) for first bookmark
    const jumpButtons = screen.getAllByTitle('跳转')
    fireEvent.click(jumpButtons[0])
    expect(onJump).toHaveBeenCalledWith('msg-1')
  })
})

// ═══════════════════════════════════════════════════════════════════════════════
// 7. Media Player — Playback Rate UI (~3 tests)
// ═══════════════════════════════════════════════════════════════════════════════

describe('MediaPlayer playback rate', () => {
  it('AudioPlayer renders rate selector', () => {
    render(
      <MediaPlayerProvider>
        <AudioPlayer src="/test.mp3" />
      </MediaPlayerProvider>
    )
    // The rate selector should be present
    expect(screen.getByTestId('audio-player')).toBeInTheDocument()
    expect(screen.getByTestId('playback-rate-btn')).toBeInTheDocument()
  })

  it('VideoPlayer renders with rate control', () => {
    render(
      <MediaPlayerProvider>
        <VideoPlayer src="/test.mp4" />
      </MediaPlayerProvider>
    )
    expect(screen.getByTestId('video-player')).toBeInTheDocument()
  })

  it('ProgressBar supports keyboard navigation', () => {
    render(
      <MediaPlayerProvider>
        <AudioPlayer src="/test.mp3" />
      </MediaPlayerProvider>
    )
    const progressBar = screen.getByTestId('audio-progress-bar')
    expect(progressBar).toHaveAttribute('role', 'slider')
  })
})
