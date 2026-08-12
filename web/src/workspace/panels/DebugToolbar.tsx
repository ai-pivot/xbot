import { Loader2, Radio } from 'lucide-react'

import { useSSERecorder } from '@/hooks/useSSERecorder'
import type { WSConnection } from '@/types/ws'

/**
 * Developer toolbar: REC button toggles SSE recording. While recording, every
 * WS/SSE message is captured; pressing it again downloads an SSE-format dump
 * (`.ev`) that the replay-test infrastructure (src/test-utils/sseReplay.ts)
 * consumes. Reproduce a bug → download → pin a regression test.
 */
export function DebugToolbar({ ws, getStateSnapshot }: { ws: WSConnection; getStateSnapshot?: () => unknown }) {
  const { recording, count, start, stop } = useSSERecorder(ws, getStateSnapshot)

  if (!recording && count === 0) {
    return (
      <div className="flex items-center gap-2 border-b border-border/50 px-3 py-1">
        <button
          type="button"
          onClick={start}
          className="flex items-center gap-1.5 rounded border border-border/60 px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:border-red-400/60 hover:text-red-500"
          title="开始录制 SSE 事件，结束后下载 .ev 文件用于重放复现"
        >
          <Radio className="size-3" />
          REC
        </button>
      </div>
    )
  }

  return (
    <div className="flex items-center gap-2 border-b border-red-400/30 bg-red-500/5 px-3 py-1">
      <button
        type="button"
        onClick={stop}
        className="flex items-center gap-1.5 rounded border border-red-400/60 bg-red-500/10 px-2 py-0.5 text-[11px] text-red-500 transition-colors hover:bg-red-500/20"
        title="停止录制并下载 SSE dump"
      >
        <span className="size-2 animate-pulse rounded-full bg-red-500" />
        STOP ({count})
      </button>
      <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
        <Loader2 className="size-3 animate-spin" />
        录制中 — 复现 bug 后点击 STOP 下载
      </span>
    </div>
  )
}
