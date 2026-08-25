/**
 * SkillManagerPanel —— 启禁用开关回归测试。
 *
 * 回归背景（用户报告）：
 * 1. 内嵌（embedded）skill 不渲染启禁用开关 —— 旧代码 `skill.source !== 'embedded'`
 *    条件把 embedded 排除在开关之外，但后端 SetSkillEnabled 是 disabled_skills
 *    黑名单机制，对 embedded 同样生效，前端排除是缺陷。
 * 2. 开关用裸 <button> 实现，缺 shrink-0，容器 justify-between 里 path 较长时
 *    开关被 flex 压缩变形（button 内 span 是 absolute 不占布局空间 → min-width 解析为 0）。
 * 修复：统一改用项目 radix Switch 组件（内置 shrink-0 + 标准样式），所有 skill 均渲染开关。
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'

import { SkillManagerPanel } from './SkillManagerPanel'

vi.mock('@/providers/i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

const { rpcCall, runtime, cwdValue } = vi.hoisted(() => {
  const rpcCall = vi.fn()
  const openFileTab = vi.fn()
  const cwdValue = { current: '/home/cjw/xbot' as string | null }
  return { rpcCall, runtime: { rpc: { call: rpcCall }, ui: { openFileTab } }, cwdValue }
})

// runtime 必须是稳定引用——usePluginRuntime 的 load useCallback 依赖 [runtime]，
// 每次渲染返回新对象会导致 useEffect 无限重跑（一直 loading）。
vi.mock('@/plugin-runtime', () => ({
  usePluginRuntime: () => runtime,
}))

// useCwd mock — 默认返回项目根目录
vi.mock('@/hooks/useCwd', () => ({
  useCwd: () => ({ cwd: cwdValue.current, loading: false }),
}))

vi.mock('@/lib/api', () => ({
  postAPI: vi.fn().mockResolvedValue({ ok: true }),
  postRawAPI: vi.fn().mockResolvedValue({ blob: () => Promise.resolve(new Blob()) }),
}))

/** 与后端 agent/skills.go ListSkillsDetailed 返回对齐。 */
const skills = [
  {
    name: 'debug',
    description: '调试技能',
    path: 'embedded:debug',
    source: 'embedded',
    enabled: true,
    can_uninstall: false,
  },
  {
    name: 'issue-solver',
    description: 'Issue 解决流程',
    path: '/home/cjw/.xbot/skills/issue-solver',
    source: 'global',
    enabled: false,
    can_uninstall: false,
  },
]

describe('SkillManagerPanel 启禁用开关', () => {
  beforeEach(() => {
    rpcCall.mockReset()
    rpcCall.mockResolvedValue(skills)
  })

  it('embedded skill 也渲染启禁用开关（不再排除 embedded）', async () => {
    render(<SkillManagerPanel />)
    const switches = await screen.findAllByRole('switch')
    expect(switches.length).toBe(skills.length) // 所有 skill（含 embedded）都有开关
  })

  it('开关 aria-checked 反映 enabled 状态', async () => {
    render(<SkillManagerPanel />)
    const switches = await screen.findAllByRole('switch')
    // debug (embedded, enabled=true) → checked
    expect(switches[0].getAttribute('aria-checked')).toBe('true')
    // issue-solver (global, disabled) → unchecked
    expect(switches[1].getAttribute('aria-checked')).toBe('false')
  })

  it('点击开关调用 skill_set_enabled 并反转状态', async () => {
    render(<SkillManagerPanel />)
    const switches = await screen.findAllByRole('switch')
    fireEvent.click(switches[0]) // debug enabled=true → 禁用
    await waitFor(() => {
      expect(rpcCall).toHaveBeenCalledWith('skill_set_enabled', { name: 'debug', enabled: false })
    })
    fireEvent.click(switches[1]) // issue-solver enabled=false → 启用
    await waitFor(() => {
      expect(rpcCall).toHaveBeenCalledWith('skill_set_enabled', { name: 'issue-solver', enabled: true })
    })
  })

  it('已禁用 skill 显示「已禁用」标签', async () => {
    render(<SkillManagerPanel />)
    expect(await screen.findByText('skills.disabled')).toBeTruthy()
  })
})

describe('SkillManagerPanel 项目 skill 显示', () => {
  beforeEach(() => {
    rpcCall.mockReset()
    rpcCall.mockResolvedValue(skills)
  })

  it('skill_list 传当前 CWD 作为 project_dir（非空）', async () => {
    cwdValue.current = '/home/cjw/xbot'
    render(<SkillManagerPanel />)
    await waitFor(() => {
      expect(rpcCall).toHaveBeenCalledWith(
        'skill_list',
        { project_dir: '/home/cjw/xbot' },
      )
    })
  })

  it('CWD 为 null 时传空字符串（不崩溃）', async () => {
    cwdValue.current = null
    render(<SkillManagerPanel />)
    await waitFor(() => {
      expect(rpcCall).toHaveBeenCalledWith(
        'skill_list',
        { project_dir: '' },
      )
    })
  })
})

describe('SkillManagerPanel SKILL.md 预览', () => {
  beforeEach(() => {
    rpcCall.mockReset()
    rpcCall.mockResolvedValue(skills)
    runtime.ui.openFileTab.mockReset()
  })

  it('非 embedded skill 点击预览调用 openFileTab（路径含 /SKILL.md）', async () => {
    render(<SkillManagerPanel />)
    const viewBtns = await screen.findAllByTitle('skills.view')
    fireEvent.click(viewBtns[1]) // issue-solver (非 embedded)
    await waitFor(() => {
      expect(runtime.ui.openFileTab).toHaveBeenCalledWith('/home/cjw/.xbot/skills/issue-solver/SKILL.md')
    })
    // 不调用 skill_get_content
    expect(rpcCall).not.toHaveBeenCalledWith(
      'skill_get_content',
      expect.anything(),
    )
  })

  it('embedded skill 点击预览也调用 openFileTab（路径 embedded:xxx/SKILL.md）', async () => {
    render(<SkillManagerPanel />)
    const viewBtns = await screen.findAllByTitle('skills.view')
    fireEvent.click(viewBtns[0]) // debug (embedded:debug)
    await waitFor(() => {
      expect(runtime.ui.openFileTab).toHaveBeenCalledWith('embedded:debug/SKILL.md')
    })
    // 不调用 skill_get_content
    expect(rpcCall).not.toHaveBeenCalledWith(
      'skill_get_content',
      expect.anything(),
    )
  })
})
