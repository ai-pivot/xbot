import { beforeAll, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import i18n from '@/i18n'
import { renderWithProviders } from '@/test-utils'
import type { ChatMessage } from '@/types/shared'
import { CopyTarget, resolveLinkTarget, resolveOpenableHref } from './MessageActions'

// 菜单标签走 i18n ⇒ 断言语言两侧钉死（jsdom 的 navigator.language 是 en-US）。
beforeAll(async () => {
  await i18n.changeLanguage('zh-CN')
})

const message: ChatMessage = {
  id: 'm1',
  role: 'assistant',
  content: '回复正文',
  timestamp: '',
  iterations: [],
  isPartial: false,
  turnID: 1,
}

function appendLink(href: string, text: string): HTMLAnchorElement {
  const a = document.createElement('a')
  a.setAttribute('href', href)
  a.textContent = text
  document.body.appendChild(a)
  return a
}

describe('resolveOpenableHref（打开链接的协议白名单）', () => {
  it('放行 http / https / mailto，相对链接按 base 解析成绝对地址', () => {
    expect(resolveOpenableHref('https://example.com/a')).toBe('https://example.com/a')
    expect(resolveOpenableHref('http://example.com/a')).toBe('http://example.com/a')
    expect(resolveOpenableHref('mailto:a@b.c')).toBe('mailto:a@b.c')
    expect(resolveOpenableHref('/api/files/download?key=x', 'https://xbot.local/')).toBe(
      'https://xbot.local/api/files/download?key=x',
    )
  })

  it('拒绝 javascript: / data: / file: 与空串', () => {
    expect(resolveOpenableHref('javascript:alert(1)')).toBeNull()
    expect(resolveOpenableHref('data:text/html;base64,PHNjcmlwdD4=')).toBeNull()
    expect(resolveOpenableHref('file:///etc/passwd')).toBeNull()
    expect(resolveOpenableHref('')).toBeNull()
    // 相对路径按 base 解析（消息里有 /api/files/download 这类相对链接，要能打开）
    expect(resolveOpenableHref('%%%not a url', 'https://xbot.local/')).toBe('https://xbot.local/%%%not%20a%20url')
  })
})

describe('resolveLinkTarget（落点是否命中链接）', () => {
  it('命中 <a href> 的子元素时返回绝对地址 + 链接文本', () => {
    const a = appendLink('https://example.com/news', '今日新闻')
    const inner = document.createElement('span')
    a.appendChild(inner)
    expect(resolveLinkTarget(inner)).toEqual({ href: 'https://example.com/news', text: '今日新闻' })
  })

  it('非链接 / 白名单外协议 / null 落点 → null', () => {
    const p = document.createElement('p')
    document.body.appendChild(p)
    expect(resolveLinkTarget(p)).toBeNull()
    expect(resolveLinkTarget(appendLink('javascript:alert(1)', 'x'))).toBeNull()
    expect(resolveLinkTarget(null)).toBeNull()
  })
})

describe('CopyTarget 菜单：打开链接 / 复制选区', () => {
  it('右键落在链接上 → 菜单含「打开链接 / 复制链接地址」，打开走 window.open(noopener,noreferrer)', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    const openSpy = vi.spyOn(window, 'open').mockImplementation(() => null)

    renderWithProviders(
      <CopyTarget kind="message" message={message}>
        <p>
          参考 <a href="https://example.com/bench">新的基准评测</a>
        </p>
      </CopyTarget>,
    )
    fireEvent.contextMenu(screen.getByText('新的基准评测'), { clientX: 10, clientY: 20 })
    const menu = screen.getByTestId('copy-menu')
    expect(menu).toHaveTextContent('打开链接')
    expect(menu).toHaveTextContent('复制链接地址')

    fireEvent.click(screen.getByText('打开链接'))
    expect(openSpy).toHaveBeenCalledWith('https://example.com/bench', '_blank', 'noopener,noreferrer')
    // 点完必须关菜单（否则菜单滞留屏幕上，要再点一次遮罩才关；CR 实测这条此前零断言）
    await vi.waitFor(() => expect(screen.queryByTestId('copy-menu')).toBeNull())

    // 复制链接地址 → 绝对地址进剪贴板
    fireEvent.contextMenu(screen.getByText('新的基准评测'), { clientX: 10, clientY: 20 })
    fireEvent.click(screen.getByText('复制链接地址'))
    expect(writeText).toHaveBeenCalledWith('https://example.com/bench')
    await vi.waitFor(() => expect(screen.queryByTestId('copy-menu')).toBeNull())
    openSpy.mockRestore()
  })

  it('打开菜单时已有选区 → 菜单含「复制选区」，点它把选区文字写入剪贴板（取 trim 后内容）', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.assign(navigator, { clipboard: { writeText } })
    vi.spyOn(window, 'getSelection').mockReturnValue({ toString: () => '  选中的一段文字  ' } as unknown as Selection)

    renderWithProviders(
      <CopyTarget kind="message" message={message}>
        <p>正文段落</p>
      </CopyTarget>,
    )
    fireEvent.contextMenu(screen.getByText('正文段落'), { clientX: 5, clientY: 5 })
    const menu = screen.getByTestId('copy-menu')
    expect(menu).toHaveTextContent('复制选区')

    fireEvent.click(screen.getByText('复制选区'))
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith('选中的一段文字'))
  })

  it('无选区且非链接 → 不出现这两组项（只有三层复制项）', () => {
    vi.spyOn(window, 'getSelection').mockReturnValue({ toString: () => '' } as unknown as Selection)

    renderWithProviders(
      <CopyTarget kind="message" message={message}>
        <p>纯正文</p>
      </CopyTarget>,
    )
    fireEvent.contextMenu(screen.getByText('纯正文'), { clientX: 5, clientY: 5 })
    const menu = screen.getByTestId('copy-menu')
    expect(menu).not.toHaveTextContent('打开链接')
    expect(menu).not.toHaveTextContent('复制选区')
    expect(menu).toHaveTextContent('复制回复')
  })
})
