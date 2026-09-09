import { useEffect, useState } from 'react'
import { FileDown, Loader2, Radio } from 'lucide-react'

import { useSSERecorder } from '@/hooks/useSSERecorder'
import { useI18n } from '@/providers/i18n'
import type { WSConnection } from '@/types/ws'

/**
 * Developer toolbar:
 *  - REC: toggles SSE recording. While recording, every WS/SSE message is
 *    captured; pressing it again downloads an SSE-format dump (`.ev`) that
 *    the replay-test infrastructure (src/test-utils/sseReplay.ts) consumes.
 *    Reproduce a bug → download → pin a regression test.
 *  - DUMP LLM: toggles server-side dumping of EVERY /chat/completions
 *    request body to ~/.xbot/llm_dumps/ (one JSON file per request, named
 *    req_<timestamp>_<sha12>.json). Loop-incident diagnosis: diff two
 *    consecutive request bodies to see exactly what the model received and
 *    whether the loop-breaker warning made it back into the prompt.
 */
export function DebugToolbar({ ws, getStateSnapshot }: { ws: WSConnection; getStateSnapshot?: () => unknown }) {
  const { t } = useI18n()
  const { recording, count, start, stop } = useSSERecorder(ws, getStateSnapshot)
  const [llmDumpOn, setLlmDumpOn] = useState<boolean | null>(null)
  const [llmDumpBusy, setLlmDumpBusy] = useState(false)

  // Read the current toggle state once on mount (server-side atomic.Bool —
  // it survives panel remounts but resets on server restart).
  useEffect(() => {
    let cancelled = false
    ws.rpc<{ enabled: boolean }>('llm_dump_reqs', {})
      .then((r) => { if (!cancelled) setLlmDumpOn(Boolean(r?.enabled)) })
      .catch(() => { if (!cancelled) setLlmDumpOn(false) })
    return () => { cancelled = true }
  }, [ws])

  const toggleLlmDump = async () => {
    if (llmDumpBusy || llmDumpOn === null) return
    setLlmDumpBusy(true)
    try {
      const r = await ws.rpc<{ enabled: boolean }>('llm_dump_reqs', { enabled: !llmDumpOn })
      setLlmDumpOn(Boolean(r?.enabled))
    } catch {
      // keep the previous state — the RPC failure is visible in devtools
    } finally {
      setLlmDumpBusy(false)
    }
  }

  const llmDumpActive = llmDumpOn === true

  return (
    <div className="flex items-center gap-2 border-b border-border/50 px-3 py-1">
      {!recording && count === 0 ? (
        <button
          type="button"
          onClick={start}
          className="flex items-center gap-1.5 rounded border border-border/60 px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:border-red-400/60 hover:text-red-500"
          title={t('panel.debugRecStart')}
        >
          <Radio className="size-3" />
          REC
        </button>
      ) : (
        <button
          type="button"
          onClick={stop}
          className="flex items-center gap-1.5 rounded border border-red-400/60 bg-red-500/10 px-2 py-0.5 text-[11px] text-red-500 transition-colors hover:bg-red-500/20"
          title={t('panel.debugRecStop')}
        >
          <span className="size-2 animate-pulse rounded-full bg-red-500" />
          STOP ({count})
        </button>
      )}
      <button
        type="button"
        onClick={() => void toggleLlmDump()}
        disabled={llmDumpBusy || llmDumpOn === null}
        className={
          llmDumpActive
            ? 'flex items-center gap-1.5 rounded border border-amber-400/60 bg-amber-500/10 px-2 py-0.5 text-[11px] text-amber-500 transition-colors hover:bg-amber-500/20'
            : 'flex items-center gap-1.5 rounded border border-border/60 px-2 py-0.5 text-[11px] text-muted-foreground transition-colors hover:border-amber-400/60 hover:text-amber-500 disabled:opacity-50'
        }
        title={t('panel.debugDumpLlm')}
      >
        {llmDumpBusy ? <Loader2 className="size-3 animate-spin" /> : <FileDown className="size-3" />}
        {llmDumpActive ? 'DUMP LLM ON' : 'DUMP LLM'}
      </button>
      {recording && count > 0 && (
        <span className="flex items-center gap-1 text-[11px] text-muted-foreground">
          <Loader2 className="size-3 animate-spin" />
          {t('panel.debugRecording')}
        </span>
      )}
      {llmDumpActive && (
        <span className="text-[11px] text-amber-500/80">
          {t('panel.debugDumpActive')}
        </span>
      )}
    </div>
  )
}
