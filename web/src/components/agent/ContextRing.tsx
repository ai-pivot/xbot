import { cn } from '@/lib/utils'
import { Tooltip, TooltipTrigger, TooltipContent } from '@/components/ui/tooltip'
import { useI18n } from '@/providers/i18n'

interface ContextRingProps {
  available: boolean
  promptTokens: number
  maxContext: number
  usagePercent: number | null
}

export function formatTokensAsK(tokens: number): string {
  const value = Math.max(0, tokens) / 1000
  if (Number.isInteger(value)) return `${value}K`
  return `${value.toFixed(1).replace(/\.0$/, '')}K`
}

function formatUsagePercent(percent: number): string {
  return Number(percent.toFixed(3)).toString()
}

export function ContextRing({ available, promptTokens, maxContext, usagePercent }: ContextRingProps) {
  const { t } = useI18n()
  const hasUsage = available && usagePercent !== null && Number.isFinite(usagePercent)
  const drawnPercent = hasUsage ? Math.min(100, Math.max(0, usagePercent)) : 0
  const high = hasUsage && usagePercent >= 80
  const label = hasUsage
    ? t('agent.contextUsage', {
        percent: formatUsagePercent(usagePercent),
        used: formatTokensAsK(promptTokens),
        available: formatTokensAsK(maxContext),
      })
    : t('agent.contextUsageUnknown', {
        available: maxContext > 0 ? formatTokensAsK(maxContext) : '—',
      })

  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <span
          role="img"
          tabIndex={0}
          aria-label={label}
          data-testid="context-ring"
          className="flex size-7 shrink-0 items-center justify-center rounded-md text-text-muted outline-none hover:bg-surface-bg focus-visible:ring-1 focus-visible:ring-accent/50"
        >
          <svg viewBox="0 0 20 20" className="size-5" aria-hidden="true">
            <circle
              cx="10"
              cy="10"
              r="7.5"
              fill="none"
              stroke="currentColor"
              strokeWidth="2"
              opacity="0.22"
            />
            {hasUsage ? (
              <circle
                cx="10"
                cy="10"
                r="7.5"
                fill="none"
                pathLength="100"
                stroke="currentColor"
                strokeWidth="2"
                strokeLinecap="round"
                strokeDasharray="100"
                strokeDashoffset={100 - drawnPercent}
                transform="rotate(-90 10 10)"
                className={cn(
                  'transition-[stroke-dashoffset,color] duration-300',
                  high ? 'text-status-error' : 'text-accent',
                )}
              />
            ) : null}
          </svg>
        </span>
      </TooltipTrigger>
      <TooltipContent side="top" sideOffset={6} className="font-mono">
        {label}
      </TooltipContent>
    </Tooltip>
  )
}
