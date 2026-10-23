/** 时间回放条：滑块 + 1x/10x/100x + 播放/暂停 + 回到最新。 */
interface Props {
  min: number
  max: number
  cursor: number
  playing: boolean
  speed: number
  onSeek: (t: number) => void
  onToggle: () => void
  onSpeed: (s: number) => void
  onLive: () => void
}

function fmt(t: number): string {
  return new Date(t).toLocaleString('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit',
  })
}

export function PlaybackBar({ min, max, cursor, playing, speed, onSeek, onToggle, onSpeed, onLive }: Props) {
  const pct = max > min ? ((cursor - min) / (max - min)) * 100 : 100
  const live = cursor >= max - 1
  return (
    <div className="flex items-center gap-3 border-t border-border bg-bg-secondary px-4 py-2">
      <button type="button" onClick={onToggle}
        className={playing
          ? 'flex size-7 items-center justify-center rounded-full bg-destructive/15 text-[11px] font-bold text-destructive'
          : 'flex size-7 items-center justify-center rounded-full bg-app-accent/15 text-[11px] font-bold text-app-accent'}
        aria-label={playing ? '暂停回放' : '开始回放'}>
        {playing ? '❚❚' : '▶'}
      </button>
      <span className="w-[112px] shrink-0 font-mono text-[10px] text-text-secondary">
        {fmt(cursor)}
      </span>
      <div className="relative min-w-0 flex-1">
        <input
          type="range"
          min={min}
          max={max}
          value={cursor}
          onChange={(e) => onSeek(Number(e.target.value))}
          className="h-1 w-full cursor-pointer appearance-none rounded-full"
          style={{
            background: `linear-gradient(to right, var(--accent) 0%, var(--accent) ${pct}%, var(--bg-tertiary) ${pct}%, var(--bg-tertiary) 100%)`,
          }}
          aria-label="回放时间"
        />
      </div>
      <span className="shrink-0 font-mono text-[10px] text-text-muted">{fmt(max)}</span>
      <div className="flex shrink-0 items-center gap-1">
        {[1, 10, 100].map((s) => (
          <button key={s} type="button" onClick={() => onSpeed(s)}
            className={speed === s
              ? 'rounded bg-app-accent/15 px-1.5 py-0.5 font-mono text-[10px] text-app-accent'
              : 'rounded bg-bg-tertiary px-1.5 py-0.5 font-mono text-[10px] text-text-secondary'}>
            {s}x
          </button>
        ))}
      </div>
      <button type="button" onClick={onLive}
        className={live
          ? 'shrink-0 rounded bg-status-running/10 px-2 py-1 text-[10px] text-status-running'
          : 'shrink-0 rounded bg-status-waiting/15 px-2 py-1 text-[10px] text-status-waiting'}>
        {live ? '● 实时' : '回到最新'}
      </button>
    </div>
  )
}
