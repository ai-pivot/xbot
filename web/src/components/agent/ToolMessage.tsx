import { Wrench } from 'lucide-react'

import { MarkdownRenderer } from './MarkdownRenderer'
import { useI18n } from '@/providers/i18n'
import type { ChatMessage } from '@/types/shared'

export function ToolMessage({ message }: { message: ChatMessage }) {
  const { t } = useI18n()
  const label = message.toolName || message.toolCallID || t('agent.tools')

  return (
    <details className="border-l-2 border-border bg-bg-secondary/40 px-3 text-sm text-text-secondary">
      <summary className="flex cursor-pointer items-center gap-2 py-2 font-medium">
        <Wrench className="size-3.5 shrink-0" />
        <span className="min-w-0 truncate">{label}</span>
      </summary>
      <div className="border-t border-border/50 py-3">
        {message.toolArguments ? <ToolArguments value={message.toolArguments} /> : null}
        <MarkdownRenderer content={message.content || ' '} />
      </div>
    </details>
  )
}

export function RawToolCalls({ calls }: { calls: NonNullable<ChatMessage['toolCalls']> }) {
  return (
    <div className="mb-2 space-y-1.5">
      {calls.map((call, index) => (
        <details
          key={call.id || `${call.name}-${index}`}
          className="border-l-2 border-border bg-bg-secondary/40 px-3 text-sm text-text-secondary"
        >
          <summary className="flex cursor-pointer items-center gap-2 py-2 font-medium">
            <Wrench className="size-3.5 shrink-0" />
            <span className="min-w-0 truncate">{call.name || call.id}</span>
          </summary>
          {call.arguments ? (
            <div className="border-t border-border/50 py-3">
              <ToolArguments value={call.arguments} />
            </div>
          ) : null}
        </details>
      ))}
    </div>
  )
}

function ToolArguments({ value }: { value: string }) {
  return (
    <pre className="mb-2 max-h-64 overflow-auto whitespace-pre-wrap break-words rounded-md bg-bg-tertiary p-2 font-mono text-xs text-text-muted">
      {value}
    </pre>
  )
}
