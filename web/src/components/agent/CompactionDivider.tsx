/**
 * CompactionDivider — turn **内部**的上下文压缩点，渲染在迭代之间、与迭代**同级**
 * （Cursor 式 "context summarized"）。
 *
 * 压缩由后端在 LLM 请求前触发（agent.maybeCompress）⇒ 恒在迭代边界 ⇒ 它属于
 * 「它所在的那个 turn」并落在某两个迭代之间（`afterIteration` = 压缩前最后一个
 * 已完成的迭代号；0 = 第一个迭代之前）。
 *
 * 低调内联：一条虚线横线 + 可点击的胶囊（展开看摘要正文）；绝不占用 turn 的
 * user 槽位、也绝不把用户消息顶掉（P0 2026-09-26 的根治）。
 */
import { memo, useState } from 'react'
import { Archive, ChevronDown } from 'lucide-react'

import { cn } from '@/lib/utils'
import { useI18n } from '@/providers/i18n'
import type { WebCompaction } from '@/types/shared'

/** 把 "[Compacted context]\n\n<summary>" 拆成标题 + 正文。 */
function splitMarker(content: string | undefined): { title: string; body: string } {
  const c = content ?? ''
  const trimmed = c.trimStart()
  const firstLineEnd = trimmed.indexOf('\n')
  const title = firstLineEnd === -1 ? trimmed : trimmed.slice(0, firstLineEnd).trim()
  const body = firstLineEnd === -1 ? '' : trimmed.slice(firstLineEnd + 1).trim()
  return { title: title || '[Compacted context]', body }
}

/** 单行（横向分隔线）样式 —— 供「迭代之间」与「turn 边界」两处复用。 */
export function CompactionDivider({ compaction }: { compaction: WebCompaction }) {
  const { t } = useI18n()
  const [open, setOpen] = useState(false)
  const { body } = splitMarker(compaction.content)

  return (
    <div className="flex flex-col gap-1.5 py-1" data-testid="compaction-divider">
      <div className="flex items-center gap-2">
        <span className="h-px flex-1 bg-border/70" />
        <button
          type="button"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          className={cn(
            'flex shrink-0 items-center gap-1.5 rounded-full border border-dashed border-border',
            'bg-bg-secondary px-2.5 py-0.5 text-[11px] text-text-muted transition-colors',
            'hover:border-border hover:text-text-secondary',
          )}
        >
          <Archive className="size-3" />
          {t('agent.compacted')}
          {body && (
            <ChevronDown className={cn('size-3 transition-transform', open && 'rotate-180')} />
          )}
        </button>
        <span className="h-px flex-1 bg-border/70" />
      </div>
      {open && body && (
        <div className="max-h-[40vh] overflow-y-auto overscroll-contain rounded-lg border border-border bg-bg-secondary p-3 text-xs whitespace-pre-wrap text-text-secondary">
          {body}
        </div>
      )}
    </div>
  )
}

export const MemoCompactionDivider = memo(CompactionDivider)
