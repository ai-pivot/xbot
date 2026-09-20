/**
 * xbot.session-stats 的 i18n 桥。
 *
 * 契约（2026-09-19 平台通用能力）：插件文案**随插件清单的 `web.i18n` 表分发**，
 * 绝不写进宿主的 `web/src/i18n/*.ts`（那是 i18n-builtins 线独占的命名空间）。
 * 宿主在 activate 时把解析器注入进来（`ctx.i18n`），面板/图表经本模块取文案。
 *
 * 与 web/src/plugins/ssh-runner/shared.ts 的 `t()` 同一模式（独立 bundle 无法 import
 * 宿主 i18n；本插件虽为 builtin，但走同一条契约可保证文案随清单走）。
 */
import type { I18nAPI } from '@/plugin-api'

let instance: I18nAPI | null = null

/** activate(ctx) 时注入（表来自清单 web.i18n）；传 undefined 表示未注入。 */
export function setPluginI18n(i18n: I18nAPI | undefined): void {
  instance = i18n ?? null
}

/** 当前是否已注入（测试与自检用）。 */
export function hasPluginI18n(): boolean {
  return instance !== null
}

/**
 * 取文案：命中插件表 → 插值 `{{x}}` 占位符；未注入 / key 缺失 ⇒ 返回调用点写的中文
 * fallback。**UI 永不显示裸 key**，也永不抛错。
 */
export function t(key: string, fallback: string, params?: Record<string, string | number>): string {
  let text = fallback
  if (instance) {
    try {
      text = instance.t(key, fallback)
    } catch {
      /* 解析异常 ⇒ 回退 fallback */
    }
  }
  if (params) {
    for (const [name, value] of Object.entries(params)) {
      text = text.split(`{{${name}}}`).join(String(value))
    }
  }
  return text
}
