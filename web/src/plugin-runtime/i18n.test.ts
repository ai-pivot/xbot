import { describe, expect, it } from 'vitest'
import { toManifest } from './usePluginRuntimeHost'
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

describe('桥：插件清单 web.i18n → ctx.i18n（端到端可达性）', () => {
  it('toManifest() 必须透传 decl.i18n，且该表真的参与解析（不是 fallback）', () => {
    // 断链根因（2026-09-19 用户实测：「插件语言设置英文，ssh 插件仍中文」）：
    // 前端 WebPluginDecl 无 i18n 字段、toManifest() 不透传 ⇒ createPluginI18n(undefined)
    // ⇒ 插件 ctx.i18n.t(key, 中文兜底) 永远走 fallback。此用例守住整条桥。
    const decl = {
      id: 'xbot.ssh-runner', name: 'ssh-runner', version: '1.0.0', state: 'active', enabled: true,
      permissions: ['rpc'], entry: 'web/index.js', module_url: '/plugins/xbot.ssh-runner/web/index.js',
      i18n: { en: { connect: 'Connect' }, ja: { connect: '接続' } },
    }
    const manifest = toManifest(decl)
    expect(manifest.i18n).toBeDefined() // 漏透传时这里红
    const en = createPluginI18n(manifest.i18n, () => 'en')
    expect(en.t('connect', '连接')).toBe('Connect') // 命中清单表（宿主语言=en），而非中文兜底
    const ja = createPluginI18n(manifest.i18n, () => 'ja')
    expect(ja.t('connect', '连接')).toBe('接続')
    expect(en.t('missing', '兜底')).toBe('兜底') // 缺 key 才回落
  })
})
