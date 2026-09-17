/**
 * xbot.ssh-runner 面板测试 —— 视图契约守护。
 *
 * 覆盖（与交付要求一一对应）：
 * 1) 列表渲染：config.targets → 名称 / 脱敏 SSH / 在线状态 / 当前目标；
 * 2) 新增流程：runner_create → probe → provision 的调用顺序与参数
 *    （connect_cmd 必须等于 runner_create 返回的 command，原样透传）；
 * 3) job_status 轮询：state !== 'running' 时停止（假定时器断言不再调用）；
 * 4) 切换：runner_session_set 收到 { channel, chat_id, name }；「切回本机」传 name:''；
 * 5) 身份未就绪：不发会话相关 RPC、切换按钮禁用、面板不崩。
 * 另：删除链路（deprovision → job → runner_delete → targets 移除）、
 * pollJobStatus 单元语义（终态停止 + cancel）、未 activate 的兜底渲染。
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

import type { JobStatus } from '@/plugins/ssh-runner/shared'

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
  serviceMode: 'nohup',
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

function runnerInfo(name: string, online: boolean) {
  return { name, mode: 'native', docker_image: '', workspace: '', online, created_at: '' }
}

function target(name: string, ssh: string) {
  return { name, ssh, install_dir: '/usr/local/bin', service_mode: 'systemd', added_at: '2026-09-17T00:00:00Z' }
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

/** flush 微任务链（fake timers 下不依赖 waitFor）。 */
async function flushAsync(rounds = 30): Promise<void> {
  await act(async () => {
    for (let i = 0; i < rounds; i += 1) await Promise.resolve()
  })
}

beforeEach(() => {
  setPluginSession(null)
})

afterEach(() => {
  setPluginSession(null)
})

// ---------- 1) 列表渲染 ----------

describe('目标列表', () => {
  it('从 config.targets 渲染名称 / 脱敏 SSH / 在线状态 / 当前目标', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [runnerInfo('gpu-box', true), runnerInfo('lab', false)] }
        if (method === 'runner_session_get') return { name: 'lab', online: false }
        return {}
      },
      config: {
        targets: JSON.stringify([target('gpu-box', 'ssh ubuntu@10.0.0.5 -p 22'), target('lab', 'ssh root@lab.example')]),
      },
    })
    setPluginSession('chat-1')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-gpu-box')
    expect(screen.getByText('gpu-box')).toBeInTheDocument()
    // SSH 脱敏显示：只展示 user@host（不含端口等参数）
    expect(screen.getByText('ubuntu@10.0.0.5')).toBeInTheDocument()
    expect(screen.getByText('root@lab.example')).toBeInTheDocument()
    // 在线状态来自 runner_list 同名项
    expect(screen.getByText('在线')).toBeInTheDocument()
    expect(screen.getByText('离线')).toBeInTheDocument()
    // 当前会话目标（runner_session_get）
    await waitFor(() => {
      expect(screen.getByTestId('ssh-session-line')).toHaveTextContent('当前目标：lab')
    })
    expect(screen.getByTestId('ssh-target-lab')).toHaveTextContent('当前')
    // 当前目标行不再显示「切换」按钮
    expect(screen.queryByTestId('ssh-switch-lab')).not.toBeInTheDocument()
    expect(call).toHaveBeenCalledWith('runner_session_get', { channel: 'web', chat_id: 'chat-1' })
  })
})

// ---------- 2) 新增流程（顺序 + 参数） ----------

describe('新增机器流程', () => {
  it('runner_create → probe → provision，connect_cmd 原样透传，成功后写入 targets', async () => {
    const command = '--server ws://host:8082/ws --token tok-1'
    const { ctx, call, set } = makeCtx({
      rpc: (method, params) => {
        if (method === 'runner_create') return { name: params.name, token: 'tok-1', command }
        if (method === 'xbot.ssh-runner.probe') return PROBE
        if (method === 'xbot.ssh-runner.provision') return { job_id: 'job-1' }
        if (method === 'xbot.ssh-runner.job_status') return jobStatus('done', [{ name: 'upload', ok: true, detail: 'ok' }])
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
    })
    setPluginSession('chat-7')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-add-open')
    fireEvent.click(screen.getByTestId('ssh-add-open'))
    fireEvent.change(screen.getByTestId('ssh-add-name'), { target: { value: 'gpu-box' } })
    fireEvent.change(screen.getByTestId('ssh-add-ssh'), { target: { value: 'ssh ubuntu@10.0.0.5' } })
    fireEvent.click(screen.getByTestId('ssh-add-probe'))

    // probe 报告展示（用户确认后才 provision）
    const confirmBtn = await screen.findByTestId('ssh-add-confirm')
    expect(screen.getByTestId('ssh-probe-report')).toHaveTextContent('ubuntu')
    fireEvent.click(confirmBtn)

    // 轮询（job 立即 done）→ 写入 targets → 完成态 + 列表出现
    await screen.findByTestId('ssh-add-completed')
    await screen.findByTestId('ssh-target-gpu-box')

    // 调用顺序：runner_create → probe → provision → job_status（无多余向导调用）
    const wizardCalls = call.mock.calls
      .map((c) => c[0])
      .filter((m) => m === 'runner_create' || m.startsWith('xbot.ssh-runner.'))
    expect(wizardCalls).toEqual(['runner_create', 'xbot.ssh-runner.probe', 'xbot.ssh-runner.provision', 'xbot.ssh-runner.job_status'])

    expect(call).toHaveBeenCalledWith('runner_create', { name: 'gpu-box' })
    expect(call).toHaveBeenCalledWith('xbot.ssh-runner.probe', { ssh: 'ssh ubuntu@10.0.0.5' })
    expect(call).toHaveBeenCalledWith('xbot.ssh-runner.provision', {
      ssh: 'ssh ubuntu@10.0.0.5',
      name: 'gpu-box',
      connect_cmd: command, // 必须等于 runner_create 返回的 command（不自己拼 URL）
      download_base: DEFAULT_CONFIG.downloadBase,
      install_dir: DEFAULT_CONFIG.installDir,
      service_mode: DEFAULT_CONFIG.serviceMode,
    })
    // 成功后目标写入插件配置 targets
    expect(set).toHaveBeenCalledWith('targets', expect.stringContaining('"gpu-box"'))
  })

  it('名称重复时拒绝探测（不发 runner_create）', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { targets: JSON.stringify([target('gpu-box', 'ssh u@h')]) },
    })
    setPluginSession('chat-7')
    mod.activate(ctx)
    render(<Panel />)

    await screen.findByTestId('ssh-target-gpu-box')
    fireEvent.click(screen.getByTestId('ssh-add-open'))
    fireEvent.change(screen.getByTestId('ssh-add-name'), { target: { value: 'gpu-box' } })
    fireEvent.change(screen.getByTestId('ssh-add-ssh'), { target: { value: 'ssh other@host' } })
    fireEvent.click(screen.getByTestId('ssh-add-probe'))

    await screen.findByTestId('ssh-add-error')
    expect(screen.getByTestId('ssh-add-error')).toHaveTextContent('名称已存在')
    expect(call.mock.calls.filter((c) => c[0] === 'runner_create')).toHaveLength(0)
  })
})

// ---------- 3) job_status 轮询在终态停止（假定时器） ----------

describe('job_status 轮询', () => {
  it('state !== "running" 后不再轮询（假定时器推进 10s 无新调用）', async () => {
    vi.useFakeTimers()
    try {
      let jobCalls = 0
      const { ctx } = makeCtx({
        rpc: (method) => {
          if (method === 'runner_create') return { name: 'gpu-box', token: 't', command: '--server ws://h --token t' }
          if (method === 'xbot.ssh-runner.probe') return PROBE
          if (method === 'xbot.ssh-runner.provision') return { job_id: 'job-9' }
          if (method === 'xbot.ssh-runner.job_status') {
            jobCalls += 1
            return jobCalls === 1
              ? jobStatus('running', [{ name: 'upload', ok: true, detail: '已上传' }])
              : jobStatus('done', [{ name: 'upload', ok: true, detail: '已上传' }, { name: 'start', ok: true, detail: '已启动' }])
          }
          if (method === 'runner_list') return { runners: [] }
          if (method === 'runner_session_get') return { name: '', online: false }
          return {}
        },
      })
      setPluginSession('chat-9')
      mod.activate(ctx)
      render(<Panel />)
      await flushAsync()

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

      // 推进 1.5s → 第二次轮询：done（终态）
      await act(async () => {
        await vi.advanceTimersByTimeAsync(shared.JOB_POLL_INTERVAL_MS)
      })
      await flushAsync()
      expect(jobCalls).toBe(2)
      expect(screen.getByTestId('ssh-add-completed')).toBeInTheDocument()

      // 终态后再推进 10s：不得再调用（轮询已停止）
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

// ---------- 4) 会话切换 ----------

describe('会话切换', () => {
  it('切换发 runner_session_set{channel,chat_id,name}；「切回本机」传 name:""', async () => {
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [runnerInfo('gpu-box', true)] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { targets: JSON.stringify([target('gpu-box', 'ssh ubuntu@10.0.0.5')]) },
    })
    setPluginSession('chat-42')
    mod.activate(ctx)
    render(<Panel />)

    const switchBtn = await screen.findByTestId('ssh-switch-gpu-box')
    fireEvent.click(switchBtn)
    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('runner_session_set', { channel: 'web', chat_id: 'chat-42', name: 'gpu-box' })
    })
    // 切换成功后该行标记为「当前」
    await screen.findByText('当前')
    expect(screen.getByTestId('ssh-session-line')).toHaveTextContent('当前目标：gpu-box')

    // 切回本机 → name:''
    fireEvent.click(screen.getByTestId('ssh-switch-local'))
    await waitFor(() => {
      expect(call).toHaveBeenCalledWith('runner_session_set', { channel: 'web', chat_id: 'chat-42', name: '' })
    })
    expect(screen.getByTestId('ssh-session-line')).toHaveTextContent('当前目标：本机')
  })
})

// ---------- 5) 身份未就绪 ----------

describe('会话身份未就绪', () => {
  it('不发会话相关 RPC、切换按钮禁用、面板正常渲染', async () => {
    setPluginSession(null) // 无 __xbot_session__
    const { ctx, call } = makeCtx({
      rpc: (method) => {
        if (method === 'runner_list') return { runners: [runnerInfo('box1', true)] }
        return {}
      },
      config: { targets: JSON.stringify([target('box1', 'ssh user@host')]) },
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
  })
})

// ---------- 6) 未 activate 兜底 ----------

describe('未注入 ctx', () => {
  it('宿主未调用 activate 时渲染兜底提示且不崩', () => {
    mod.activate(undefined)
    render(<Panel />)
    expect(screen.getByTestId('ssh-runner-not-initialized')).toBeInTheDocument()
  })
})

// ---------- 7) 删除链路 ----------

describe('删除机器', () => {
  it('deprovision（uninstall:false）→ job done → runner_delete + targets 移除', async () => {
    const { ctx, call, set } = makeCtx({
      rpc: (method) => {
        if (method === 'xbot.ssh-runner.deprovision') return { job_id: 'job-del' }
        if (method === 'xbot.ssh-runner.job_status') return jobStatus('done', [{ name: 'stop', ok: true, detail: '' }])
        if (method === 'runner_delete') return {}
        if (method === 'runner_list') return { runners: [] }
        if (method === 'runner_session_get') return { name: '', online: false }
        return {}
      },
      config: { targets: JSON.stringify([target('gpu-box', 'ssh ubuntu@10.0.0.5')]) },
    })
    setPluginSession('chat-1')
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
    expect(set).toHaveBeenCalledWith('targets', '[]')
    await waitFor(() => {
      expect(screen.queryByTestId('ssh-target-gpu-box')).not.toBeInTheDocument()
    })
  })
})
