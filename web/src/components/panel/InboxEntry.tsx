/**
 * InboxEntry —— 「收件箱」入口（带未处理数量徽标）。
 *
 * 为什么放在这里：/structured（结构化消息流 + 收件箱）原本只有任务面板里的
 * 一个入口，而任务面板默认不展开 —— 真实用户进不来（2026-10-24 验收：
 * 默认布局里 `a[href="/structured"]` 数量为 0）。会话面板是默认左栏、标题行常驻，
 * 放这里才「看得见、点得到」；手机抽屉里也挂了同一个入口。
 *
 * 数量口径与 /api/inbox 一致（ask_user 待回答 + report 待批复，answered=0 且
 * cancelled=0），数字直接取服务端 `unanswered`，并在看板上处理完立刻收敛
 * —— 数据来自共享的 inboxStore，不再各组件独立轮询（N1：状态必须流转）。
 *
 * 角标只在 unanswered > 0 时渲染（0 是常态，常驻一个「0」是噪音）。
 *
 * 样式与 Inbox 面板内的徽标同源（bg-kind-ask-user + text-bg-primary），
 * 保证同一语义在两处颜色一致。
 */
import { Inbox } from 'lucide-react'

import { StructuredLink } from '@/components/StructuredLink'
import { useInboxPolling } from '@/structured/inboxStore'
import { useI18n } from '@/providers/i18n'

export function InboxEntry({ className }: { className?: string }) {
  const { t } = useI18n()
  const { unanswered } = useInboxPolling()

  return (
    <StructuredLink className={className} title={t('sidebar.inboxTitle')}>
      <span className="flex min-w-0 cursor-pointer items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-text-secondary transition-colors hover:bg-bg-tertiary">
        <Inbox className="size-3.5 shrink-0" aria-hidden />
        <span className="min-w-0 truncate">{t('sidebar.inboxEntry')}</span>
        {unanswered > 0 ? (
          <span
            data-testid="inbox-entry-badge"
            className="ml-0.5 inline-flex min-w-4 shrink-0 items-center justify-center rounded-full bg-kind-ask-user px-1 text-[10px] font-bold leading-4 text-bg-primary"
          >
            {unanswered > 99 ? '99+' : unanswered}
          </span>
        ) : null}
      </span>
    </StructuredLink>
  )
}
