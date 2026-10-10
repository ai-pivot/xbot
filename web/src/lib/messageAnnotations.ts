/** Snapshot hints, not permanent source anchors (rewind can reuse turn/iteration numbers). */
export interface AnnotationSource {
  turnID?: number
  iteration?: number
  messageID?: number
  /** Half-open UTF-16 offsets into the selected body's concatenated DOM text. */
  startOffset?: number
  endOffset?: number
}

export interface MessageAnnotation {
  id: string
  source: AnnotationSource
  quote: string
  comment: string
}

export interface ResponseAnnotation {
  text: string
  annotation: string
  source: {
    turnId?: number
    iteration?: number
    messageId?: number
    startOffset?: number
    endOffset?: number
    offsetBasis?: 'dom-text'
    offsetEncoding?: 'utf-16'
  }
}

export const ANNOTATION_STORAGE_PREFIX = 'xbot:annotations:'
export const ANNOTATION_LIMITS = { count: 10, quote: 6000, comment: 2000, total: 20000 } as const

export function annotationStorageKey(username: string, sessionKey: string): string {
  return ANNOTATION_STORAGE_PREFIX + JSON.stringify([username, sessionKey])
}

export function validateAnnotations(items: readonly MessageAnnotation[]): string | null {
  if (items.length > ANNOTATION_LIMITS.count) return 'tooMany'
  let total = 0
  for (const item of items) {
    const quoteLength = [...item.quote].length
    const commentLength = [...item.comment].length
    if (!item.quote.trim() || !item.comment.trim()) return 'empty'
    if (quoteLength > ANNOTATION_LIMITS.quote) return 'quoteTooLong'
    if (commentLength > ANNOTATION_LIMITS.comment) return 'commentTooLong'
    total += quoteLength + commentLength
  }
  return total > ANNOTATION_LIMITS.total ? 'totalTooLong' : null
}

function isSource(value: unknown): value is AnnotationSource {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const source = value as AnnotationSource
  const identitiesValid = [source.turnID, source.iteration, source.messageID]
    .every((n) => n === undefined || (Number.isSafeInteger(n) && n > 0))
  const { startOffset, endOffset } = source
  const offsetsValid = startOffset === undefined && endOffset === undefined
    || (Number.isSafeInteger(startOffset) && Number.isSafeInteger(endOffset) && startOffset! >= 0 && endOffset! > startOffset!)
  return identitiesValid && offsetsValid
}

function isAnnotation(value: unknown): value is MessageAnnotation {
  if (!value || typeof value !== 'object') return false
  const item = value as Partial<MessageAnnotation>
  if (typeof item.id !== 'string' || typeof item.quote !== 'string' || typeof item.comment !== 'string') return false
  return isSource(item.source)
}

export function loadAnnotations(key: string): MessageAnnotation[] {
  const raw = localStorage.getItem(key)
  if (!raw) return []
  const parsed: unknown = JSON.parse(raw)
  if (!Array.isArray(parsed) || !parsed.every(isAnnotation) || validateAnnotations(parsed)) {
    throw new Error('Invalid annotation draft')
  }
  return parsed
}

/** A fence longer than every backtick run preserves the exact snapshot as literal text. */
function literalBlock(text: string): string {
  const length = Math.max(3, ...Array.from(text.matchAll(/`+/g), (match) => match[0].length + 1))
  const fence = '`'.repeat(length)
  return `${fence}text\n${text}\n${fence}`
}

type Translate = (key: string, params?: Record<string, string | number>) => string

const RESPONSE_HEADER = '# Response annotations:\n'
const OPEN_TAG = '<response-annotations>'
const CLOSE_TAG = '</response-annotations>'
const REQUEST_HEADER = '\n\n# My request:'

/** One ordinary user message: the existing ingress, queue, history and replay remain authoritative. */
export function formatAnnotatedMessage(body: string, items: readonly MessageAnnotation[], t: Translate): string {
  if (!items.length) return body
  const payload: ResponseAnnotation[] = items.map(({ quote, comment, source }) => ({
    text: quote,
    annotation: comment,
    source: {
      turnId: source.turnID,
      iteration: source.iteration,
      messageId: source.messageID,
      startOffset: source.startOffset,
      endOffset: source.endOffset,
      ...(source.startOffset !== undefined && source.endOffset !== undefined
        ? { offsetBasis: 'dom-text', offsetEncoding: 'utf-16' } as const : {}),
    },
  }))
  // JSON Unicode escapes keep quotes/comments literal without introducing XML tags or entities.
  const json = JSON.stringify(payload, null, 2).replace(/[<>&]/g, (char) => `\\u${char.charCodeAt(0).toString(16).padStart(4, '0')}`)
  return `${RESPONSE_HEADER}${t('agent.annotations.feedbackInstructions')}\n${OPEN_TAG}\n${json}\n${CLOSE_TAG}${REQUEST_HEADER}\n${body}`
}

function isResponseAnnotation(value: unknown): value is ResponseAnnotation {
  if (!value || typeof value !== 'object') return false
  const item = value as ResponseAnnotation
  if (typeof item.text !== 'string' || typeof item.annotation !== 'string') return false
  if (!item.source || typeof item.source !== 'object' || Array.isArray(item.source)) return false
  const source = item.source
  if (source.offsetBasis !== undefined && source.offsetBasis !== 'dom-text') return false
  if (source.offsetEncoding !== undefined && source.offsetEncoding !== 'utf-16') return false
  return isSource({ turnID: source.turnId, iteration: source.iteration, messageID: source.messageId, startOffset: source.startOffset, endOffset: source.endOffset })
}

/** Decode only complete outgoing envelopes; ordinary XML examples and malformed messages stay untouched. */
export function parseAnnotatedMessage(content: string): { body: string; annotations: ResponseAnnotation[] } | null {
  if (!content.startsWith(RESPONSE_HEADER)) return null
  const start = content.indexOf(`\n${OPEN_TAG}\n`, RESPONSE_HEADER.length)
  if (start < 0) return null
  const end = content.indexOf(`\n${CLOSE_TAG}`, start)
  if (end < 0) return null
  const suffix = content.slice(end + 1 + CLOSE_TAG.length)
  if (!suffix.startsWith(REQUEST_HEADER)) return null
  const request = suffix.slice(REQUEST_HEADER.length)
  if (request && !request.startsWith('\n')) return null
  const xml = new DOMParser().parseFromString(content.slice(start + 1, end + 1 + CLOSE_TAG.length), 'application/xml')
  if (xml.documentElement.tagName !== 'response-annotations' || xml.documentElement.children.length) return null
  try {
    const payload: unknown = JSON.parse(xml.documentElement.textContent ?? '')
    if (!Array.isArray(payload) || !payload.length || !payload.every(isResponseAnnotation)) return null
    if (validateAnnotations(payload.map((item, index) => ({ id: String(index), source: {}, quote: item.text, comment: item.annotation })))) return null
    return { body: request.slice(1), annotations: payload }
  } catch { return null }
}

/** Display quotes/comments, while copy, edit, storage and model replay keep the original XML prompt. */
export function annotatedMessageDisplay(content: string, t: Translate): string {
  const parsed = parseAnnotatedMessage(content)
  if (!parsed) return content
  const feedback = parsed.annotations.map((item, index) => [
    `### ${t('agent.annotations.item', { number: index + 1 })}`,
    `**${t('agent.annotations.quote')}**\n\n${literalBlock(item.text)}`,
    `**${t('agent.annotations.comment')}**\n\n${literalBlock(item.annotation)}`,
  ].join('\n\n')).join('\n\n')
  return [parsed.body, t('agent.annotations.feedbackHeading'), feedback].filter(Boolean).join('\n\n')
}
