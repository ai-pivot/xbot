/**
 * xbot.ssh-runner —— 底部 bar（当前执行目标 + 快速切换）测试。
 *
 * 与 index.test.tsx 同一套 mock 方式：插件模块从 window.React 取 React，测试先注入
 * 真实 React 再**动态** import 插件模块（静态 import 会被提升到注入之前）。
 *
 * 覆盖：
 * 1) 未绑定 ⇒ 显示「本机」（data-status=local）；
 * 2) 已绑定某 runner ⇒ 显示其名，且**离线/在线可区分**（data-status=offline/online）；
 * 3) 点击弹出选择器（本机 + 全部 runner），选择 ⇒ runner_session_set（channel/chat_id/name）
 *    + 乐观立即更新 + 成功后收起；
 * 4) 切换失败 ⇒ 回滚到原值 + 显示错误 + 选择器保持打开；
 * 5) 会话身份未就绪 ⇒ 不发任何会话相关 RPC、标签为 '—'、点击不弹选择器；
 * 6) activate() 注册桌面底栏徽章（zone='bottom' + badgeRender）——桌面 rail 的唯一入口。
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import * as RealReact from 'react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

;(window as unknown as { React: unknown }).React = RealReact

type BarModule = typeof import('@/plugins/ssh-runner/bar')
type IndexModule = typeof import('@/plugins/ssh-runner/index')
type SharedModule = typeof import('@/plugins/ssh-runner/shared')
type RpcMock = ReturnType<typeof vi.fn<(method: string, params: Record<string, unknown>) => Promise<unknown>>>

let bar: BarModule
let shared: SharedModule
let indexMod: IndexModule

beforeAll(async () => {
  shared = await import('@/plugins/ssh-runner/shared')
  bar = await import('@/plugins/ssh-runner/bar')
  indexMod = await import('@/plugins/ssh-runner/index')
})

// ---------- 测试助手 ----------

function setPluginSession(chatID: string | null, channel = 'web'): void {
  const w = window as unknown as { __xbot_session__?: { channel: string; chatID: string } }
  if (chatID === null) delete w.__xbot_session__
  else w.__xbot_session__ = { channel, chatID }
}

function runnerInfo(name: string, online = true) {
  return { name, mode: 'native', docker_image: '', workspace: '', online, created_at: '', version: '' }
}

/** 安装 ctx（只用到 rpc.i18n 缺省 ⇒ 文案走中文 fallback，断言与宿主语言无关）。 */
function makeCtx(rpc: (method: string, params: Record<string, unknown>) => unknown): { call: RpcMock } {
  const call = vi.fn((method: string, params: Record<string, unknown>) => Promise.resolve(rpc(method, params)))
  shared.setCtx({ rpc: { call } })
  return { call }
}

function methodsOf(call: RpcMock): string[] {
  return call.mock.calls.map((c) => String(c[0]))
}

afterEach(() => {
  setPluginSession(null)
})

// ---------- 1) 未绑定 ⇒ 本机 ----------

describe('底部 bar 显示当前执行目标', () => {
  it('未绑定 ⇒ 显示「本机」（local）', async () => {
    setPluginSession('chat-bar-local')
    const { call } = makeCtx((method) => {
      if (method === 'runner_session_get') return { name: '', online: false }
      if (method === 'runner_list') return { runners: [runnerInfo('gpu-01')] }
      return {}
    })
    render(<bar.default />)

    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('本机'))
    expect(screen.getByTestId('ssh-runner-bar')).toHaveAttribute('data-status', 'local')
    // 身份就绪 ⇒ 绑定查询带正确的 channel/chat_id
    expect(call).toHaveBeenCalledWith('runner_session_get', { channel: 'web', chat_id: 'chat-bar-local' })
  })

  it('已绑定某 runner ⇒ 显示其名；离线与在线可区分（视觉：data-status + 状态文案）', async () => {
    setPluginSession('chat-bar-bound')
    makeCtx((method) => {
      if (method === 'runner_session_get') return { name: 'gpu-01', online: false }
      if (method === 'runner_list') return { runners: [runnerInfo('gpu-01', false)] }
      return {}
    })
    const { unmount } = render(<bar.default />)
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('gpu-01'))
    expect(screen.getByTestId('ssh-runner-bar')).toHaveAttribute('data-status', 'offline')
    unmount()

    // 同一 runner 在线 ⇒ online（两态必须可区分）
    makeCtx((method) => {
      if (method === 'runner_session_get') return { name: 'gpu-01', online: true }
      if (method === 'runner_list') return { runners: [runnerInfo('gpu-01', true)] }
      return {}
    })
    render(<bar.default />)
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar')).toHaveAttribute('data-status', 'online'))
  })
})

// ---------- 2) 点击 ⇒ 选择器 ⇒ runner_session_set ----------

describe('点击选择目标', () => {
  it('点击列出全部 runner + 本机；选择 ⇒ runner_session_set + 乐观立即更新 + 成功收起', async () => {
    setPluginSession('chat-bar-switch')
    // mock 服务器状态：get 反映 set 的结果（组件的权威回读依赖它）。
    let bound = ''
    const { call } = makeCtx((method, params) => {
      if (method === 'runner_session_get') return { name: bound, online: bound !== '' }
      if (method === 'runner_list') return { runners: [runnerInfo('gpu-01'), runnerInfo('lab-2', false)] }
      if (method === 'runner_session_set') {
        bound = String(params.name ?? '')
        return {}
      }
      return {}
    })
    render(<bar.default />)
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('本机'))

    // 尚未点击 ⇒ 没有选择器
    expect(screen.queryByTestId('ssh-runner-bar-picker')).toBeNull()
    fireEvent.click(screen.getByTestId('ssh-runner-bar'))

    // 列出：本机 + gpu-01 + lab-2；当前绑定（本机）打勾
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-option-local')).toBeInTheDocument())
    expect(screen.getByTestId('ssh-runner-bar-option-gpu-01')).toBeInTheDocument()
    expect(screen.getByTestId('ssh-runner-bar-option-lab-2')).toBeInTheDocument()
    expect(screen.getByTestId('ssh-runner-bar-option-local')).toHaveAttribute('data-active', 'true')
    // 在线/离线标记来自 runner_list
    expect(screen.getByTestId('ssh-runner-bar-option-state-lab-2')).toHaveTextContent('离线')

    fireEvent.click(screen.getByTestId('ssh-runner-bar-option-gpu-01'))

    // 乐观：点击后立即更新（不等 RPC 回包）
    expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('gpu-01')
    await waitFor(() => expect(call).toHaveBeenCalledWith('runner_session_set', { channel: 'web', chat_id: 'chat-bar-switch', name: 'gpu-01' }))
    // 成功 ⇒ 选择器收起，标签保持新值
    await waitFor(() => expect(screen.queryByTestId('ssh-runner-bar-picker')).toBeNull())
    expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('gpu-01')
  })

  it('选择「本机」⇒ runner_session_set name 为空串（切回本机）', async () => {
    setPluginSession('chat-bar-back')
    let bound = 'gpu-01'
    const { call } = makeCtx((method, params) => {
      if (method === 'runner_session_get') return { name: bound, online: bound !== '' }
      if (method === 'runner_list') return { runners: [runnerInfo('gpu-01')] }
      if (method === 'runner_session_set') {
        bound = String(params.name ?? '')
        return {}
      }
      return {}
    })
    render(<bar.default />)
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('gpu-01'))

    fireEvent.click(screen.getByTestId('ssh-runner-bar'))
    fireEvent.click(await screen.findByTestId('ssh-runner-bar-option-local'))
    await waitFor(() => expect(call).toHaveBeenCalledWith('runner_session_set', { channel: 'web', chat_id: 'chat-bar-back', name: '' }))
    expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('本机')
  })
})

// ---------- 3) 失败回滚 ----------

describe('切换失败回滚', () => {
  it('runner_session_set 失败 ⇒ 标签回滚到原值 + 错误可见 + 选择器保持打开', async () => {
    setPluginSession('chat-bar-fail')
    makeCtx((method) => {
      if (method === 'runner_session_get') return { name: '', online: false }
      if (method === 'runner_list') return { runners: [runnerInfo('gpu-01')] }
      if (method === 'runner_session_set') throw new Error('switch denied')
      return {}
    })
    render(<bar.default />)
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('本机'))

    fireEvent.click(screen.getByTestId('ssh-runner-bar'))
    fireEvent.click(await screen.findByTestId('ssh-runner-bar-option-gpu-01'))

    // 回滚：乐观值不得残留
    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('本机'))
    expect(screen.getByTestId('ssh-runner-bar')).toHaveAttribute('data-status', 'local')
    // 错误显示在选择器里，且选择器保持打开（用户可重选/看到原因）
    expect(screen.getByTestId('ssh-runner-bar-picker')).toBeInTheDocument()
    expect(screen.getByTestId('ssh-runner-bar-error')).toHaveTextContent('switch denied')
  })
})

// ---------- 4) 身份未就绪 ----------

describe('会话身份未就绪', () => {
  it('不发会话相关 RPC、标签为 —、点击不弹选择器', async () => {
    setPluginSession(null)
    const { call } = makeCtx(() => ({}))
    render(<bar.default />)

    await waitFor(() => expect(screen.getByTestId('ssh-runner-bar')).toHaveAttribute('data-status', 'unavailable'))
    expect(screen.getByTestId('ssh-runner-bar-label')).toHaveTextContent('—')
    fireEvent.click(screen.getByTestId('ssh-runner-bar'))
    expect(screen.queryByTestId('ssh-runner-bar-picker')).toBeNull()
    // 身份未知 ⇒ 绝不伪造 chatID 发 RPC（会把切换写到错误目标上）
    expect(methodsOf(call).filter((m) => m === 'runner_session_get' || m === 'runner_session_set' || m === 'runner_list')).toEqual([])
  })
})

// ---------- 5) activate 注册桌面底栏徽章 ----------

describe('桌面底栏徽章注册', () => {
  it('activate() 注册 zone=bottom 的徽章面板（badgeRender 即 bar 组件，无面板主体）', () => {
    const register = vi.fn((_def: unknown) => () => {})
    const call = vi.fn(() => Promise.resolve({}))
    indexMod.activate({ rpc: { call }, config: { get: async () => ({}), set: async () => {}, onConfigChange: () => () => {} }, panels: { register } })

    expect(register).toHaveBeenCalledTimes(1)
    const def = register.mock.calls[0][0] as {
      id: string
      location: { zone: string; order: number }
      render: () => unknown
      badgeRender: () => unknown
    }
    expect(def.id).toBe('xbot.ssh-runner.bar')
    expect(def.location).toEqual({ zone: 'bottom', order: 0 })
    // 徽章面板没有面板主体（主体即徽章）——与 buildPanelDefs 的独立徽章面板同形
    expect(def.render()).toBeNull()
    expect(def.badgeRender()).toBeTruthy()
  })

  it('无 panels 能力（未声明 ui 权限）⇒ 静默跳过，不抛错', () => {
    const call = vi.fn(() => Promise.resolve({}))
    expect(() => indexMod.activate({ rpc: { call } })).not.toThrow()
  })
})
