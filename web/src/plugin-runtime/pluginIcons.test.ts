import { describe, expect, it, vi } from 'vitest'
import { Puzzle } from 'lucide-react'
import { pluginIcon } from './pluginIcons'

// 2026-09-17：xbot.ssh-runner 的 manifest 声明 icon:"server"，但 ICON_MAP 没登记
// ⇒ 侧栏/面板一直显示 Puzzle（用户看到的"默认图标"）。这组用例守护协议层的这条契约：
// manifest 里声明的名字必须有真实图标，且未登记时不再静默退化成 Puzzle。
describe('pluginIcon', () => {
  it('ssh-runner 声明的 "server" 必须映射到真实图标（不是默认 Puzzle）', () => {
    expect(pluginIcon('server')).not.toBe(Puzzle)
  })

  it('server 与 server-cog 指向同一图标（显式名可用）', () => {
    expect(pluginIcon('server-cog')).not.toBe(Puzzle)
    expect(pluginIcon('server')).toBe(pluginIcon('server-cog'))
  })

  it('已有插件声明的图标不回归', () => {
    for (const name of ['git-branch', 'git-commit-horizontal', 'git', 'terminal', 'chart']) {
      expect(pluginIcon(name), name).not.toBe(Puzzle)
    }
  })

  it('未登记的名字回退 Puzzle，且只告警一次（大小写不敏感，避免每帧刷屏）', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(pluginIcon('totally-unknown-icon-xyz')).toBe(Puzzle)
    expect(warn).toHaveBeenCalledTimes(1)
    pluginIcon('totally-unknown-icon-xyz')
    pluginIcon('TOTALLY-Unknown-Icon-XYZ')
    expect(warn).toHaveBeenCalledTimes(1)
    warn.mockRestore()
  })

  it('没有声明 icon 时不告警，直接 Puzzle', () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {})
    expect(pluginIcon()).toBe(Puzzle)
    expect(pluginIcon(undefined)).toBe(Puzzle)
    expect(warn).not.toHaveBeenCalled()
    warn.mockRestore()
  })
})
