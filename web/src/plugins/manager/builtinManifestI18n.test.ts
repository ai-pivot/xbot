/**
 * 内置插件清单的文案必须**读取时**按当前宿主语言解析（getter），
 * 不能在模块 import 时求值一次。
 *
 * 回归背景（2026-09-19 用户实测：「改了语言插件没动态变化」）：
 * 内置插件（plugin-manager / skill-manager / session-stats / ambience）随主 bundle
 * 打包，拿不到插件清单的 `web.i18n` ⇒ 文案走宿主 i18n。若在模块顶层 `i18n.t(...)`
 * 求值一次，`manifest.name` / view `title` 就**定格在加载时的语言**：
 * 即使登记表在语言变化后重算，读到的仍是旧字符串 ⇒ tab/面板标题永不更新。
 *
 * 契约：manifest 是文案的权威来源，**每次读取都按当前语言解析**（getter）；
 * registry 也不得把 name 快照进 state（`listStates()` 读取时取 manifest.name）。
 */
import { afterEach, describe, expect, it } from 'vitest'

import i18n from '@/i18n'
import type { PluginManifest } from '@/plugin-api'
import { ContributionRegistry } from '@/plugin-runtime/registry'

import { manifest as pluginManager } from './pluginManager'

// 三个「文案在宿主 i18n」的内置清单：manager / skills / sessionStats。
import { manifest as skillManager } from '@/plugins/xbot-skill-manager/skillManager'
import { manifest as sessionStats } from '@/plugins/session-stats/sessionStats'

afterEach(async () => {
  await i18n.changeLanguage('zh-CN')
})

/** 取清单里第一个 view 贡献点的标题（跨三个清单复用，避免各自的字面量类型差异）。 */
function viewTitle(manifest: PluginManifest): string {
  const view = manifest.contributes.find((c) => c.kind === 'view')
  return view && 'title' in view ? String((view as { title: string }).title) : ''
}

describe('内置插件清单文案：读取时解析（不是 import 时求值一次）', () => {
  it('plugin-manager：name / description / view title 随语言变化', async () => {
    await i18n.changeLanguage('zh-CN')
    expect(pluginManager.name).toBe('插件管理')
    expect(pluginManager.description).toBe('管理插件：查看/启用/禁用/卸载/重载（自举实现，本身也是一个插件）')
    expect(viewTitle(pluginManager)).toBe('插件')

    await i18n.changeLanguage('en')
    expect(pluginManager.name).toBe('Plugin Manager')
    expect(viewTitle(pluginManager)).toBe('Plugins')

    await i18n.changeLanguage('ja')
    expect(pluginManager.name).toBe('プラグイン管理')
    expect(viewTitle(pluginManager)).toBe('プラグイン')

    // 切回中文同样成立（不是单向偶然）
    await i18n.changeLanguage('zh-CN')
    expect(pluginManager.name).toBe('插件管理')
    expect(viewTitle(pluginManager)).toBe('插件')
  })

  it('skill-manager：name / view title 随语言变化', async () => {
    await i18n.changeLanguage('zh-CN')
    expect(skillManager.name).toBe('技能管理')
    expect(viewTitle(skillManager)).toBe('技能')

    await i18n.changeLanguage('en')
    expect(skillManager.name).toBe('Skill Manager')
    expect(viewTitle(skillManager)).toBe('Skills')

    await i18n.changeLanguage('ja')
    expect(viewTitle(skillManager)).toBe('スキル')
  })

  it('session-stats：name / view title 随语言变化', async () => {
    await i18n.changeLanguage('zh-CN')
    expect(sessionStats.name).toBe('会话统计')
    expect(viewTitle(sessionStats)).toBe('统计')

    await i18n.changeLanguage('en')
    expect(sessionStats.name).toBe('Session Stats')
    expect(viewTitle(sessionStats)).toBe('Stats')
  })

  it('registry.listStates() 的 name 不落快照（读取时取 manifest.name）', async () => {
    const registry = new ContributionRegistry()
    await i18n.changeLanguage('zh-CN')
    const reg = await registry.registerPlugin(pluginManager, {})
    expect(reg.ok).toBe(true)
    expect(registry.listStates()[0]?.name).toBe('插件管理')

    await i18n.changeLanguage('en')
    // 修复前：state 里的 name 是注册瞬间的快照 ⇒ 永远 '插件管理'（本断言必红）
    expect(registry.listStates()[0]?.name).toBe('Plugin Manager')
  })
})
