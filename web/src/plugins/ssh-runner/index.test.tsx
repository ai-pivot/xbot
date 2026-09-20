/**
 * xbot.ssh-runner 面板测试 —— 视图契约守护（VS Code Remote 式 SSH 管道模型）。
 *
 * 覆盖（与交付要求一一对应）：
 * 1) 新增流程调用顺序与参数：runner_create → probe → provision → connect；
 *    connect.connect_cmd === runner_create 返回的 command（原样透传）；
 *    provision 参数逐字对齐新契约（只有 ssh/name/download_base/install_dir，
 *    不再有 connect_cmd/service_mode），connection_mode 透传正确；
 * 2) job_status 轮询：state !== 'running' 时停止（假定时器推进 10s 无新调用）；
 * 3) 连接徽章三态：connected / reconnecting / disconnected（来自 status）；
 * 4) 「断开」调用 disconnect；「重连」先 disconnect 再 connect；
 * 5) autoConnect：auto_connect:true 的条目会被 connect（带 auto_connect:true）；
 *    已在连接的不重复 connect；connect 失败只提示、不阻塞面板；
 * 6) 删除流程：disconnect → deprovision（uninstall:false）→ runner_delete → targets 移除；
 * 7) 身份未就绪：不发会话相关 RPC、切换按钮禁用、面板不崩。
 * 另：pollConnectStatus / pollJobStatus 单元语义、targets 解析（camelCase 兼容）
 * 与序列化、未 activate 的兜底渲染。
 *
 * 插件模块从 window.React 取 React（不 import 宿主 react）—— 测试先注入真实
 * React，再动态 import 插件模块。⚠️ 本文件不得静态 import 插件模块的任何
 * 运行时值（静态 import 会被提升到 window.React 注入之前；shared 模块在模块
 * 初始化时读取 window.React）——类型 import（erased）除外。
 */
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import '@testing-library/jest-dom'
import * as RealReact from 'react'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import type { JobStatus, RemoteStatus } from '@/plugins/ssh-runner/shared'

;(window as unknown as { React: unknown }).React = RealReact

type PanelModule = typeof import('@/plugins/ssh-runner/index')
type SharedModule = typeof import('@/plugins/ssh-runner/shared')
type RpcMock = ReturnType<typeof vi.fn<(method: string, params: Record<string, unknown>) => Promise<unknown>>>

let mod: PanelModule
let shared: SharedModule
let Panel: PanelModule['default']

beforeAll(async () => {
  shared = await import('@/plugins/ssh-runner/shared')
  mod = await import('@/plugins/ssh-runner/index')
  Panel = mod.default
})

// ---------- 测试助手 ----------

function setPluginSession(chatID: string | null, channel = 'web'): void {
  const w = window as unknown as { __xbot_session__?: { channel: string; chatID: string } }
  if (chatID === null) delete w.__xbot_session__
  else w.__xbot_session__ = { channel, chatID }
}

const DEFAULT_CONFIG: Record<string, unknown> = {
  downloadBase: 'https://mirror.example/xbot',
  installDir: '/home/ubuntu/.local/bin',
  connectionMode: 'tunnel',
  targets: '[]',
}

interface CtxBundle {
  ctx: unknown
  call: RpcMock
  set: ReturnType<typeof vi.fn>
  store: Record<string, unknown>
}

function makeCtx(opts: {
  rpc: (method: string, params: Record<string, unknown>) => unknown
  config?: Record<string, unknown>
}): CtxBundle {
  const store: Record<string, unknown> = { ...DEFAULT_CONFIG, ...(opts.config ?? {}) }
  const call = vi.fn((method: string, params: Record<string, unknown>) => Promise.resolve(opts.rpc(method, params)))
  const set = vi.fn(async (key: string, value: unknown) => {
    store[key] = value
  })
  const get = vi.fn(async () => ({ ...store }))
  const onConfigChange = vi.fn(() => () => {})
  const ctx = { rpc: { call }, config: { get, set, onConfigChange } }
  return { ctx, call, set, store }
}

function runnerInfo(name: string, extra: Record<string, unknown> = {}) {
  return { name, mode: 'native', docker_image: '', workspace: '', online: true, created_at: '', ...extra }
}

/** 目标条目（配置 targets JSON 的元素——规范形态）。 */
function target(name: string, ssh: string, extra: Record<string, unknown> = {}) {
  return {
    name,
    ssh,
    install_dir: '/usr/local/bin',
    connection_mode: 'tunnel',
    auto_connect: false,
    added_at: '2026-09-17T00:00:00Z',
    ...extra,
  }
}

function targetsJSON(list: Array<ReturnType<typeof target>>): string {
  return JSON.stringify(list)
}

const PROBE = {
  os: 'linux',
  arch: 'amd64',
  user: 'ubuntu',
  is_root: false,
  has_systemd: true,
  has_curl: true,
  has_wget: false,
  installed_version: '',
  install_dir: '',
}

function jobStatus(state: JobStatus['state'], steps: JobStatus['steps'] = [], error = ''): JobStatus {
  return { state, steps, error }
}

function remoteStatus(over: Partial<RemoteStatus> = {}): RemoteStatus {
  return {
    installed_version: '',
    service_state: 'disconnected',
    detail: '',
    connected: false,
    connection_mode: '',
    restarts: 0,
    connected_at: '',
    remote_port: 0,
    last_error: '',
    ...over,
  }
}

/** flush 微任务链（fake timers 下不依赖 waitFor）。 */
async function flushAsync(rounds = 40): Promise<void> {
  await act(async () => {
    for (let i = 0; i < rounds; i += 1) await Promise.resolve()
  })
}

/** 取某方法的调用序号（-1 表示未调用）。 */
function callIndex(call: RpcMock, method: string): number {
  return call.mock.calls.findIndex((c) => c[0] === method)
}

function callsOf(call: RpcMock, method: string): Array<Record<string, unknown>> {
  return call.mock.calls.filter((c) => c[0] === method).map((c) => c[1] as Record<string, unknown>)
}

/** 打开添加向导并填好表单（默认不提交）。 */
async function openAddForm(name: string, ssh: string): Promise<void> {
  fireEvent.click(await screen.findByTestId('ssh-add-open'))
  fireEvent.change(screen.getByTestId('ssh-add-name'), { target: { value: name } })
  fireEvent.change(screen.getByTestId('ssh-add-ssh'), { target: { value: ssh } })
}

beforeEach(() => {
  setPluginSession(null)
})

afterEach(() => {
  setPluginSession(null)
})

// ---------- 1) 新增流程（顺序 + 参数） ----------

describe('新增机器流程', () => {
  it('runner_create → probe → provision（逐字对齐新参数）→ connect（connect_cmd 原样透传）→ status 直到 connected → 写 targets', async () => {
    const command = 'xbot-runner --server ws://host:8082/ws --token tok-1 --name gpu-box'
    let connectRequested = false
    const { ctx, call, set } = makeCtx({
      rpc: (method, params) => {
        if (method === 'runner_create') return { name: params.name, token: 'tok-1', command }
        if (method === 'xbot.ssh-runner.probe') return PROBE
        if (method === 'xbot.ssh-runner.provision') return { job_id: 'job-1' }
        if (method === 'xbot.ssh-runner.job_status') {
          return jobStatus('done', [{ name: 'install', ok: true, detail: '/usr/local/bin/xbot-runner' }])
        }
        if (method === 'xbot.ssh-runner.connect') {
          connectRequested = true
          return { connected: false, mode: 'tunnel', restarts: 0 }
        }
        if (method === 'xbot.ssh-runner.status') {
          return remoteStatus(
            connectRequested
              ? { connected: true, service_state: 'connected', connection_mode: 'tunnel', remote_port: 41234, restarts: 0 }
              : { connected: false, service_state: 'disconnected' },
          )
        }
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { targets: '[]' },
    })
    setPluginSession('chat-flow')
    mod.activate(ctx)
    render(<Panel />)

    await openAddForm('gpu-box', 'ssh ubuntu@10.0.0.5')
    // 默认连接方式从插件配置 connectionMode 取（tunnel）
    expect((screen.getByTestId('ssh-add-mode') as HTMLSelectElement).value).toBe('tunnel')
    fireEvent.click(screen.getByTestId('ssh-add-probe'))

    // probe 报告展示（用户确认后才安装）
    const confirmBtn = await screen.findByTestId('ssh-add-confirm')
    expect(screen.getByTestId('ssh-probe-report')).toHaveTextContent('ubuntu')
    fireEvent.click(confirmBtn)

    // 安装完成 → 自动进入连接等待 → status connected → 完成 + 写入 targets
    await screen.findByTestId('ssh-add-completed')
    await screen.findByTestId('ssh-target-gpu-box')

    // 调用顺序：runner_create → probe → provision → job_status → connect（status 轮询在后）
    const wizard = call.mock.calls
      .map((c) => String(c[0]))
      .filter((m) => m === 'runner_create' || m.startsWith('xbot.ssh-runner.'))
    expect(wizard.slice(0, 5)).toEqual([
      'runner_create',
      'xbot.ssh-runner.probe',
      'xbot.ssh-runner.provision',
      'xbot.ssh-runner.job_status',
      'xbot.ssh-runner.connect',
    ])

    // provision 参数逐字对齐新契约：只有 ssh/name/download_base/install_dir
    const provisionParams = callsOf(call, 'xbot.ssh-runner.provision')[0]
    expect(provisionParams).toEqual({
      ssh: 'ssh ubuntu@10.0.0.5',
      name: 'gpu-box',
      download_base: DEFAULT_CONFIG.downloadBase,
      install_dir: DEFAULT_CONFIG.installDir,
    })
    expect(Object.keys(provisionParams).sort()).toEqual(['download_base', 'install_dir', 'name', 'ssh'])

    // connect：connect_cmd 必须等于 runner_create 返回的 command；模式透传
    const connectParams = callsOf(call, 'xbot.ssh-runner.connect')[0]
    expect(connectParams).toEqual({
      ssh: 'ssh ubuntu@10.0.0.5',
      name: 'gpu-box',
      connect_cmd: command,
      install_dir: DEFAULT_CONFIG.installDir,
      connection_mode: 'tunnel',
      auto_connect: false,
    })

    // 成功后写入 targets（规范字段）
    expect(set).toHaveBeenCalledWith(
      'targets',
      expect.stringContaining('"gpu-box"'),
    )
    const written = JSON.parse(String(set.mock.calls[0]?.[1])) as Array<Record<string, unknown>>
    expect(written[0]).toMatchObject({ name: 'gpu-box', connection_mode: 'tunnel', auto_connect: false })
  })

  it('connectionMode 表单选择（含配置默认 direct）透传到 connect', async () => {
    const command = 'xbot-runner --server ws://host:8082/ws --token tok-2 --name lab'
    const { ctx, call } = makeCtx({
      rpc: (method, params) => {
        if (method === 'runner_create') return { name: params.name, token: 'tok-2', command }
        if (method === 'xbot.ssh-runner.probe') return PROBE
        if (method === 'xbot.ssh-runner.provision') return { job_id: 'job-2' }
        if (method === 'xbot.ssh-runner.job_status') return jobStatus('done')
        if (method === 'xbot.ssh-runner.connect') return { connected: false, mode: 'direct', restarts: 0 }
        if (method === 'xbot.ssh-runner.status') {
          return remoteStatus({ connected: true, service_state: 'connected', connection_mode: 'direct', restarts: 0 })
        }
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { connectionMode: 'direct', targets: '[]' },
    })
    setPluginSession('chat-mode')
    mod.activate(ctx)
    render(<Panel />)

    await openAddForm('lab', 'ssh root@lab.example')
    // 表单默认取插件配置的 connectionMode=direct…
    expect((screen.getByTestId('ssh-add-mode') as HTMLSelectElement).value).toBe('direct')
    // …用户改选 tunnel
    fireEvent.change(screen.getByTestId('ssh-add-mode'), { target: { value: 'tunnel' } })
    expect((screen.getByTestId('ssh-add-mode') as HTMLSelectElement).value).toBe('tunnel')

    fireEvent.click(screen.getByTestId('ssh-add-probe'))
    fireEvent.click(await screen.findByTestId('ssh-add-confirm'))
    await screen.findByTestId('ssh-add-completed')

    expect(callsOf(call, 'xbot.ssh-runner.connect')[0]).toMatchObject({
      name: 'lab',
      connect_cmd: command,
      connection_mode: 'tunnel',
    })
  })

  it('名称重复时拒绝探测（不发 runner_create）', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [] }
        if (method === 'xbot.ssh-runner.status') return remoteStatus()
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { targets: targetsJSON([target('gpu-box', 'ssh u@h')]) },
    })
    setPluginSession('chat-dup')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-gpu-box')
    await openAddForm('gpu-box', 'ssh other@host')
    fireEvent.click(screen.getByTestId('ssh-add-probe'))

    await screen.findByTestId('ssh-add-error')
    expect(screen.getByTestId('ssh-add-error')).toHaveTextContent('名称已存在')
    expect(call.mock.calls.filter((c) => c[0] === 'runner_create')).toHaveLength(0)
  })
})

// ---------- 2) job_status 轮询在终态停止（假定时器） ----------

describe('job_status 轮询', () => {
  it('state !== "running" 后不再轮询（假定时器推进 10s 无新调用）', async () => {
    vi.useFakeTimers()
    try {
      let jobCalls = 0
      const { ctx } = makeCtx({
        rpc: (method) => {
          if (method === 'runner_create') return { name: 'gpu-box', token: 't', command: 'xbot-runner --server ws://h --token t' }
          if (method === 'xbot.ssh-runner.probe') return PROBE
          if (method === 'xbot.ssh-runner.provision') return { job_id: 'job-9' }
          if (method === 'xbot.ssh-runner.job_status') {
            jobCalls += 1
            return jobCalls === 1
              ? jobStatus('running', [{ name: 'download', ok: true, detail: '已下载' }])
              : jobStatus('done', [
                  { name: 'download', ok: true, detail: '已下载' },
                  { name: 'ready', ok: true, detail: 'installed' },
                ])
          }
          if (method === 'xbot.ssh-runner.connect') return { connected: true, mode: 'tunnel', restarts: 0 }
          if (method === 'xbot.ssh-runner.status') {
            return remoteStatus({ connected: true, service_state: 'connected', connection_mode: 'tunnel' })
          }
          if (method === 'runner_list') return { runners: [] }
          if (method === 'runner_session_get') return { name: '', online: false }
          return {}
        },
      })
      setPluginSession('chat-job')
      mod.activate(ctx)
      render(<Panel />)
      await flushAsync()

      // ⚠️ 假定时器下不用 findByXxx（waitFor 依赖真实计时器）——同步点击 + flushAsync。
      fireEvent.click(screen.getByTestId('ssh-add-open'))
      fireEvent.change(screen.getByTestId('ssh-add-name'), { target: { value: 'gpu-box' } })
      fireEvent.change(screen.getByTestId('ssh-add-ssh'), { target: { value: 'ssh u@h' } })
      fireEvent.click(screen.getByTestId('ssh-add-probe'))
      await flushAsync()

      fireEvent.click(screen.getByTestId('ssh-add-confirm'))
      await flushAsync()
      // 第一次轮询：running（步骤进度可见）
      expect(jobCalls).toBe(1)
      expect(screen.getByTestId('ssh-job')).toHaveTextContent('正在安装')
      expect(screen.getAllByTestId('ssh-job-step')).toHaveLength(1)

      // 推进 1.5s → 第二次轮询：done（终态）→ 进入连接阶段
      await act(async () => {
        await vi.advanceTimersByTimeAsync(shared.JOB_POLL_INTERVAL_MS)
      })
      await flushAsync()
      expect(jobCalls).toBe(2)

      // 终态后再推进 10s：不得再调用（作业轮询已停止）
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10_000)
      })
      await flushAsync()
      expect(jobCalls).toBe(2)
    } finally {
      vi.useRealTimers()
    }
  })

  it('pollJobStatus：终态停止 + cancel() 后不再回调/排期', async () => {
    vi.useFakeTimers()
    try {
      let calls = 0
      const states: string[] = []
      const fetch1 = (): Promise<JobStatus> => {
        calls += 1
        return Promise.resolve(jobStatus(calls < 3 ? 'running' : 'done'))
      }
      shared.pollJobStatus('j1', fetch1, (s) => states.push(s.state), (m) => states.push(`err:${m}`))
      await vi.advanceTimersByTimeAsync(0)
      expect(calls).toBe(1)

      await vi.advanceTimersByTimeAsync(shared.JOB_POLL_INTERVAL_MS)
      expect(calls).toBe(2)
      await vi.advanceTimersByTimeAsync(shared.JOB_POLL_INTERVAL_MS)
      expect(calls).toBe(3)
      await vi.advanceTimersByTimeAsync(60_000)
      expect(calls).toBe(3) // 终态后不再轮询
      expect(states).toEqual(['running', 'running', 'done'])

      // cancel（组件卸载语义）：取消后连第一次 in-flight 都不再回调、不再排期
      let calls2 = 0
      const fetch2 = (): Promise<JobStatus> => {
        calls2 += 1
        return Promise.resolve(jobStatus('running'))
      }
      const poller = shared.pollJobStatus('j2', fetch2, () => {}, () => {})
      poller.cancel()
      await vi.advanceTimersByTimeAsync(60_000)
      expect(calls2).toBe(1)
    } finally {
      vi.useRealTimers()
    }
  })
})

// ---------- 3) 连接徽章三态 ----------

describe('连接状态徽章', () => {
  it('connected / reconnecting / disconnected 三态来自 status.service_state', async () => {
    const { ctx } = makeCtx({
      rpc: (method, params) => {
        if (method === 'xbot.ssh-runner.status') {
          const name = String(params.name)
          if (name === 'live') {
            return remoteStatus({
              connected: true,
              service_state: 'connected',
              connection_mode: 'tunnel',
              remote_port: 40123,
              restarts: 3,
              installed_version: 'xbot-runner v0.3.1',
            })
          }
          if (name === 'flaky') {
            return remoteStatus({ connected: false, service_state: 'reconnecting', connection_mode: 'tunnel', restarts: 2 })
          }
          return remoteStatus({ connected: false, service_state: 'disconnected' })
        }
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: {
        targets: targetsJSON([
          target('live', 'ssh u@live'),
          target('flaky', 'ssh u@flaky'),
          target('gone', 'ssh u@gone'),
        ]),
      },
    })
    setPluginSession('chat-badges')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-live')
    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-live')).toHaveTextContent('已连接')
    })
    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-flaky')).toHaveTextContent('重连中')
    })
    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-gone')).toHaveTextContent('未连接')
    })
    // restarts / tunnel 端口 / 已装版本 展示
    expect(screen.getByTestId('ssh-restarts-live')).toHaveTextContent('3')
    expect(screen.getByTestId('ssh-tunnel-port-live')).toHaveTextContent('40123')
    expect(screen.getByTestId('ssh-version-live')).toHaveTextContent('xbot-runner v0.3.1')
  })
})

// ---------- 4) 行操作：断开 / 重连 ----------

describe('行操作', () => {
  it('「断开」调用 disconnect{ssh,name}；「重连」先 disconnect 再 connect', async () => {
    const command = 'xbot-runner --server ws://host:8082/ws --token tok-r --name gpu-box'
    let connected = true
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [runnerInfo('gpu-box')] }
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'runner_create') return { name: 'gpu-box', token: 'tok-r', command }
        if (method === 'xbot.ssh-runner.disconnect') {
          connected = false
          return { connected: false }
        }
        if (method === 'xbot.ssh-runner.connect') {
          connected = true
          return { connected: false, mode: 'tunnel', restarts: 0 }
        }
        if (method === 'xbot.ssh-runner.status') {
          return connected
            ? remoteStatus({ connected: true, service_state: 'connected', connection_mode: 'tunnel', remote_port: 40001 })
            : remoteStatus({ connected: false, service_state: 'disconnected' })
        }
        return {}
      },
      config: { targets: targetsJSON([target('gpu-box', 'ssh ubuntu@10.0.0.5')]) },
    })
    setPluginSession('chat-ops')
    mod.activate(ctx)
    render(<Panel />)

    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-gpu-box')).toHaveTextContent('已连接')
    })

    // 断开
    fireEvent.click(screen.getByTestId('ssh-disconnect-gpu-box'))
    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('xbot.ssh-runner.disconnect', { ssh: 'ssh ubuntu@10.0.0.5', name: 'gpu-box' })
    })
    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-gpu-box')).toHaveTextContent('未连接')
    })

    // 重连（未连接时也可用）：先 disconnect 再 connect
    call.mockClear()
    fireEvent.click(screen.getByTestId('ssh-reconnect-gpu-box'))
    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('xbot.ssh-runner.connect', expect.objectContaining({ name: 'gpu-box' }))
    })
    const disconnectIdx = callIndex(call, 'xbot.ssh-runner.disconnect')
    const connectIdx = callIndex(call, 'xbot.ssh-runner.connect')
    expect(disconnectIdx).toBeGreaterThanOrEqual(0)
    expect(connectIdx).toBeGreaterThan(disconnectIdx)
    // 重连的 connect 参数：connect_cmd 来自 runner_create，模式透传
    expect(callsOf(call, 'xbot.ssh-runner.connect')[0]).toMatchObject({
      connect_cmd: command,
      install_dir: '/usr/local/bin',
      connection_mode: 'tunnel',
    })
    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-gpu-box')).toHaveTextContent('已连接')
    })
  })
})

// ---------- 5) autoConnect ----------

describe('autoConnect', () => {
  it('auto_connect:true 的条目挂载后自动 connect（auto_connect 透传 true）', async () => {
    const command = 'xbot-runner --server ws://host:8082/ws --token tok-a --name auto-box'
    let connectRequested = false
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'runner_create') return { name: 'auto-box', token: 'tok-a', command }
        if (method === 'xbot.ssh-runner.connect') {
          connectRequested = true
          return { connected: false, mode: 'tunnel', restarts: 0 }
        }
        if (method === 'xbot.ssh-runner.status') {
          return connectRequested
            ? remoteStatus({ connected: true, service_state: 'connected', connection_mode: 'tunnel' })
            : remoteStatus({ connected: false, service_state: 'disconnected' })
        }
        return {}
      },
      config: { targets: targetsJSON([target('auto-box', 'ssh u@auto', { auto_connect: true })]) },
    })
    setPluginSession('chat-auto')
    mod.activate(ctx)
    render(<Panel />)

    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('xbot.ssh-runner.connect', {
        ssh: 'ssh u@auto',
        name: 'auto-box',
        connect_cmd: command,
        install_dir: '/usr/local/bin',
        connection_mode: 'tunnel',
        auto_connect: true,
      })
    })
    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-auto-box')).toHaveTextContent('已连接')
    })
    expect(call.mock.calls.filter((c) => c[0] === 'xbot.ssh-runner.connect')).toHaveLength(1)
  })

  it('已在连接的目标不会被 autoConnect 重复 connect', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'xbot.ssh-runner.status') {
          return remoteStatus({ connected: true, service_state: 'connected', connection_mode: 'tunnel' })
        }
        return {}
      },
      config: { targets: targetsJSON([target('live', 'ssh u@live', { auto_connect: true })]) },
    })
    setPluginSession('chat-auto2')
    mod.activate(ctx)
    render(<Panel />)

    await waitFor(() => {
      expect(screen.getByTestId('ssh-conn-badge-live')).toHaveTextContent('已连接')
    })
    await new Promise((r) => setTimeout(r, 30))
    expect(call.mock.calls.filter((c) => c[0] === 'xbot.ssh-runner.connect')).toHaveLength(0)
    expect(call.mock.calls.filter((c) => c[0] === 'runner_create')).toHaveLength(0)
  })

  it('autoConnect 失败只提示（不阻塞面板渲染）', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'runner_create') return { name: 'broken', token: 'tok-b', command: 'xbot-runner --server ws://h --token tok-b' }
        if (method === 'xbot.ssh-runner.connect') throw new Error('ssh: connect to host unreachable')
        if (method === 'xbot.ssh-runner.status') return remoteStatus({ connected: false, service_state: 'disconnected' })
        return {}
      },
      config: {
        targets: targetsJSON([target('broken', 'ssh u@broken', { auto_connect: true }), target('other', 'ssh u@other')]),
      },
    })
    setPluginSession('chat-auto3')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-broken')
    // 失败被如实提示（不静默吞）
    await waitFor(() => {
      expect(screen.getByTestId('ssh-row-error-broken')).toHaveTextContent('自动连接失败')
    })
    expect(screen.getByTestId('ssh-row-error-broken')).toHaveTextContent('unreachable')
    // 面板其余部分照常渲染（不阻塞）
    expect(screen.getByTestId('ssh-runner-panel')).toBeInTheDocument()
    expect(screen.getByTestId('ssh-target-other')).toBeInTheDocument()
    expect(screen.getByTestId('ssh-add-open')).toBeInTheDocument()
    expect(call).toHaveBeenCalled()
  })
})

// ---------- 6) 删除流程 ----------

describe('删除机器', () => {
  it('disconnect → deprovision（uninstall:false）→ runner_delete → targets 移除', async () => {
    const { ctx, call, set } = makeCtx({
      rpc: (method) => {
        if (method === 'xbot.ssh-runner.status') return remoteStatus({ connected: true, service_state: 'connected' })
        if (method === 'xbot.ssh-runner.disconnect') return { connected: false }
        if (method === 'xbot.ssh-runner.deprovision') return { job_id: 'job-del' }
        if (method === 'xbot.ssh-runner.job_status') return jobStatus('done', [{ name: 'kill-old', ok: true, detail: '' }])
        if (method === 'runner_delete') return {}
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { targets: targetsJSON([target('gpu-box', 'ssh ubuntu@10.0.0.5')]) },
    })
    setPluginSession('chat-del')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-gpu-box')
    fireEvent.click(screen.getByTestId('ssh-delete-gpu-box'))
    fireEvent.click(screen.getByTestId('ssh-delete-confirm-gpu-box'))

    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('xbot.ssh-runner.deprovision', {
        ssh: 'ssh ubuntu@10.0.0.5',
        name: 'gpu-box',
        uninstall: false,
      })
    })
    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('runner_delete', { name: 'gpu-box' })
    })
    // 顺序：disconnect → deprovision → runner_delete
    const d1 = callIndex(call, 'xbot.ssh-runner.disconnect')
    const d2 = callIndex(call, 'xbot.ssh-runner.deprovision')
    const d3 = callIndex(call, 'runner_delete')
    expect(d1).toBeGreaterThanOrEqual(0)
    expect(d2).toBeGreaterThan(d1)
    expect(d3).toBeGreaterThan(d2)

    expect(set).toHaveBeenCalledWith('targets', '[]')
    await waitFor(() => {
      expect(screen.queryByTestId('ssh-target-gpu-box')).not.toBeInTheDocument()
    })
  })
})

// ---------- 7) 会话身份未就绪 ----------

describe('会话身份未就绪', () => {
  it('不发会话相关 RPC、切换按钮禁用、面板正常渲染', async () => {
    setPluginSession(null) // 无 __xbot_session__
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [] }
        if (method === 'xbot.ssh-runner.status') return remoteStatus()
        return {}
      },
      config: { targets: targetsJSON([target('box1', 'ssh user@host')]) },
    })
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-box1')
    // 等两圈事件循环后确认零会话 RPC（不伪造身份）
    await new Promise((r) => setTimeout(r, 20))
    expect(call.mock.calls.filter((c) => String(c[0]).startsWith('runner_session_'))).toHaveLength(0)
    expect(screen.getByTestId('ssh-switch-box1')).toBeDisabled()
    expect(screen.getByTestId('ssh-switch-local')).toBeDisabled()
    expect(screen.getByTestId('ssh-session-line')).toHaveTextContent('会话身份未就绪')
    // 非会话操作仍可用（连接按钮渲染）
    expect(screen.getByTestId('ssh-connect-box1')).toBeInTheDocument()
  })
})

// ---------- 8) pollConnectStatus 单元语义 ----------

describe('pollConnectStatus', () => {
  it('connected=true 即停止；cancel() 后不再回调/排期', async () => {
    vi.useFakeTimers()
    try {
      let calls = 0
      const seen: string[] = []
      const poller = shared.pollConnectStatus(
        () => {
          calls += 1
          return Promise.resolve(
            calls < 3
              ? remoteStatus({ connected: false, service_state: 'reconnecting' })
              : remoteStatus({ connected: true, service_state: 'connected' }),
          )
        },
        {
          onUpdate: (s) => seen.push(s.service_state),
          onConnected: () => seen.push('done'),
          onExhausted: () => seen.push('exhausted'),
          onError: (m) => seen.push(`err:${m}`),
        },
        { intervalMs: 100 },
      )
      await vi.advanceTimersByTimeAsync(0)
      expect(calls).toBe(1)
      await vi.advanceTimersByTimeAsync(100)
      await vi.advanceTimersByTimeAsync(100)
      expect(calls).toBe(3)
      expect(seen).toEqual(['reconnecting', 'reconnecting', 'done'])
      // connected 后不再排期
      await vi.advanceTimersByTimeAsync(60_000)
      expect(calls).toBe(3)

      // cancel 幂等
      let calls2 = 0
      const poller2 = shared.pollConnectStatus(
        () => {
          calls2 += 1
          return Promise.resolve(remoteStatus({ connected: false, service_state: 'reconnecting' }))
        },
        { onUpdate: () => {}, onConnected: () => {}, onExhausted: () => {}, onError: () => {} },
        { intervalMs: 100 },
      )
      poller2.cancel()
      await vi.advanceTimersByTimeAsync(60_000)
      expect(calls2).toBe(1)
      poller.cancel()
    } finally {
      vi.useRealTimers()
    }
  })

  it('达到 attempts 上限仍未连接 → onExhausted（停止轮询）', async () => {
    vi.useFakeTimers()
    try {
      let calls = 0
      const outcomes: string[] = []
      shared.pollConnectStatus(
        () => {
          calls += 1
          return Promise.resolve(remoteStatus({ connected: false, service_state: 'reconnecting' }))
        },
        {
          onUpdate: () => {},
          onConnected: () => outcomes.push('connected'),
          onExhausted: () => outcomes.push('exhausted'),
          onError: () => outcomes.push('error'),
        },
        { maxAttempts: 3, intervalMs: 100 },
      )
      await vi.advanceTimersByTimeAsync(0)
      await vi.advanceTimersByTimeAsync(100)
      await vi.advanceTimersByTimeAsync(100)
      expect(calls).toBe(3)
      expect(outcomes).toEqual(['exhausted'])
      // 上限后不再轮询
      await vi.advanceTimersByTimeAsync(60_000)
      expect(calls).toBe(3)
    } finally {
      vi.useRealTimers()
    }
  })

  it('单次查询失败只上报（onError），轮询继续', async () => {
    vi.useFakeTimers()
    try {
      let calls = 0
      const errors: string[] = []
      let connected = false
      shared.pollConnectStatus(
        () => {
          calls += 1
          if (calls === 1) return Promise.reject(new Error('ssh timeout'))
          return Promise.resolve(remoteStatus({ connected, service_state: connected ? 'connected' : 'reconnecting' }))
        },
        {
          onUpdate: () => {},
          onConnected: () => {
            connected = true
          },
          onExhausted: () => errors.push('exhausted'),
          onError: (m) => errors.push(m),
        },
        { maxAttempts: 4, intervalMs: 100 },
      )
      await vi.advanceTimersByTimeAsync(0)
      expect(errors).toEqual(['ssh timeout'])
      await vi.advanceTimersByTimeAsync(100)
      expect(calls).toBe(2) // 失败后仍在继续轮询
    } finally {
      vi.useRealTimers()
    }
  })
})

// ---------- 9) 配置 / targets 解析 ----------

describe('targets 解析与配置合并', () => {
  it('parseTargets 接受 snake_case 与 camelCase 两种形态；serializeTargets 输出规范字段', () => {
    const parsed = shared.parseTargets(
      JSON.stringify([
        { name: 'a', ssh: 'ssh u@a', connection_mode: 'direct', auto_connect: true },
        { name: 'b', ssh: 'ssh u@b', connectionMode: 'tunnel', autoConnect: 'true' },
        { name: 'broken' },
        { name: 'c', ssh: 'ssh u@c' },
      ]),
    )
    expect(parsed.map((x) => x.name)).toEqual(['a', 'b', 'c'])
    expect(parsed[0]).toMatchObject({ connection_mode: 'direct', auto_connect: true })
    expect(parsed[1]).toMatchObject({ connection_mode: 'tunnel', auto_connect: true })
    expect(parsed[2]).toMatchObject({ connection_mode: 'tunnel', auto_connect: false })

    const round = JSON.parse(shared.serializeTargets(parsed)) as Array<Record<string, unknown>>
    expect(Object.keys(round[0]).sort()).toEqual(['added_at', 'auto_connect', 'connection_mode', 'install_dir', 'name', 'ssh'])
    expect(round[0]).toMatchObject({ name: 'a', connection_mode: 'direct', auto_connect: true })
  })

  it('mergeConfigValues：connectionMode 缺省/非法回落 tunnel（serviceMode 已废弃）', () => {
    expect(shared.mergeConfigValues(null).connectionMode).toBe('tunnel')
    expect(shared.mergeConfigValues({ connectionMode: 'direct' }).connectionMode).toBe('direct')
    expect(shared.mergeConfigValues({ connectionMode: 'bogus' }).connectionMode).toBe('tunnel')
    // 旧键不再读取（废弃后不猜）
    expect(shared.mergeConfigValues({ serviceMode: 'nohup' }).connectionMode).toBe('tunnel')
  })

  it('connectionStateOf：connected 布尔与 service_state 的映射', () => {
    expect(shared.connectionStateOf(null)).toBe('disconnected')
    expect(shared.connectionStateOf(remoteStatus({ service_state: 'connected' }))).toBe('connected')
    expect(shared.connectionStateOf(remoteStatus({ connected: true, service_state: 'reconnecting' }))).toBe('connected')
    expect(shared.connectionStateOf(remoteStatus({ service_state: 'reconnecting' }))).toBe('reconnecting')
    expect(shared.connectionStateOf(remoteStatus())).toBe('disconnected')
  })
})

// ---------- 10) 未 activate 兜底 ----------

describe('未注入 ctx', () => {
  it('宿主未调用 activate 时渲染兜底提示且不崩', () => {
    mod.activate(undefined)
    render(<Panel />)
    expect(screen.getByTestId('ssh-runner-not-initialized')).toBeInTheDocument()
  })
})

// ---------- 11) 写入侧：失败的纳管不留孤儿（幽灵执行目标的根因） ----------

describe('纳管失败回滚（注册表不留孤儿）', () => {
  it('探针失败 ⇒ 回滚本次登记（runner_delete），且目标不被写入', async () => {
    const { ctx, call, store } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_registry') return { runners: [], orphans: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'runner_create') return { name: 'ghost-box', token: 'tok', command: 'xbot-runner --token tok' }
        if (method === 'xbot.ssh-runner.probe') throw new Error('ssh: connect to host refused')
        return {}
      },
    })
    setPluginSession('chat-rollback')
    mod.activate(ctx)
    render(<Panel />)
    await flushAsync()

    await openAddForm('ghost-box', 'ssh u@nope')
    fireEvent.click(screen.getByTestId('ssh-add-probe'))
    await flushAsync()

    // 流程失败（如实提示）
    expect(screen.getByTestId('ssh-add-error')).toHaveTextContent('connect to host refused')
    // 回滚：登记行被删（runner_create 先于机器存在，失败即回收）
    await waitFor(() => expect(callsOf(call, 'runner_delete')).toEqual([{ name: 'ghost-box' }]))
    // 不受管 ⇒ 不进 targets（机器从未成立）
    expect(String(store['targets'])).not.toContain('ghost-box')
  })

  it('机器真的连着（status.connected）⇒ 绝不回滚（不误删真机器）', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_registry') return { runners: [], orphans: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'runner_create') return { name: 'live-box', token: 'tok', command: 'cmd' }
        if (method === 'xbot.ssh-runner.status') return remoteStatus({ connected: true, service_state: 'connected' })
        if (method === 'xbot.ssh-runner.probe') throw new Error('probe hiccup')
        return {}
      },
    })
    setPluginSession('chat-rollback-live')
    mod.activate(ctx)
    render(<Panel />)
    await flushAsync()

    await openAddForm('live-box', 'ssh u@live')
    fireEvent.click(screen.getByTestId('ssh-add-probe'))
    await flushAsync()
    await waitFor(() => expect(screen.getByTestId('ssh-add-error')).toHaveTextContent('probe hiccup'))

    expect(callsOf(call, 'runner_delete')).toEqual([])
  })
})

// ---------- 12) 未纳管注册记录（遗留登记的唯一清理入口） ----------

describe('未纳管注册记录', () => {
  function registryPayload(): Record<string, unknown> {
    return {
      runners: [
        { ...runnerInfo('b300-4'), managed: true, state: 'managed', selectable: true, bound_count: 3 },
        { ...runnerInfo('default', { online: false }), managed: false, state: 'orphan', selectable: false, bound_count: 0 },
        { ...runnerInfo('ubuntu', { online: false }), managed: false, state: 'orphan', selectable: false, bound_count: 0 },
      ],
      orphans: ['default', 'ubuntu'],
    }
  }

  async function renderPanel(chatID: string) {
    const bundle = makeCtx({
      rpc: (method) => {
        if (method === 'runner_registry') return registryPayload()
        if (method === 'runner_session_get') return { name: '', online: false }
        if (method === 'xbot.ssh-runner.status') return remoteStatus()
        return {}
      },
      config: { targets: targetsJSON([target('b300-4', 'ssh u@b300-4')]) },
    })
    setPluginSession(chatID)
    mod.activate(bundle.ctx)
    render(<Panel />)
    await screen.findByTestId('ssh-unmanaged')
    return bundle
  }

  it('列出遗留行（含注册表里有、面板 targets 里没有的机器）+ 单条删除', async () => {
    const { call } = await renderPanel('chat-unmanaged')
    expect(screen.getByTestId('ssh-unmanaged-row-default')).toBeInTheDocument()
    expect(screen.getByTestId('ssh-unmanaged-row-ubuntu')).toBeInTheDocument()
    // 受管的机器不在遗留区
    expect(screen.queryByTestId('ssh-unmanaged-row-b300-4')).toBeNull()

    fireEvent.click(screen.getByTestId('ssh-unmanaged-delete-default'))
    await waitFor(() => expect(callsOf(call, 'runner_delete')).toEqual([{ name: 'default' }]))
  })

  it('一键清理：逐条删除全部遗留行（幂等删除，之后 reload 注册表）', async () => {
    const { call } = await renderPanel('chat-unmanaged-all')
    fireEvent.click(screen.getByTestId('ssh-unmanaged-cleanup'))
    await waitFor(() => expect(callsOf(call, 'runner_delete')).toEqual([{ name: 'default' }, { name: 'ubuntu' }]))
    // 清理后重新拉注册表（列表以后端为准）
    expect(callsOf(call, 'runner_registry').length).toBeGreaterThan(1)
  })
})
