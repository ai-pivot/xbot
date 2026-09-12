/**
 * 思考模式控件 i18n 守护（2026-09-12 用户两次踩坑）：
 *   1. 档位文案曾是硬编码 ASCII（`think-`/`think`/`think+`/`think++`）——中文界面显示英文；
 *   2. 接 i18n 后 key 被插进 **sidebar** 命名空间而组件查的是 **settings** →
 *      页面直接渲染原始 key `settings.thinkingStepOn`（ja 环境实测）。
 *
 * 本测试同时守护两点：三种语言的 settings 字典都**必须**有这 4 个 key（键位正确），
 * 且控件渲染出来的是**本地化文案**（绝不含 'settings.' 前缀 / ASCII 'think'）。
 */
import { afterEach, describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import '@testing-library/jest-dom'

import { ThinkingModeControl } from '@/components/agent/ThinkingModeControl'
import { changeLocale } from '@/i18n'
import en from '@/i18n/en'
import ja from '@/i18n/ja'
import zhCN from '@/i18n/zh-CN'
import { renderWithProviders } from '@/test-utils'

const STEP_KEYS = ['thinkingStepOff', 'thinkingStepOn', 'thinkingStepPlus', 'thinkingStepMax'] as const

describe('思考模式控件 i18n', () => {
  afterEach(() => changeLocale('zh-CN'))

  it('三种语言的 settings 字典都含 4 个档位 key（键位正确，不是 sidebar.*）', () => {
    for (const [name, dict] of [['zh-CN', zhCN], ['en', en], ['ja', ja]] as const) {
      const settings = (dict as unknown as { settings: Record<string, string> }).settings
      for (const k of STEP_KEYS) {
        expect(settings[k], `${name}.settings.${k} 缺失`).toBeTruthy()
      }
      const sidebar = (dict as unknown as { sidebar?: Record<string, string> }).sidebar
      // 错位 key 不得留在 sidebar 命名空间（会造成"死键 + 原始 key 上屏"）
      for (const k of STEP_KEYS) {
        expect(sidebar?.[k], `${name}.sidebar.${k} 是错位死键`).toBeUndefined()
      }
    }
  })

  it.each([
    ['zh-CN', '思考++'],
    ['en', 'Think++'],
    ['ja', '思考++'],
  ] as const)('%s：控件渲染本地化档位文案，绝不渲染原始 key / ASCII think', (locale, expectedTick) => {
    changeLocale(locale)
    renderWithProviders(
      <ThinkingModeControl value="think" onValueCommit={() => true} />,
    )
    const text = document.body.textContent ?? ''
    // 本地化文案出现（滑杆最后一档）
    expect(text).toContain(expectedTick)
    // 原始 key 绝不能上屏
    expect(text).not.toContain('settings.thinkingStep')
    // 硬编码 ASCII 档位标签不得再出现在可见文案里
    expect(text).not.toMatch(/\bthink\+\+|\bthink-\b/)
    expect(screen.getAllByLabelText(/思考|Thinking|思考モード/).length).toBeGreaterThan(0)
  })
})
