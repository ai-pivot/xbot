import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'
import { renderWithProviders } from '@/test-utils'
import i18n from '@/i18n'
import { UserMessage } from './UserMessage'
import { CopyTarget } from './MessageActions'
import { AnnotationChip, MessageAnnotationsProvider } from './MessageAnnotations'
import { annotationStorageKey, formatAnnotatedMessage, loadAnnotations, type MessageAnnotation } from '@/lib/messageAnnotations'
import { clearWebCaches } from '@/lib/webCache'
import { memoryStorage } from '@/test-utils/memoryStorage'
import { frameScheduler } from '@/lib/frameScheduler'
import { I18nProvider } from '@/providers/i18n'

function selectText(element: HTMLElement, from = 0, to = element.textContent!.length) {
  const range = document.createRange()
  range.setStart(element.firstChild!, from)
  range.setEnd(element.firstChild!, to)
  range.getClientRects = () => [new DOMRect(20, 100, 120, 20)] as unknown as DOMRectList
  const selection = document.getSelection()!
  selection.removeAllRanges()
  selection.addRange(range)
  fireEvent(document, new Event('selectionchange'))
  act(() => frameScheduler.flushNow())
}

beforeEach(async () => {
  await i18n.changeLanguage('zh-CN')
  vi.restoreAllMocks()
  vi.stubGlobal('localStorage', memoryStorage())
})
afterEach(() => {
  document.getSelection()?.removeAllRanges()
  frameScheduler.reset()
  vi.unstubAllGlobals()
})

describe('selection comment entry', () => {
  it('waits until the drag ends and dismisses on collapse, scroll or Escape', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:drag" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '正文', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 1 }}>
        <p data-annotation-body="">正文</p>
      </CopyTarget>
    </MessageAnnotationsProvider>)
    const text = screen.getByText('正文')
    fireEvent.pointerDown(text, { button: 0, pointerType: 'mouse' })
    selectText(text)
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
    fireEvent.pointerUp(text, { pointerType: 'mouse' })
    act(() => frameScheduler.flushNow())
    expect(screen.getByTestId('annotation-selection-action')).toBeInTheDocument()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
    selectText(text)
    fireEvent.scroll(document)
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
    selectText(text)
    document.getSelection()!.removeAllRanges()
    fireEvent(document, new Event('selectionchange'))
    act(() => frameScheduler.flushNow())
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
  })

  it('ignores reasoning, tools and cross-body selections', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:eligibility" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '第一段', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 1 }}>
        <p>思考</p><p data-annotation-body="">第一段</p><p>工具</p>
      </CopyTarget>
      <CopyTarget kind="iteration" iteration={{ iteration: 2, content: '第二段', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 2 }}>
        <p data-annotation-body="">第二段</p>
      </CopyTarget>
    </MessageAnnotationsProvider>)
    for (const text of ['思考', '工具']) {
      selectText(screen.getByText(text))
      expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
    }
    selectText(screen.getByText('第一段'))
    const selection = document.getSelection()!
    selection.getRangeAt(0).setEnd(screen.getByText('第二段').firstChild!, 2)
    fireEvent(document, new Event('selectionchange'))
    act(() => frameScheduler.flushNow())
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
  })

  it('shows exactly one action for the owning panel and clears it when the panel is hidden', () => {
    const panels = (visible: boolean) => <>
      <MessageAnnotationsProvider username="tester" sessionKey="web:first" visible={visible}>
        <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '当前会话', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 1 }}>
          <p data-annotation-body="">当前会话</p>
        </CopyTarget>
      </MessageAnnotationsProvider>
      <MessageAnnotationsProvider username="tester" sessionKey="web:second" visible>
        <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '另一会话', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 8, iteration: 1 }}>
          <p data-annotation-body="">另一会话</p>
        </CopyTarget>
      </MessageAnnotationsProvider>
    </>
    const view = renderWithProviders(panels(true))
    selectText(screen.getByText('当前会话'))
    expect(screen.getAllByTestId('annotation-selection-action')).toHaveLength(1)
    view.rerender(<I18nProvider>{panels(false)}</I18nProvider>)
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
    view.rerender(<I18nProvider>{panels(true)}</I18nProvider>)
    expect(screen.queryByTestId('annotation-selection-action')).toBeNull()
    selectText(screen.getByText('另一会话'))
    expect(screen.getAllByTestId('annotation-selection-action')).toHaveLength(1)
    fireEvent.click(screen.getByTestId('annotation-selection-action'))
    expect(screen.getByLabelText('所选文本')).toHaveValue('另一会话')
    expect(screen.getByTestId('annotation-editor')).not.toHaveTextContent('第 8 轮')
  })

  it('shows on selection without right-click and quotes only the selected excerpt', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:selection" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 2, content: '前文 原文 后文', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 2 }}>
        <p data-annotation-body="">前文 原文 后文</p>
      </CopyTarget>
      <AnnotationChip />
    </MessageAnnotationsProvider>)
    selectText(screen.getByText('前文 原文 后文'), 3, 5)
    const action = screen.getByTestId('annotation-selection-action')
    expect(screen.queryByTestId('copy-menu')).toBeNull()
    fireEvent.mouseDown(action)
    expect(document.getSelection()?.toString()).toBe('原文')
    fireEvent.click(action)
    expect(screen.getByLabelText('所选文本')).toHaveValue('原文')
    fireEvent.change(screen.getByLabelText('用户评论'), { target: { value: '推进一下' } })
    fireEvent.click(screen.getByLabelText('确认批注'))
    expect(loadAnnotations(annotationStorageKey('tester', 'web:selection'))[0]).toMatchObject({
      quote: '原文', comment: '推进一下', source: { turnID: 7, iteration: 2, startOffset: 3, endOffset: 5 },
    })
  })

  it('keeps comments out of the right-click menu', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:context" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '正文', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 1 }}>
        <p data-annotation-body="">正文</p>
      </CopyTarget>
    </MessageAnnotationsProvider>)
    fireEvent.contextMenu(screen.getByText('正文'))
    expect(screen.getByTestId('copy-menu')).not.toHaveTextContent('批注这段内容')
    expect(screen.getByTestId('copy-menu')).not.toHaveTextContent('添加到对话')
    expect(screen.getByTestId('copy-menu')).toHaveTextContent('复制该迭代正文')
  })
})

describe('annotation editor', () => {
  it.each([{ ctrlKey: false, metaKey: false }, { ctrlKey: true, metaKey: false }, { ctrlKey: false, metaKey: true }])('confirms with Enter %j and omits source labels and character counts from editor and preview', (modifiers) => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:enter" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 2, content: '正文', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 2 }}><p data-annotation-body="">正文</p></CopyTarget>
      <AnnotationChip />
    </MessageAnnotationsProvider>)
    selectText(screen.getByText('正文'))
    fireEvent.click(screen.getByTestId('annotation-selection-action'))
    const editor = screen.getByTestId('annotation-editor')
    expect(editor).not.toHaveTextContent(/第\s*7\s*轮|第\s*2\s*次迭代|\/6000|\/2000/)
    const comment = screen.getByLabelText('用户评论')
    fireEvent.change(comment, { target: { value: '推进一下' } })
    fireEvent.keyDown(comment, { key: 'Enter', ...modifiers })
    expect(screen.queryByTestId('annotation-editor')).toBeNull()
    expect(loadAnnotations(annotationStorageKey('tester', 'web:enter'))[0]).toMatchObject({
      quote: '正文', comment: '推进一下', source: { turnID: 7, iteration: 2 },
    })
    fireEvent.click(screen.getByTestId('annotation-chip'))
    expect(screen.getByTestId('annotation-list')).not.toHaveTextContent(/第\s*7\s*轮|第\s*2\s*次迭代|\/6000|\/2000/)
  })

  it('keeps Shift+Enter for newlines and does not confirm empty comments or repeated keydown', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:enter-guards" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '正文', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 1 }}><p data-annotation-body="">正文</p></CopyTarget>
      <AnnotationChip />
    </MessageAnnotationsProvider>)
    selectText(screen.getByText('正文'))
    fireEvent.click(screen.getByTestId('annotation-selection-action'))
    const comment = screen.getByLabelText('用户评论')
    fireEvent.keyDown(comment, { key: 'Enter' })
    expect(screen.queryByTestId('annotation-chip')).toBeNull()
    expect(screen.queryByRole('alert')).toBeNull()
    fireEvent.change(comment, { target: { value: '第一行' } })
    const newline = new KeyboardEvent('keydown', { key: 'Enter', shiftKey: true, bubbles: true, cancelable: true })
    fireEvent(comment, newline)
    expect(newline.defaultPrevented).toBe(false)
    fireEvent.keyDown(comment, { key: 'Enter', repeat: true })
    expect(screen.queryByTestId('annotation-chip')).toBeNull()
    expect(comment).toBeInTheDocument()
  })

  it('captures selected body/source, edits without sending, and excludes reasoning/tools', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:a" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 2, content: '正文', reasoning: '思考', toolCount: 0, tools: [] }} annotationSource={{ turnID: 7, iteration: 2 }}>
        <p>思考</p><div data-annotation-body=""><p>  正文 🙂  </p></div>
      </CopyTarget>
      <AnnotationChip />
    </MessageAnnotationsProvider>)
    fireEvent.contextMenu(screen.getByText('思考'))
    expect(screen.queryByText('批注这段内容')).toBeNull()
    fireEvent.keyDown(screen.getByTestId('copy-menu'), { key: 'Escape' })
    selectText(screen.getByText('正文 🙂'))
    fireEvent.click(screen.getByTestId('annotation-selection-action'))
    fireEvent.change(screen.getByLabelText('用户评论'), { target: { value: '我们推进一下' } })
    fireEvent.click(screen.getByLabelText('确认批注'))
    expect(screen.getByTestId('annotation-chip')).toHaveTextContent('1 条批注')
    const saved = loadAnnotations(annotationStorageKey('tester', 'web:a'))
    expect(saved[0].source).toEqual({ turnID: 7, iteration: 2, startOffset: 0, endOffset: 9 })
    expect(saved[0].quote).toBe('  正文 🙂  ')
    expect(saved[0].comment).toBe('我们推进一下')
  })

  it('keeps an oversized quote and editor open without silently changing the quote', () => {
    const text = 'x'.repeat(6001)
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:large" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 1, content: text, reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 8, iteration: 1 }}>
        <p data-annotation-body="">{text}</p>
      </CopyTarget>
    </MessageAnnotationsProvider>)
    selectText(screen.getByText(text))
    fireEvent.click(screen.getByTestId('annotation-selection-action'))
    fireEvent.change(screen.getByLabelText('用户评论'), { target: { value: '评论' } })
    fireEvent.keyDown(screen.getByLabelText('用户评论'), { key: 'Enter' })
    expect(screen.getByRole('alert')).toHaveTextContent('6000')
    expect(screen.getByLabelText('所选文本')).toHaveValue(text)
    expect(screen.getByLabelText('用户评论')).toHaveValue('评论')
  })

  it('does not confirm during IME composition', () => {
    renderWithProviders(<MessageAnnotationsProvider username="tester" sessionKey="web:ime" visible>
      <CopyTarget kind="iteration" iteration={{ iteration: 1, content: '正文', reasoning: '', toolCount: 0, tools: [] }} annotationSource={{ turnID: 8, iteration: 1 }}><p data-annotation-body="">正文</p></CopyTarget>
      <AnnotationChip />
    </MessageAnnotationsProvider>)
    selectText(screen.getByText('正文'))
    fireEvent.click(screen.getByTestId('annotation-selection-action'))
    const comment = screen.getByLabelText('用户评论')
    fireEvent.change(comment, { target: { value: '尚在输入' } })
    fireEvent.keyDown(comment, { key: 'Enter', isComposing: true })
    fireEvent.keyDown(comment, { key: 'Enter', keyCode: 229 })
    expect(screen.queryByTestId('annotation-chip')).toBeNull()
    expect(comment).toBeInTheDocument()
  })
})

describe('annotation snapshot rendering', () => {
  it('renders literal brackets, HTML, links, fences and math instead of interpreting them', () => {
    const quote = '中文 🙂 [链接](https://example.com) <script>x</script> $x$ \\(x\\) ``` !rm -rf /'
    const annotation: MessageAnnotation = { id: 'one', source: { turnID: 7 }, quote, comment: '评论 [保持字面量]' }
    const content = formatAnnotatedMessage('正文', [annotation], (key) => key)
    const { container } = renderWithProviders(<UserMessage content={content} />)
    expect(container.querySelector('pre code')?.textContent).toContain(quote)
    expect(container.textContent).toContain(annotation.comment)
    expect(container.querySelector('script, a, .katex')).toBeNull()
    expect(container.textContent).not.toMatch(/response-annotations|turnId|startOffset|Response annotations|My request/)
  })

  it('preserves TeX after a shorter standalone fence inside the snapshot fence', () => {
    const quote = '前文\n```\n\\(literal\\)\n\\[literal display\\]\n````still literal\n\\(more\\)'
    const annotation: MessageAnnotation = { id: 'nested', source: { turnID: 7 }, quote, comment: '保留原文' }
    const content = formatAnnotatedMessage('', [annotation], (key) => key)
    const { container } = renderWithProviders(<UserMessage content={content} />)
    expect(container.querySelector('pre code')?.textContent).toBe(quote + '\n')
    expect(container.querySelector('.katex')).toBeNull()
  })

  it('clears annotation drafts when authentication caches are reset', () => {
    const key = annotationStorageKey('tester', 'web:logout')
    localStorage.setItem(key, '[]')
    localStorage.setItem('unrelated-setting', 'keep')
    clearWebCaches()
    expect(localStorage.getItem(key)).toBeNull()
    expect(localStorage.getItem('unrelated-setting')).toBe('keep')
  })
})
