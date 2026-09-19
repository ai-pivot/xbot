import { describe, expect, it } from 'vitest'
import { createPluginI18n } from './i18n'

// ===========================================================================
// 插件自带 i18n 的契约（2026-09-19 用户要求：插件 i18n 是平台通用能力）
//
// 解析顺序：当前 locale（精确 → 语言前缀）→ en → 表里第一个可用 locale → fallback → key。
// 硬约束：**永不抛错、永不返回 undefined**（最差返回 fallback ?? key），
//         且 `locale` 是读取时的实时快照（宿主切语言后立即生效）。
//
// 变异自证：砍掉 lookup 任一级（如去掉 en 回落、或去掉 key 兜底）⇒ 对应用例必红。
// ===========================================================================

describe('createPluginI18n：逐级回退', () => {
  const table = {
    'zh-CN': { hello: '你好', onlyZh: '只有中文' },
    en: { hello: 'Hello', onlyEn: 'only english' },
    ja: { hello: 'こんにちは' },
  }

  it('精确 locale 命中（ja）', () => {
    expect(createPluginI18n(table, () => 'ja').t('hello')).toBe('こんにちは')
  })

  it('zh-CN 精确命中', () => {
    expect(createPluginI18n(table, () => 'zh-CN').t('hello')).toBe('你好')
  })

  it('语言前缀回退：zh-TW / zh-HK 命中 zh-CN（地区变体不该退化到英文）', () => {
    expect(createPluginI18n(table, () => 'zh-TW').t('hello')).toBe('你好')
    expect(createPluginI18n(table, () => 'zh-HK').t('hello')).toBe('你好')
  })

  it('语言前缀回退：表里键是短前缀（"zh"）时，zh-CN 也能命中', () => {
    expect(createPluginI18n({ zh: { hello: '你好' } }, () => 'zh-CN').t('hello')).toBe('你好')
  })

  it('当前语言没有该 key ⇒ 回落 en', () => {
    expect(createPluginI18n(table, () => 'ja').t('onlyEn')).toBe('only english')
  })

  it('当前语言与 en 都没有 ⇒ 回落表里第一个提供该 key 的语言', () => {
    expect(createPluginI18n(table, () => 'ja').t('onlyZh')).toBe('只有中文')
  })

  it('任何语言都没有 ⇒ 用 fallback 参数', () => {
    expect(createPluginI18n(table, () => 'ja').t('nope', '兜底文案')).toBe('兜底文案')
  })

  it('没有 fallback ⇒ 返回 key 本身（绝不返回空串/undefined 语义）', () => {
    expect(createPluginI18n(table, () => 'ja').t('nope')).toBe('nope')
  })

  it('表缺失/为空 ⇒ 永不抛错，全部走 fallback', () => {
    expect(createPluginI18n(undefined, () => 'en').t('k', 'fb')).toBe('fb')
    expect(createPluginI18n({}, () => 'en').t('k')).toBe('k')
  })

  it('空 key ⇒ 返回 fallback（不查表）', () => {
    expect(createPluginI18n(table, () => 'en').t('', 'fb')).toBe('fb')
  })

  it('locale 是实时快照：宿主切语言后同一实例立刻给出新语言', () => {
    let loc = 'en'
    const i = createPluginI18n(table, () => loc)
    expect(i.t('hello')).toBe('Hello')
    loc = 'ja'
    expect(i.locale).toBe('ja')
    expect(i.t('hello')).toBe('こんにちは')
  })

  it('下划线写法的 locale 归一化为连字符（zh_CN → zh-CN）', () => {
    expect(createPluginI18n(table, () => 'zh_CN').t('hello')).toBe('你好')
  })
})
