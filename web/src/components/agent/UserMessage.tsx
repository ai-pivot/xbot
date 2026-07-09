/**
 * UserMessage — renders one committed user message (Spec 4 §3.5).
 *
 * Right-aligned bubble. Content is plain text (the backend already folded any
 * uploaded-file references into the text on echo); we render it as Markdown so
 * line breaks and inline code the user may have typed render faithfully.
 */
import { memo } from 'react'
import { RotateCcw } from 'lucide-react'

import { MarkdownRenderer } from './MarkdownRenderer'
import { Button } from '@/components/ui/button'

interface UserMessageProps {
  content: string
  onRewind?: () => void
}

export const UserMessage = memo(function UserMessage({ content, onRewind }: UserMessageProps) {
  return (
    <div className="flex justify-end px-1">
      <div className="flex max-w-[85%] flex-col items-end gap-1">
        <div className="rounded-2xl rounded-br-sm bg-accent/15 px-3.5 py-2 text-text-primary">
          <MarkdownRenderer content={content || ' '} />
        </div>
        {onRewind && (
          <Button
            type="button"
            variant="ghost"
            size="icon-xs"
            aria-label="rewind"
            className="h-6 w-6 opacity-60 hover:opacity-100"
            onClick={onRewind}
          >
            <RotateCcw className="size-3.5" />
          </Button>
        )}
      </div>
    </div>
  )
})
