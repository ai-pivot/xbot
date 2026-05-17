import { describe, it, expect } from 'vitest'
import { groupMessagesIntoTurns, parseAttachments } from '../ChatPage'
import type { Message } from '../types'

// ─── groupMessagesIntoTurns ───

describe('groupMessagesIntoTurns', () => {
  const userMsg = (id: string, content: string): Message => ({
    id, type: 'user', content, ts: 1000,
  })
  const assistantMsg = (id: string, content: string): Message => ({
    id, type: 'assistant', content, ts: 1001,
  })

  it('groups alternating user/assistant messages', () => {
    const messages = [
      userMsg('u1', 'Hello'),
      assistantMsg('a1', 'Hi there'),
      userMsg('u2', 'How are you?'),
      assistantMsg('a2', 'Fine, thanks'),
    ]
    const turns = groupMessagesIntoTurns(messages)
    expect(turns).toHaveLength(4)
    expect(turns[0]).toEqual({ type: 'user', message: messages[0] })
    expect(turns[1]).toEqual({ type: 'assistant', messages: [messages[1]] })
    expect(turns[2]).toEqual({ type: 'user', message: messages[2] })
    expect(turns[3]).toEqual({ type: 'assistant', messages: [messages[3]] })
  })

  it('merges consecutive assistant messages', () => {
    const messages = [
      userMsg('u1', 'Hello'),
      assistantMsg('a1', 'Part 1'),
      assistantMsg('a2', 'Part 2'),
    ]
    const turns = groupMessagesIntoTurns(messages)
    expect(turns).toHaveLength(2)
    expect(turns[0].type).toBe('user')
    expect(turns[1]).toEqual({ type: 'assistant', messages: [messages[1], messages[2]] })
  })

  it('returns empty for empty input', () => {
    expect(groupMessagesIntoTurns([])).toEqual([])
  })

  it('handles starting with assistant message', () => {
    const messages = [
      assistantMsg('a1', 'First'),
      userMsg('u1', 'Hello'),
    ]
    const turns = groupMessagesIntoTurns(messages)
    expect(turns).toHaveLength(2)
    expect(turns[0]).toEqual({ type: 'assistant', messages: [messages[0]] })
    expect(turns[1]).toEqual({ type: 'user', message: messages[1] })
  })

  it('handles ending with user message (no trailing assistant)', () => {
    const messages = [
      assistantMsg('a1', 'Response'),
      userMsg('u1', 'Last message'),
    ]
    const turns = groupMessagesIntoTurns(messages)
    expect(turns).toHaveLength(2)
    expect(turns[1].type).toBe('user')
  })
})

// ─── parseAttachments ───

describe('parseAttachments', () => {
  it('parses image tag', () => {
    const content = '<image name="photo.jpg" url="https://example.com/photo.jpg"/>'
    const { attachments, cleanContent } = parseAttachments(content)
    expect(attachments).toHaveLength(1)
    expect(attachments[0].type).toBe('image')
    expect(attachments[0].name).toBe('photo.jpg')
    expect(attachments[0].url).toBe('https://example.com/photo.jpg')
    expect(cleanContent).toContain('{{attachment-0}}')
  })

  it('parses file tag with size', () => {
    const content = '<file name="report.pdf" url="https://example.com/report.pdf" size="2048576"/>'
    const { attachments } = parseAttachments(content)
    expect(attachments).toHaveLength(1)
    expect(attachments[0].type).toBe('file')
    expect(attachments[0].name).toBe('report.pdf')
    expect(attachments[0].size).toBe(2048576)
  })

  it('handles no attachments', () => {
    const content = 'Just plain text without any tags'
    const { attachments, cleanContent } = parseAttachments(content)
    expect(attachments).toHaveLength(0)
    expect(cleanContent).toBe('Just plain text without any tags')
  })

  it('removes markdown image syntax after xml tag', () => {
    const content = '<image name="photo.jpg" url="https://example.com/photo.jpg"/>\n![photo.jpg](https://example.com/photo.jpg)'
    const { attachments } = parseAttachments(content)
    expect(attachments).toHaveLength(1)
    // The markdown duplicate should be stripped; only one attachment parsed
  })

  it('handles non-http URLs — keeps tag in content', () => {
    const content = '<image name="xss.png" url="javascript:alert(1)"/>'
    const { attachments, cleanContent } = parseAttachments(content)
    // Non-http URLs should not be parsed as attachments
    expect(attachments).toHaveLength(0)
    expect(cleanContent).toContain('javascript:alert(1)')
  })

  it('uses default name when name attribute is missing', () => {
    const content = '<image url="https://example.com/photo.jpg"/>'
    const { attachments } = parseAttachments(content)
    expect(attachments).toHaveLength(1)
    expect(attachments[0].name).toBe('图片') // default for image type
  })

  it('handles filename attribute as alias for name', () => {
    const content = '<file filename="doc.pdf" url="https://example.com/doc.pdf"/>'
    const { attachments } = parseAttachments(content)
    expect(attachments).toHaveLength(1)
    expect(attachments[0].name).toBe('doc.pdf')
  })
})
