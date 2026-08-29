/**
 * ambience store 性能风暴回归测试（2026-08-29 桌宠事件风暴 bug）。
 *
 * 根因链：progress.iteration 事件（agent 运行时高频）→ PetWidget setMood →
 * setProps（无 diff）→ emit → snapshot 重建（wallpapers flatMap 新数组引用）→
 * AmbienceBackground 的 wp useMemo deps（snap.wallpapers）失效 → 玻璃化
 * effect deps（wp 对象引用）失效 → 每条事件重跑 getComputedStyle ×3 +
 * setProperty --bg-* ×3（全站 CSS 变量失效）→ layout/paint 风暴。
 *
 * 这里的三个测试分别锁定链条的三个环节：
 *  1. setProps/show 相同内容不 emit（事件源头节流）
 *  2. 无关 mutate 后 snapshot 集合引用稳定（useMemo deps 不失效）
 *  3. （xbot-ambience 侧的 mood guard 见插件测试）
 */
import { describe, expect, it } from 'vitest'

import { ambienceStore } from './store'
import type { WallpaperPreset } from '@/plugin-api'

/** wallpapers 快照引用（引用稳定性断言用）。 */
function wallpapersRef(): { pluginId: string; preset: WallpaperPreset }[] {
  return ambienceStore.get().wallpapers
}

describe('ambience store — 桌宠事件风暴修复（性能回归）', () => {
  it('setProps 相同内容不触发订阅（progress.iteration 高频重放防护）', () => {
    const handle = ambienceStore.mountOverlay('test-plugin', { layer: 'hud' }, () => null, { mood: 'idle' })
    let emits = 0
    const unsub = ambienceStore.subscribe(() => { emits++ })

    // 相同内容 props 重放（progress.iteration 每条事件 setMood 相同情绪的路径）
    handle.setProps({ mood: 'idle' })
    handle.setProps({ mood: 'idle' })
    handle.setProps({ mood: 'idle' })
    expect(emits).toBe(0) // 修复前：3 次 emit（无条件 mutate → 全订阅者重渲染）

    // 内容变化才 emit
    handle.setProps({ mood: 'thinking' })
    expect(emits).toBe(1)

    // show 相同值同样不 emit
    handle.show(true)
    expect(emits).toBe(1)
    handle.show(false)
    expect(emits).toBe(2)

    unsub()
    handle.dispose()
  })

  it('overlay setProps 时 snapshot.wallpapers 引用稳定（wp useMemo deps 不失效）', () => {
    const presets: WallpaperPreset[] = [{ id: 'w1', name: 'W', css: 'red' }]
    ambienceStore.syncPluginWallpapers([{ pluginId: 'perf-test', presets }])
    const refBefore = wallpapersRef()
    expect(refBefore.length).toBeGreaterThanOrEqual(1)

    const handle = ambienceStore.mountOverlay('perf-test', { layer: 'decoration' }, () => null)
    handle.setProps({ tick: 1 })

    // 无关集合（wallpapers）引用必须不变——变了会让消费方 useMemo deps 抖动，
    // 玻璃化 effect 每条事件重跑（getComputedStyle + setProperty 全站失效风暴）。
    expect(wallpapersRef()).toBe(refBefore)
    // 变化集合（overlays）必须换新引用（订阅者需要感知）。
    expect(ambienceStore.get().overlays.length).toBeGreaterThan(0)

    handle.dispose()
  })

  it('userWallpapers 注册/删除走引用替换（快照集合身份可比较）', () => {
    const before = ambienceStore.get().userWallpapers
    ambienceStore.registerUserWallpaper({ id: 'user:ref-test', name: 'T', css: 'url(x)' })
    const after = ambienceStore.get().userWallpapers
    expect(after).not.toBe(before) // 内容变化必须新引用
    expect(after.find((u) => u.id === 'user:ref-test')).toBeDefined()
    ambienceStore.removeUserWallpaper('user:ref-test')
    expect(ambienceStore.get().userWallpapers.find((u) => u.id === 'user:ref-test')).toBeUndefined()
  })
})
