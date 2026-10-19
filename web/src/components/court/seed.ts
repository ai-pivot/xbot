/**
 * 前端内置演示数据（API 无数据/失败时的兜底）。
 *
 * 与 scripts/seed-group-demo.go 的数据形状完全一致：同一 group_id、同一批
 * 成员、同样的 kind / correlation_id 链。这样“真实 API 断掉”与“真实 API 可用”
 * 两种情况下，面板的结构与交互完全一致，不会出现两套 UI。
 */
import type { Group, GroupMember, GroupMessage, Lane } from './types'

export const DEMO_GROUP_ID = 'grp_court_demo'
export const DEMO_CHAT_ID = 'court-demo'

export const demoGroup: Group = {
  id: DEMO_GROUP_ID,
  name: '朝廷晨会',
  owner: 'web-1',
  created_at: new Date().toISOString(),
  member_count: 4,
}

export const demoMembers: GroupMember[] = [
  { group_id: DEMO_GROUP_ID, member_addr: 'web-1', status: 'alive', joined_at: '', name: '首辅', role: 'owner' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:hubu/main', status: 'alive', joined_at: '', name: '户部', role: 'hubu', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:gongbu/main', status: 'alive', joined_at: '', name: '工部', role: 'gongbu', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:libu/main', status: 'alive', joined_at: '', name: '吏部', role: 'libu', instance: 'main' },
]

interface Topic {
  corr: string
  title: string
  askee: string
  askeeName: string
  report: string
  metrics: Record<string, unknown>
}

const TOPICS: Topic[] = [
  {
    corr: 'corr_d1_gpu', title: 'B300 集群今日吞吐', askee: 'agent:gongbu/main', askeeName: '工部',
    report: '工部回报：8 卡 B300 集群 2P1D 稳定，TPOT P50 18.2ms，TPM 142k。',
    metrics: { tpm: 142000, tpot_ms: 18.2, nodes: 8 },
  },
  {
    corr: 'corr_d1_serve', title: '户部：昨日 token 用量', askee: 'agent:hubu/main', askeeName: '户部',
    report: '户部回报：昨日总 token 8.4B，缓存命中率 63%，成本环比 -7%。',
    metrics: { tokens: '8.4B', cache_hit: 0.63, cost_delta: -0.07 },
  },
  {
    corr: 'corr_d1_people', title: '吏部：值班与技能清单', askee: 'agent:libu/main', askeeName: '吏部',
    report: '吏部回报：三名值守 agent 技能齐备，轮值表已更新。',
    metrics: { agents: 3, skills: 14 },
  },
]

let nextId = 1

function msg(
  ts: Date,
  sender: string,
  senderType: 'user' | 'agent',
  kind: GroupMessage['kind'],
  corr: string,
  content: string,
  to: string[],
  payload?: Record<string, unknown>,
): GroupMessage {
  const name =
    sender === 'web-1' ? '首辅'
      : sender === 'agent:hubu/main' ? '户部'
        : sender === 'agent:gongbu/main' ? '工部'
          : sender === 'agent:libu/main' ? '吏部' : sender
  return {
    id: nextId++,
    group_id: DEMO_GROUP_ID,
    kind,
    correlation_id: corr || undefined,
    sender_type: senderType,
    sender_id: sender.startsWith('agent:') ? sender.slice('agent:'.length) : sender,
    sender_name: name,
    role: senderType === 'user' ? 'user' : 'assistant',
    content,
    to,
    payload,
    created_at: ts.toISOString(),
  }
}

/** 生成 3 天 × 3 议题 × 7 条 + 每天下午协同 = 69 条演示消息。 */
export function buildDemoMessages(now = new Date()): GroupMessage[] {
  nextId = 1
  const out: GroupMessage[] = []
  const day0 = new Date(now)
  day0.setDate(day0.getDate() - 3)
  day0.setHours(0, 0, 0, 0)

  const at = (dayOffset: number, hour: number, minute: number) => {
    const d = new Date(day0)
    d.setDate(d.getDate() + dayOffset)
    d.setHours(hour, minute, 0, 0)
    return d
  }

  for (let d = 0; d < 3; d++) {
    TOPICS.forEach((t, ti) => {
      const corr = `${t.corr}_${d}`
      const h = 9 + ti
      out.push(msg(at(d, h, 0), 'web-1', 'user', 'decision', corr,
        `今日议题 ${ti + 1}：${t.title}。请${t.askeeName}核实后回报。`, [t.askee],
        { decision: 'approve', topic: t.title, priority: 'P1' }))
      out.push(msg(at(d, h, 4), 'web-1', 'user', 'ask_user', corr,
        `请确认：${t.title} 是否需要扩到 16 卡？`, [t.askee],
        { question: '是否需要扩到 16 卡？', options: ['扩', '不扩', '观望'] }))
      out.push(msg(at(d, h, 11), t.askee, 'agent', 'answer', corr,
        '回禀：当前负载 72%，暂不扩容；若 TPM 连续两日 >160k 再申请。', ['web-1'],
        { answer: '暂不扩容', confidence: 0.86 }))
      out.push(msg(at(d, h, 26), t.askee, 'agent', 'report', corr,
        t.report, ['web-1'], { summary: t.report, metrics: t.metrics }))
      out.push(msg(at(d, h, 34), 'web-1', 'user', 'decision', corr,
        '准。继续按现网配置运行，明日晨会复盘。', [t.askee],
        { decision: 'approved', review_at: 'tomorrow' }))
      out.push(msg(at(d, h, 41), t.askee, 'agent', 'action', corr,
        `${t.askeeName}已执行：更新看板并登记台账。`, ['web-1'],
        { action: 'update_dashboard', status: 'done' }))
      out.push(msg(at(d, h, 55), 'web-1', 'user', 'text', '',
        '收到，散会。', []))
    })
    out.push(msg(at(d, 14, 0), 'agent:gongbu/main', 'agent', 'text', '',
      'B300-3 节点网卡抖动，已切流；观察 30 分钟。', ['agent:hubu/main', 'web-1'],
      { severity: 'warn' }))
    out.push(msg(at(d, 14, 9), 'agent:hubu/main', 'agent', 'text', '',
      '收到，已把流量切到 B300-4，业务无感。', ['agent:gongbu/main']))
  }
  // 最后一条“未回答的 ask_user”，用于展示呼吸脉冲与 blocked 状态。
  const last = new Date(out[out.length - 1].created_at)
  last.setMinutes(last.getMinutes() + 6)
  out.push(msg(last, 'web-1', 'user', 'ask_user', 'corr_pending',
    '工部：16 卡扩容的预算评估今晚能给吗？', ['agent:gongbu/main'],
    { question: '预算评估今晚能给吗？', pending: true }))
  return out
}

/** 由消息推导泳道状态：blocked = 有未回答 ask_user 指向它。 */
export function deriveLanes(members: GroupMember[], messages: GroupMessage[]): Lane[] {
  const answered = new Set<string>()
  for (const m of messages) {
    if (m.kind === 'answer' && m.correlation_id) answered.add(m.correlation_id)
  }
  const lastSeen = new Map<string, string>()
  const pendingBy = new Map<string, number>()
  for (const m of messages) {
    const sender = m.sender_type === 'agent' ? `agent:${m.sender_id}` : m.sender_id
    lastSeen.set(sender, m.content)
    if (m.kind === 'ask_user' && m.correlation_id && !answered.has(m.correlation_id)) {
      for (const to of m.to ?? []) pendingBy.set(to, (pendingBy.get(to) ?? 0) + 1)
    }
  }
  const closed = new Map<string, number>()
  const total = new Map<string, number>()
  for (const m of messages) {
    if (!m.correlation_id) continue
    const sender = m.sender_type === 'agent' ? `agent:${m.sender_id}` : m.sender_id
    for (const who of [sender, ...(m.to ?? [])]) {
      total.set(who, (total.get(who) ?? 0) + 1)
      if (m.kind === 'decision' && String(m.payload?.decision ?? '').includes('approved')) {
        closed.set(who, (closed.get(who) ?? 0) + 1)
      }
    }
  }
  return members.map((mb) => {
    const pending = pendingBy.get(mb.member_addr) ?? 0
    const totalN = total.get(mb.member_addr) ?? 0
    const closedN = closed.get(mb.member_addr) ?? 0
    const progress = totalN === 0 ? (mb.status === 'alive' ? 0.72 : 0) : Math.min(1, 0.35 + (closedN / totalN) * 0.65)
    return {
      id: mb.member_addr,
      name: mb.name ?? memberNameOf(mb.member_addr),
      type: mb.member_addr.startsWith('agent:') ? 'agent' : 'user',
      status: mb.status !== 'alive' ? 'left' : pending > 0 ? 'blocked' : 'idle',
      task: pending > 0 ? `等待回答 · ${lastSeen.get(mb.member_addr) ?? '—'}` : (lastSeen.get(mb.member_addr) ?? '暂无任务'),
      progress,
    } satisfies Lane
  })
}

function memberNameOf(addr: string): string {
  if (addr === 'web-1') return '首辅'
  const m = demoMembers.find((x) => x.member_addr === addr)
  return m?.name ?? addr
}
