/**
 * P1 验收（纯函数层）：组织树只保留「当前会话的直属子代理」。
 *
 * 背景：旧实现把 /api/session-tree 的 **全部 Sessions + OrphanSubAgents** 拍平渲染，
 * 生产上左栏是「1270 个部门/会话」（绝大多数是历史会话与 explore 一次性代理）。
 * 这里用生产形状的桩数据把口径钉死在测试里：默认可见 ≤ ORG_VISIBLE_LIMIT。
 */
import { afterEach, describe, expect, it, vi } from 'vitest'

import {
  ORG_VISIBLE_LIMIT,
  buildOrgTree,
  fetchSessionTree,
  flattenSessionTree,
  isEphemeralRole,
  resolveCurrentChatID,
  roleNamespace,
} from './sources'
import type { CourtSession } from './sources'

const CURRENT = 'chat_cur'

function sess(over: Partial<CourtSession> & { chatID: string }): CourtSession {
  const laneID = over.laneID ?? (over.isSubAgent ? `agent:${over.role ?? 'x'}/main` : `session:web:${over.chatID}`)
  const base: CourtSession = {
    key: `web:${over.chatID}/${laneID}`,
    laneID,
    name: over.name ?? over.role ?? over.chatID,
    channel: 'web',
    chatID: over.chatID,
    type: 'user',
    status: 'idle',
    running: false,
    lastActive: '2026-10-23T00:00:00Z',
    depth: 0,
    historical: false,
    rawStatus: 'idle',
    isCurrent: false,
    orphan: false,
    isSubAgent: false,
    synthetic: false,
  }
  const merged = { ...base, ...over }
  merged.type = merged.isSubAgent ? 'agent' : 'user'
  return merged
}

/** 生产形状：1 个当前会话 + 7 部门 + 3 项目角色 + 12 explore + 3 unloaded + 历史 + 孤儿。 */
function prodShaped(): CourtSession[] {
  const rows: CourtSession[] = [
    sess({ chatID: CURRENT, isCurrent: true, name: '首辅', lastActive: '2026-10-23T10:00:00Z' }),
  ]
  const depts = ['court.gongbu', 'court.hubu', 'court.hanlin', 'court.ducha', 'court.bingbu', 'court.sili', 'court.menxia']
  depts.forEach((role, i) => rows.push(sess({
    chatID: `agent/${role}/main`, laneID: `agent:${role}/main`, isSubAgent: true, role,
    parentChatID: CURRENT, lastActive: `2026-10-23T0${i}:00:00Z`,
  })))
  ;['proj.xbot.lead', 'proj.xbot.dev', 'proj.xbot.qa'].forEach((role, i) => rows.push(sess({
    chatID: `agent/${role}/main`, laneID: `agent:${role}/main`, isSubAgent: true, role,
    parentChatID: CURRENT, lastActive: `2026-10-23T0${i}:30:00Z`,
  })))
  for (let i = 0; i < 12; i++) {
    rows.push(sess({
      chatID: `agent/explore/main#${i}`, laneID: `agent:explore/main#${i}`, isSubAgent: true,
      role: 'explore', name: `explore/x${i}`, parentChatID: CURRENT, lastActive: '2026-10-22T00:00:00Z',
    }))
  }
  for (let i = 0; i < 3; i++) {
    rows.push(sess({
      chatID: `agent/explore/unloaded#${i}`, laneID: `agent:explore/unloaded#${i}`, isSubAgent: true,
      role: 'explore', name: `explore/u${i}`, parentChatID: CURRENT, rawStatus: 'unloaded',
    }))
  }
  for (let i = 0; i < 120; i++) {
    rows.push(sess({ chatID: `chat_hist_${i}`, name: `历史会话 ${i}`, lastActive: '2026-09-01T00:00:00Z' }))
  }
  for (let i = 0; i < 61; i++) {
    rows.push(sess({
      chatID: `agent/explore/gone#${i}`, laneID: `agent:explore/gone#${i}`, isSubAgent: true,
      role: 'explore', name: `explore/g${i}`, parentChatID: `chat_deleted_${i}`,
      orphan: true, historical: true,
    }))
  }
  return rows
}

describe('buildOrgTree（P1：组织树数据源）', () => {
  it('生产形状下默认只出「当前会话的直属子代理」，且 ≤ 20 条', () => {
    const model = buildOrgTree(prodShaped())
    expect(model.currentChatID).toBe(CURRENT)
    expect(model.direct.length).toBe(10) // 7 部门 + 3 项目角色
    expect(model.direct.length).toBeLessThanOrEqual(ORG_VISIBLE_LIMIT)
    expect(model.direct.map((s) => s.role)).toEqual(expect.arrayContaining(['court.gongbu', 'proj.xbot.dev']))
    // 直属里绝不能出现一次性 explore / 历史会话 / 孤儿
    expect(model.direct.some((s) => isEphemeralRole(s.role))).toBe(false)
    expect(model.direct.some((s) => s.historical || s.orphan)).toBe(false)
  })

  it('unloaded（已卸载的僵尸子代理）一行都不渲染：直属与历史里都不出现', () => {
    const model = buildOrgTree(prodShaped())
    const ids = [...model.direct, ...model.history].map((s) => s.laneID)
    expect(ids.some((id) => id.includes('unloaded'))).toBe(false)
  })

  it('历史（历史会话 / 孤儿 / explore）默认折叠在 history 里，不丢条目', () => {
    const model = buildOrgTree(prodShaped())
    expect(model.history.length).toBe(120 + 61 + 12)
    expect(model.history.every((s) => s.chatID !== CURRENT)).toBe(true)
    // 需要拉消息的只有「当前会话 + 直属」——历史不打后端
    expect(model.scan.map((s) => s.chatID)).toContain(CURRENT)
    expect(model.scan.every((s) => s.chatID === CURRENT || s.parentChatID === CURRENT)).toBe(true)
  })

  it('直属超过上限时截断到 20，多出的进历史并给出 overflow 计数（不静默丢弃）', () => {
    const rows = [sess({ chatID: CURRENT, isCurrent: true })]
    for (let i = 0; i < 30; i++) {
      rows.push(sess({
        chatID: `agent/dept${i}/main`, laneID: `agent:dept${i}/main`, isSubAgent: true,
        role: `court.dept${i}`, parentChatID: CURRENT,
        lastActive: `2026-10-23T00:${String(i).padStart(2, '0')}:00Z`,
      }))
    }
    const model = buildOrgTree(rows)
    expect(model.direct.length).toBe(ORG_VISIBLE_LIMIT)
    expect(model.overflow).toBe(10)
    expect(model.history.length).toBe(10)
    // 保留的是最近活跃的那批
    expect(model.direct[0].role).toBe('court.dept29')
  })

  it('按 role 命名空间分组：court.* / proj.xbot.* 各自成组，组内按最近活跃排序', () => {
    const model = buildOrgTree(prodShaped())
    expect(model.groups.map((g) => g.key)).toEqual(['court', 'proj.xbot'])
    expect(model.groups[0].laneIDs.length).toBe(7)
    expect(model.groups[1].laneIDs.length).toBe(3)
    expect(model.groups[0].label).toBe('court')
  })

  it('定不出当前会话时返回 currentChatID=null（调用方退回平铺，不静默清空左栏）', () => {
    expect(buildOrgTree([]).currentChatID).toBeNull()
    expect(buildOrgTree([], { currentChatID: 'explicit' }).currentChatID).toBe('explicit')
  })

  it('explore 角色识别 / 命名空间切分', () => {
    expect(isEphemeralRole('explore')).toBe(true)
    expect(isEphemeralRole('explore.x')).toBe(true)
    expect(isEphemeralRole('explorer')).toBe(false)
    expect(isEphemeralRole('court.gongbu')).toBe(false)
    expect(roleNamespace('court.gongbu')).toBe('court')
    expect(roleNamespace('proj.xbot.dev')).toBe('proj.xbot')
    expect(roleNamespace('tester')).toBe('tester')
  })
})

describe('flattenSessionTree（父子关系与状态透传）', () => {
  it('透传 parentChatID / rawStatus / isSubAgent，孤儿带 orphan 标记', () => {
    const rows = flattenSessionTree({
      sessions: [{
        chatID: CURRENT, channel: 'web', label: '首辅', lastActive: '', preview: '',
        status: 'idle', isCurrent: true, type: 'main',
        children: [{
          chatID: 'agent/court.gongbu/main', channel: 'agent', label: 'court.gongbu', lastActive: '',
          preview: '', status: 'idle', isCurrent: false, type: 'agent', role: 'court.gongbu',
          instance: 'main', parentChatID: CURRENT,
        }],
      }],
      orphan_subagents: [{
        chatID: 'agent/explore/gone/main', channel: 'agent', label: 'explore', lastActive: '',
        preview: '', status: 'idle', isCurrent: false, type: 'agent', role: 'explore',
        parentChatID: 'chat_deleted', historical: true,
      }],
    })
    const main = rows.find((r) => r.chatID === CURRENT)!
    expect(main.isSubAgent).toBe(false)
    expect(main.isCurrent).toBe(true)
    const dept = rows.find((r) => r.role === 'court.gongbu')!
    expect(dept.parentChatID).toBe(CURRENT)
    expect(dept.isSubAgent).toBe(true)
    expect(dept.rawStatus).toBe('idle')
    const orphan = rows.find((r) => r.orphan)!
    expect(orphan.laneID).toContain('explore')
    expect(orphan.historical).toBe(true)
  })
})

describe('当前会话的解析（通信视图开在部门会话里也成立）', () => {
  it('is_current 打在子代理会话上时，用它自己当「当前会话」', () => {
    const rows = [
      sess({ chatID: 'chat_main', name: '首辅' }),
      sess({
        chatID: 'agent/court.gongbu/main', laneID: 'agent:court.gongbu/main', isSubAgent: true,
        role: 'court.gongbu', parentChatID: 'chat_main', isCurrent: true,
      }),
      sess({
        chatID: 'agent/tester/main', laneID: 'agent:tester/main', isSubAgent: true,
        role: 'tester', parentChatID: 'agent/court.gongbu/main',
      }),
    ]
    expect(resolveCurrentChatID(rows)).toBe('agent/court.gongbu/main')
    const model = buildOrgTree(rows)
    expect(model.currentChat?.role).toBe('court.gongbu')
    expect(model.direct.map((s) => s.role)).toEqual(['tester'])
  })
})

describe('fetchSessionTree → buildOrgTree（真实归一化链路）', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('孤儿被后端归一化成「合成父会话 + 子代理」时不重复、不把占位会话当部门', async () => {
    const entity = (role: string, parent: string, over: Record<string, unknown> = {}) => {
      const instance = (over.instance as string | undefined) ?? 'main'
      return {
        chat_id: `agent/${role}/main`, channel: 'agent',
        full_key: `web:${parent}/${role}${instance === 'main' ? '' : `:${instance}`}`,
        label: role, role, instance, last_active: '2026-10-23T00:00:00Z', preview: '',
        running: false, status: 'idle', parent_chat_id: parent, parent_channel: 'web', ...over,
      }
    }
    const payload = {
      sessions: [{
        chat_id: CURRENT, channel: 'web', label: '首辅', last_active: '2026-10-23T00:00:00Z',
        preview: '', running: true, status: 'running', is_current: true,
        children: [
          entity('court.gongbu', CURRENT),
          entity('explore', CURRENT, { chat_id: 'agent/explore/main#1', instance: 'x1' }),
          entity('explore', CURRENT, { chat_id: 'agent/explore/unloaded#1', instance: 'u1', status: 'unloaded' }),
        ],
      }],
      orphan_subagents: [
        entity('explore', 'chat_deleted_1', { chat_id: 'agent/explore/gone#1', instance: 'g1', historical: true }),
        entity('explore', 'chat_deleted_2', { chat_id: 'agent/explore/gone#2', instance: 'g2', historical: true }),
      ],
    }
    vi.stubGlobal('fetch', vi.fn(async () => new Response(
      JSON.stringify({ ok: true, data: payload, error: null }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    )))

    const rows = await fetchSessionTree()
    const model = buildOrgTree(rows)
    expect(model.currentChatID).toBe(CURRENT)
    expect(model.direct.map((s) => s.role)).toEqual(['court.gongbu'])
    // 历史 = 1 个 explore + 2 个孤儿子代理；合成父会话与 unloaded 都不在里面
    expect(model.history.length).toBe(3)
    expect(model.history.some((s) => s.synthetic)).toBe(false)
    expect(model.history.some((s) => s.rawStatus === 'unloaded')).toBe(false)
  })
})
