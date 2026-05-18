import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import '@testing-library/jest-dom/vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { useCallback } from 'react'
import ErrorBoundary from '../components/ErrorBoundary'
import { ToastProvider, useToast } from '../contexts/ToastContext'

// ─── ErrorBoundary ───

// Suppress React error boundary console.error output
let consoleSpy: ReturnType<typeof vi.spyOn>

beforeEach(() => {
  consoleSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
})

afterEach(() => {
  consoleSpy.mockRestore()
})

// Component that always throws
function ThrowingComponent(): never {
  throw new Error('Test error in render')
}

describe('ErrorBoundary', () => {
  it('renders children when no error', () => {
    render(
      <ErrorBoundary>
        <div data-testid="child">Hello World</div>
      </ErrorBoundary>,
    )
    expect(screen.getByTestId('child')).toBeInTheDocument()
    expect(screen.getByText('Hello World')).toBeInTheDocument()
  })

  it('renders error UI when child throws', () => {
    render(
      <ErrorBoundary>
        <ThrowingComponent />
      </ErrorBoundary>,
    )
    // ErrorBoundary uses getTranslation() which reads localStorage for locale
    // Default zh-CN has errorBoundaryTitle
    expect(screen.getByText('😵')).toBeInTheDocument()
  })

  it('shows retry button', () => {
    render(
      <ErrorBoundary>
        <ThrowingComponent />
      </ErrorBoundary>,
    )
    // Default zh-CN: errorBoundaryRetry key
    const buttons = screen.getAllByRole('button')
    expect(buttons.length).toBeGreaterThanOrEqual(1)
  })

  it('uses custom fallback when provided', () => {
    render(
      <ErrorBoundary fallback={(error, retry) => (
        <div data-testid="custom-fallback">
          <span>{error.message}</span>
          <button onClick={retry}>Custom Retry</button>
        </div>
      )}>
        <ThrowingComponent />
      </ErrorBoundary>,
    )
    expect(screen.getByTestId('custom-fallback')).toBeInTheDocument()
    expect(screen.getByText('Test error in render')).toBeInTheDocument()
    expect(screen.getByText('Custom Retry')).toBeInTheDocument()
  })

  it('retry button resets error state', () => {
    // This test just verifies the retry button exists and is clickable
    render(
      <ErrorBoundary fallback={(_err, retry) => (
        <button data-testid="retry-btn" onClick={retry}>Retry</button>
      )}>
        <ThrowingComponent />
      </ErrorBoundary>,
    )
    const retryBtn = screen.getByTestId('retry-btn')
    expect(retryBtn).toBeInTheDocument()
    fireEvent.click(retryBtn)
    // After clicking retry, the boundary resets — but ThrowingComponent will throw again
    // This is expected behavior; we just verify the click doesn't crash
  })
})

// ─── ToastContext ───

describe('ToastContext', () => {
  it('ToastProvider renders children', () => {
    render(
      <ToastProvider>
        <div data-testid="child">Inside provider</div>
      </ToastProvider>,
    )
    expect(screen.getByTestId('child')).toBeInTheDocument()
  })

  it('showToast adds a toast', () => {
    function TestComponent() {
      const { showToast } = useToast()
      return <button onClick={() => showToast('Hello toast', 'info')}>Show</button>
    }

    render(
      <ToastProvider>
        <TestComponent />
      </ToastProvider>,
    )
    fireEvent.click(screen.getByText('Show'))
    expect(screen.getByText('Hello toast')).toBeInTheDocument()
  })

  it('removeToast removes a toast', () => {
    function TestComponent() {
      const { showToast } = useToast()
      const handleShow = useCallback(() => {
        showToast('Clickable toast', 'info')
      }, [showToast])
      return (
        <div>
          <button onClick={handleShow}>Show</button>
        </div>
      )
    }

    render(
      <ToastProvider>
        <TestComponent />
      </ToastProvider>,
    )
    fireEvent.click(screen.getByText('Show'))
    expect(screen.getByText('Clickable toast')).toBeInTheDocument()

    // Click the toast to remove it
    fireEvent.click(screen.getByText('Clickable toast'))
    expect(screen.queryByText('Clickable toast')).not.toBeInTheDocument()
  })

  it('useToast outside provider throws error', () => {
    // Test that calling useToast outside ToastProvider throws
    function OutsideComponent() {
      try {
        useToast()
      } catch (e) {
        return <div data-testid="caught">{(e as Error).message}</div>
      }
      return null
    }

    render(<OutsideComponent />)
    expect(screen.getByTestId('caught')).toHaveTextContent('useToast must be used within ToastProvider')
  })

  it('shows error toast with ❌ prefix', () => {
    function TestComponent() {
      const { showToast } = useToast()
      return <button onClick={() => showToast('Error occurred', 'error')}>Show Error</button>
    }

    render(
      <ToastProvider>
        <TestComponent />
      </ToastProvider>,
    )
    fireEvent.click(screen.getByText('Show Error'))
    expect(screen.getByText(/❌ Error occurred/)).toBeInTheDocument()
  })

  it('shows success toast with ✅ prefix', () => {
    function TestComponent() {
      const { showToast } = useToast()
      return <button onClick={() => showToast('Done!', 'success')}>Show Success</button>
    }

    render(
      <ToastProvider>
        <TestComponent />
      </ToastProvider>,
    )
    fireEvent.click(screen.getByText('Show Success'))
    expect(screen.getByText(/✅ Done!/)).toBeInTheDocument()
  })
})
