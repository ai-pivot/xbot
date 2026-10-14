import { memo, useEffect, useRef, useState } from 'react'
import { MessageSquare } from 'lucide-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { useI18n } from '@/providers/i18n'
import { parseAnnotatedMessage } from '@/lib/messageAnnotations'

interface ResponseAnnotation {
  text: string
  annotation: string
}

/** Presentation only: never change the message used for copy, rewind or transport. */
export function parseResponseAnnotations(content: string): {
  annotations: ResponseAnnotation[]
  body: string
} | null {
  // Current envelopes use the shared protocol validator; keep legacy history readable.
  if (content.startsWith('# Response annotations:\n')) return parseAnnotatedMessage(content)
  // Only recognize the generated envelope at the beginning of a user message.
  // A quoted tag in an ordinary message must not hide surrounding user content.
  const match = content.match(/^\s*(?:#{1,6}\s*)?Response annotations:\s*[\s\S]*?<response-annotations>\s*([\s\S]*?)\s*<\/response-annotations>\s*(?:#{1,6}\s*)?My request:\s*([\s\S]*)$/)
  if (!match) return null
  try {
    const entries: unknown = JSON.parse(match[1])
    if (!Array.isArray(entries) || entries.length === 0) return null
    if (!entries.every((entry) => entry && typeof entry === 'object'
      && typeof entry.text === 'string' && typeof entry.annotation === 'string')) return null
    return {
      annotations: entries.map((entry) => ({ text: entry.text, annotation: entry.annotation })),
      body: match[2],
    }
  } catch {
    return null
  }
}

export const ResponseAnnotations = memo(function ResponseAnnotations({ annotations }: {
  annotations: ResponseAnnotation[]
}) {
  const { locale } = useI18n()
  const zh = locale === 'zh-CN'
  const label = zh ? `${annotations.length} 条注释` : `${annotations.length} ${annotations.length === 1 ? 'annotation' : 'annotations'}`
  const [open, setOpen] = useState(false)
  const pinned = useRef(false)
  const closeTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined)
  const cancelClose = () => clearTimeout(closeTimer.current)
  const show = () => { cancelClose(); setOpen(true) }
  const scheduleClose = () => {
    cancelClose()
    closeTimer.current = setTimeout(() => {
      if (!pinned.current) setOpen(false)
    }, 180)
  }
  useEffect(() => () => clearTimeout(closeTimer.current), [])

  return (
    <Popover open={open} onOpenChange={(value) => {
      cancelClose()
      pinned.current = value
      setOpen(value)
    }}>
      <PopoverTrigger asChild>
        <button
          type="button"
          onPointerEnter={(event) => { if (event.pointerType !== 'touch') show() }}
          onPointerLeave={scheduleClose}
          onFocus={show}
          onBlur={scheduleClose}
          onClick={(event) => {
            event.preventDefault()
            cancelClose()
            pinned.current = !pinned.current
            setOpen(pinned.current)
          }}
          className="inline-flex min-h-8 items-center gap-1.5 rounded-full border border-border bg-bg-primary/60 px-3 py-1 text-sm font-medium text-text-secondary transition-colors hover:bg-bg-secondary focus-visible:outline-2 focus-visible:outline-accent"
        >
          <MessageSquare className="size-3.5 text-text-muted" aria-hidden="true" />
          {label}
        </button>
      </PopoverTrigger>
      <PopoverContent
        aria-label={label}
        side="bottom"
        align="end"
        sideOffset={6}
        onOpenAutoFocus={(event) => event.preventDefault()}
        onCloseAutoFocus={(event) => event.preventDefault()}
        onPointerEnter={cancelClose}
        onPointerLeave={scheduleClose}
        onFocusCapture={show}
        onBlurCapture={(event) => {
          if (!event.currentTarget.contains(event.relatedTarget)) scheduleClose()
        }}
        className="w-[min(38rem,calc(100vw-1.5rem))] max-h-[min(32rem,var(--radix-popover-content-available-height))] overflow-y-auto rounded-2xl border-border bg-bg-primary p-5 text-text-primary shadow-lg select-text"
      >
        <ol className="space-y-5">
          {annotations.map((entry, index) => (
            <li key={index} className="flex min-w-0 gap-3 text-sm leading-relaxed">
              <span className="shrink-0 tabular-nums text-text-muted">{index + 1}.</span>
              <div className="min-w-0 flex-1 space-y-3 wrap-anywhere">
                <div>
                  <div className="mb-1 text-text-muted">{zh ? '所选文本：' : 'Selected text:'}</div>
                  <div className="whitespace-pre-wrap">{entry.text}</div>
                </div>
                <div>
                  <div className="mb-1 text-text-muted">{zh ? '用户评论：' : 'User comment:'}</div>
                  <div className="whitespace-pre-wrap font-medium">{entry.annotation}</div>
                </div>
              </div>
            </li>
          ))}
        </ol>
      </PopoverContent>
    </Popover>
  )
})
