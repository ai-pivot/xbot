import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { FilePanel } from './FilePanel'

const { html } = vi.hoisted(() => ({
  html: '<!doctype html><html><body><h1>Preview</h1></body></html>',
}))

vi.mock('@/hooks/useFileContent', () => ({
  useFileContent: () => ({
    content: html,
    loading: false,
    error: null,
    setContent: vi.fn(),
    imageUrl: null,
  }),
}))

vi.mock('@/workspace/types', () => ({
  useDockviewContext: () => ({ ws: {}, cwd: { cwd: '/workspace' } }),
}))

vi.mock('@/components/file/FileToolbar', () => ({ FileToolbar: () => null }))
vi.mock('@/components/file/MonacoEditor', () => ({ MonacoEditor: () => null }))
vi.mock('@/components/file/MarkdownPreview', () => ({ MarkdownPreview: () => null }))
vi.mock('@/components/file/ImagePreview', () => ({ ImagePreview: () => null }))

describe('FilePanel', () => {
  it('renders HTML preview iframe with src pointing to raw API for HTML files', () => {
    render(<FilePanel
      params={{
        tabId: 'file-page',
        type: 'file',
        title: 'page.html',
        filePath: '/workspace/page.html',
        closable: true,
      }}
      api={null as never}
      containerApi={null as never}
    />)

    const frame = screen.getByTitle('page.html')
    // The implementation uses src (not srcdoc) pointing to /api/fs/raw
    expect(frame.hasAttribute('src')).toBe(true)
    expect(frame.getAttribute('src')).toContain('/api/fs/raw')
    expect(frame.getAttribute('src')).toContain(encodeURIComponent('/workspace/page.html'))
  })
})
