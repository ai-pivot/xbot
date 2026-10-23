/**
 * 前端内置演示数据（只有 `/court-demo` 的「示例模式」用）。
 *
 * 真实面板（`/court`）永远从 API 读，API 失败时显示空态/错误，绝不回落到这里。
 *
 * 编制名必须与朝廷实际架构一致：工部 / 户部 / 翰林院 / 兵部 / 都察院 / 司礼监 /
 * 门下省（首辅 = 主会话本身）。2026-10-22 用户看到示例里出现「吏部 / 礼部 / 刑部」
 * —— 这些部门根本不存在，示例数据写假编制名会让人以为产品认错了组织架构。
 */
import type { Group, GroupMember, GroupMessage, Lane } from './types'

export const DEMO_GROUP_ID = 'grp_court_demo'
export const DEMO_CHAT_ID = 'court-demo'

export const demoGroup: Group = {
  id: DEMO_GROUP_ID,
  name: '示例群组',
  owner: 'web-1',
  created_at: new Date().toISOString(),
  member_count: 8,
}

/** 首辅（主会话）+ 朝廷实际在编的七个部门。 */
export const demoMembers: GroupMember[] = [
  { group_id: DEMO_GROUP_ID, member_addr: 'web-1', status: 'alive', joined_at: '', name: '首辅', role: 'owner' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.gongbu/main', status: 'alive', joined_at: '', name: '工部', role: 'court.gongbu', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.hubu/main', status: 'alive', joined_at: '', name: '户部', role: 'court.hubu', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.hanlin/main', status: 'alive', joined_at: '', name: '翰林院', role: 'court.hanlin', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.bingbu/main', status: 'alive', joined_at: '', name: '兵部', role: 'court.bingbu', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.ducha/main', status: 'alive', joined_at: '', name: '都察院', role: 'court.ducha', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.sili/main', status: 'alive', joined_at: '', name: '司礼监', role: 'court.sili', instance: 'main' },
  { group_id: DEMO_GROUP_ID, member_addr: 'agent:court.menxia/main', status: 'alive', joined_at: '', name: '门下省', role: 'court.menxia', instance: 'main' },
]

interface Topic {
  corr: string
  title: string
  askee: string
  askeeName: string
  report: string
  /** 补充回报一句话（让各议题的往来条数不一样）。 */
  followUp: string
  metrics: Record<string, unknown>
}

const TOPICS: Topic[] = [
  {
    corr: 'corr_d1_deploy', title: '工部：sglang 镜像同步与 PD 就绪', askee: 'agent:court.gongbu/main', askeeName: '工部',
    report: '工部回报：sglang 镜像已同步到 3 个新节点，PD 服务全部就绪。',
    followUp: '新节点上 PD 与 router 已联通，冷启动压测通过。',
    metrics: { nodes: 3, ready: 3 },
  },
  {
    corr: 'corr_d1_disk', title: '户部：工作区清理与磁盘水位', askee: 'agent:court.hubu/main', askeeName: '户部',
    report: '户部回报：清理 12 个过期工作区，释放 41GB 磁盘。',
    followUp: '磁盘水位回到 63%，本周无需再清。',
    metrics: { workspaces: 12, freed_gb: 41 },
  },
  {
    corr: 'corr_d1_docs', title: '翰林院：朝会纪要归档', askee: 'agent:court.hanlin/main', askeeName: '翰林院',
    report: '翰林院回报：今日朝会纪要已归档，含 4 份附议与 2 项待批。',
    followUp: '纪要已同步到知识库，链接发在群内。',
    metrics: { docs: 4, pending: 2 },
  },
  {
    corr: 'corr_d1_scan', title: '兵部：暴露面与凭据巡检', askee: 'agent:court.bingbu/main', askeeName: '兵部',
    report: '兵部回报：暴露面扫描完成，无新增高危端口，2 个旧 key 已轮换。',
    followUp: '下周做一次凭据全量复核。',
    metrics: { new_ports: 0, rotated_keys: 2 },
  },
  {
    corr: 'corr_d1_audit', title: '都察院：证据抽查复算', askee: 'agent:court.ducha/main', askeeName: '都察院',
    report: '都察院回报：抽查 6 份证据，5 份可复算，1 份路径不存在已打回。',
    followUp: '打回的那份已由工部重发，复算通过。',
    metrics: { checked: 6, mismatch: 1 },
  },
  {
    corr: 'corr_d1_digest', title: '司礼监：六部奏折汇总', askee: 'agent:court.sili/main', askeeName: '司礼监',
    report: '司礼监回报：六部奏折已汇总成一页，其中 2 项待首辅批红。',
    followUp: '汇总页已置顶，方便随时回看。',
    metrics: { reports: 6, pending: 2 },
  },
  {
    corr: 'corr_d1_review', title: '门下省：封驳复查', askee: 'agent:court.menxia/main', askeeName: '门下省',
    report: '门下省回报：3 份诏令封驳复查通过，1 份需补证据后重发。',
    followUp: '需补证据的那份已退回起草部门。',
    metrics: { reviewed: 3, rejected: 1 },
  },
]

let nextId = 1

/** 地址 → 展示名（首辅 / 部门名）。 */
function memberNameOf(addr: string): string {
  if (addr === 'web-1') return '首辅'
  const m = demoMembers.find((x) => x.member_addr === addr)
  return m?.name ?? addr.replace('agent:', '')
}

/** 会话地址 → 来源字段（与真实后端 sources.ts 填的口径一致：channel/chat_id/role）。 */
function sourceOf(lane: string): Pick<GroupMessage, 'sourceChannel' | 'sourceChatID' | 'sourceRole'> {
  const role = lane.startsWith('agent:') ? lane.slice('agent:'.length).split('/')[0] : undefined
  return {
    sourceChannel: 'web',
    sourceChatID: role ? `chat-${role.replace('court.', '')}` : DEMO_CHAT_ID,
    sourceRole: role,
  }
}

/**
 * 一条示例消息。
 *
 * `lane` = 这条消息**落在哪个会话**（真实后端里一次对话的消息都落在同一个会话库）。
 * 不传就按发送者自己算。示例数据必须带上 laneID / source*，否则线程视图只能显示
 * 「未知来源」（2026-10-22 用户报障：右栏 14 条线程全是未知来源）。
 */
function msg(
  ts: Date,
  sender: string,
  senderType: 'user' | 'agent',
  kind: GroupMessage['kind'],
  corr: string,
  content: string,
  to: string[],
  payload?: Record<string, unknown>,
  lane?: string,
): GroupMessage {
  const laneID = lane || sender
  return {
    id: nextId++,
    group_id: DEMO_GROUP_ID,
    kind,
    correlation_id: corr || undefined,
    sender_type: senderType,
    sender_id: sender.startsWith('agent:') ? sender.slice('agent:'.length) : sender,
    sender_name: memberNameOf(sender),
    role: senderType === 'user' ? 'user' : 'assistant',
    content,
    to,
    payload,
    created_at: ts.toISOString(),
    laneID,
    ...sourceOf(laneID),
  }
}

/** 生成 2 天 × 7 议题 × 7 条 + 每日协同 2 条 + 1 条未答提问 = 103 条演示消息。 */
export function buildDemoMessages(now = new Date()): GroupMessage[] {
  nextId = 1
  const out: GroupMessage[] = []
  const day0 = new Date(now)
  day0.setDate(day0.getDate() - 2)
  day0.setHours(0, 0, 0, 0)

  const at = (dayOffset: number, hour: number, minute: number) => {
    const d = new Date(day0)
    d.setDate(d.getDate() + dayOffset)
    d.setHours(hour, minute, 0, 0)
    return d
  }

  for (let d = 0; d < 2; d++) {
    TOPICS.forEach((t, ti) => {
      const corr = `${t.corr}_${d}`
      const h = 9 + ti
      const lane = t.askee
      out.push(msg(at(d, h, 0), 'web-1', 'user', 'decision', corr,
        `今日议题 ${ti + 1}：${t.title}。请${t.askeeName}核实后回报。`, [t.askee],
        { decision: 'approve', topic: t.title, priority: 'P1' }, lane))
      out.push(msg(at(d, h, 4), 'web-1', 'user', 'ask_user', corr,
        `请确认：${t.title} 是否需要扩到 16 卡？`, [t.askee],
        { question: '是否需要扩到 16 卡？', options: ['扩', '不扩', '观望'] }, lane))
      out.push(msg(at(d, h, 11), t.askee, 'agent', 'answer', corr,
        '回禀：当前负载 72%，暂不扩容；若 TPM 连续两日 >160k 再申请。', ['web-1'],
        { answer: '暂不扩容', confidence: 0.86 }, lane))
      out.push(msg(at(d, h, 26), t.askee, 'agent', 'report', corr,
        t.report, ['web-1'], { summary: t.report, metrics: t.metrics }, lane))
      out.push(msg(at(d, h, 34), 'web-1', 'user', 'decision', corr,
        '准。继续按现网配置运行，明日朝会复盘。', [t.askee],
        { decision: 'approved', review_at: 'tomorrow' }, lane))
      out.push(msg(at(d, h, 41), t.askee, 'agent', 'action', corr,
        `${t.askeeName}已执行：更新看板并登记台账。`, ['web-1'],
        { action: 'update_dashboard', status: 'done' }, lane))
      // 往来条数按议题不同（6 / 7 / 8 条）：每条线程都正好 6 条会让人以为是写死的。
      if (ti % 3 !== 2) {
        out.push(msg(at(d, h, 46), t.askee, 'agent', 'report', corr,
          `${t.askeeName}补充回报：${t.followUp}`, ['web-1'],
          { summary: `${t.askeeName}补充回报：${t.followUp}` }, lane))
      }
      if (ti % 3 === 1) {
        out.push(msg(at(d, h, 49), 'web-1', 'user', 'action', corr,
          '按补充回报更新台账，收到。', [t.askee],
          { action: 'ack', status: 'done' }, lane))
      }
      out.push(msg(at(d, h, 55), 'web-1', 'user', 'text', '',
        '收到，散会。', []))
    })
    out.push(msg(at(d, 14, 0), 'agent:court.gongbu/main', 'agent', 'text', '',
      'B300-3 节点网卡抖动，已切流；观察 30 分钟。', ['agent:court.bingbu/main', 'web-1'],
      { severity: 'warn' }))
    out.push(msg(at(d, 14, 9), 'agent:court.bingbu/main', 'agent', 'text', '',
      '收到，已确认切流后没有新增对外端口。', ['agent:court.gongbu/main']))
  }
  // 最后一条「未回答的 ask_user」：用于展示待回答徽标与 blocked 状态。
  const last = new Date(out[out.length - 1].created_at)
  last.setMinutes(last.getMinutes() + 6)
  out.push(msg(last, 'web-1', 'user', 'ask_user', 'corr_pending',
    '工部：16 卡扩容的预算评估今晚能给吗？', ['agent:court.gongbu/main'],
    { question: '预算评估今晚能给吗？', pending: true }, 'agent:court.gongbu/main'))
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
      task: pending > 0 ? `等待处理 · ${lastSeen.get(mb.member_addr) ?? '—'}` : (lastSeen.get(mb.member_addr) ?? '暂无任务'),
      progress,
    } satisfies Lane
  })
}
