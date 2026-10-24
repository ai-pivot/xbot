/**
 * InboxEntry —— 侧栏标题行的「收件箱」入口（带未处理数量徽标）。
 *
 * 为什么放在这里：/structured（结构化消息流 + 收件箱）原本只有任务面板里的
 * 一个入口，而任务面板默认不展开 —— 真实用户进不来（2026-10-24 验收：
 * `a[href="/structured"]` 在默认布局里数量为 0）。会话面板是默认左栏，
 * 标题行常驻，放这里才「看得见、点得到」。
 *
 * 数量口径与 /api/inbox 一致：ask_user（待回答）+ report（待批复），
 * 都是 answered=0 且 cancelled=0。
 */
import { useEffect, useState } from 'react'
import { Inbox } from 'lucide-react'

import { StructuredLink } from '@/components/StructuredLink'
import { fetchInbox } from '@/structured/api'
import { useI18n } from '@/providers/i18n'

/** 轮询间隔：结构化消息不频繁，30s 足够；切回标签页时立刻刷新。 */
const POLL_MS = 30_000

export function InboxEntry({ className }: { className?: string }) {
  const { t } = useI18n()
  const [count, setCount] = useState(0)

  useEffect(() => {
    let alive = true
    let timer: ReturnType<typeof setTimeout> | null = null

    const tick = async () => {
      try {
        // 不带 chat_id：服务端按当前登录用户的默认会话解析（子代理的 report
        // 落在该会话库里），正是「我的待办」应有的范围。
        const res = await fetchInbox({ channel: 'web' })
        if (alive) setCount(res.unanswered ?? 0)
      } catch {
        // 未登录 / 网络抖动不该让入口消失：保留上一次的数字。
      }
      if (alive) timer = setTimeout(() => void tick(), POLL_MS)
    }

    void tick()
    const onFocus = () => void tick()
    window.addEventListener('focus', onFocus)
    return () => {
      alive = false
      if (timer) clearTimeout(timer)
      window.removeEventListener('focus', onFocus)
    }
  }, [])

  return (
    <StructuredLink
      className={className}
      title={t('sidebar.inboxTitle')}
    >
      <span className="relative inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-[11px] text-text-secondary hover:bg-bg-tertiary">
        <Inbox className="size-3.5" />
        <span className="truncate">{t('sidebar.inboxEntry')}</span>
        {count > 0 && (
          <span
            data-testid="inbox-badge"
            className="ml-0.5 inline-flex min-w-[1rem] items-center justify-center rounded-full bg-app-accent px-1 text-[10px] font-medium leading-4 text-white"
          >
            {count > 99 ? '99+' : count}
          </span>
        )}
      </span>
    </StructuredLink>
  )
}
