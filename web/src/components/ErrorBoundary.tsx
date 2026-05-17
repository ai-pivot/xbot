import { Component, type ErrorInfo, type ReactNode } from 'react'

interface ErrorBoundaryProps {
  children: ReactNode
  /** Optional fallback UI; receives the error and a retry callback */
  fallback?: (error: Error, retry: () => void) => ReactNode
}

interface ErrorBoundaryState {
  hasError: boolean
  error: Error | null
}

/**
 * React Error Boundary — catches render errors in child components
 * and displays a friendly recovery UI instead of a blank screen.
 */
export default class ErrorBoundary extends Component<ErrorBoundaryProps, ErrorBoundaryState> {
  constructor(props: ErrorBoundaryProps) {
    super(props)
    this.state = { hasError: false, error: null }
  }

  static getDerivedStateFromError(error: Error): ErrorBoundaryState {
    return { hasError: true, error }
  }

  componentDidCatch(error: Error, errorInfo: ErrorInfo): void {
    console.error('[ErrorBoundary] caught render error:', error, errorInfo)
  }

  private handleRetry = () => {
    this.setState({ hasError: false, error: null })
  }

  render(): ReactNode {
    if (this.state.hasError && this.state.error) {
      if (this.props.fallback) {
        return this.props.fallback(this.state.error, this.handleRetry)
      }

      return (
        <div className="flex flex-col items-center justify-center min-h-screen gap-4 p-8"
             style={{ background: 'var(--xbot-bg-primary)', color: 'var(--xbot-text-primary)' }}>
          <div className="text-4xl">😵</div>
          <h2 className="text-lg font-semibold">页面出错了</h2>
          <p className="text-sm max-w-md text-center" style={{ color: 'var(--xbot-text-secondary)' }}>
            发生了意外错误，请尝试刷新页面。
          </p>
          <details className="text-xs max-w-lg w-full" style={{ color: 'var(--xbot-text-muted)' }}>
            <summary className="cursor-pointer mb-2">错误详情</summary>
            <pre className="p-3 rounded-lg overflow-auto text-xs" style={{ background: 'var(--xbot-bg-secondary)', maxHeight: '200px' }}>
              {this.state.error.message}
              {this.state.error.stack && `\n\n${this.state.error.stack}`}
            </pre>
          </details>
          <div className="flex gap-3">
            <button
              onClick={this.handleRetry}
              className="px-4 py-2 rounded-lg text-sm font-medium transition-colors"
              style={{ background: 'var(--xbot-accent-blue)', color: '#fff' }}
            >
              重试
            </button>
            <button
              onClick={() => window.location.reload()}
              className="px-4 py-2 rounded-lg text-sm font-medium transition-colors"
              style={{ background: 'var(--xbot-bg-elevated)', color: 'var(--xbot-text-primary)' }}
            >
              刷新页面
            </button>
          </div>
        </div>
      )
    }

    return this.props.children
  }
}
