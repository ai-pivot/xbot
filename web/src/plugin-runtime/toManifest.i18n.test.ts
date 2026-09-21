import { describe, expect, it } from 'vitest'
import { toManifest } from './usePluginRuntimeHost'

// 守护：宿主把后端 decl 转成 manifest 时必须透传 i18n。
// 曾发生：WebPluginDecl/toManifest 漏掉 i18n ⇒ 插件 ctx.i18n.t() 全部回退中文兜底
// （现象：宿主语言是 en，插件面板却是中文）。
describe('toManifest · i18n 透传', () => {
  const decl = {
    id: 'xbot.ssh-runner',
    name: 'SSH Runner',
    version: '1.0.0',
    permissions: ['rpc', 'ui'],
    entries: [],
    i18n: { en: { stateDisconnected: 'Not connected' }, 'zh-CN': { stateDisconnected: '未连接' } },
  } as never

  it('保留 i18n 表（缺了插件只能回退兜底文案）', () => {
    const m = toManifest(decl)
    expect(m.i18n).toEqual({
      en: { stateDisconnected: 'Not connected' },
      'zh-CN': { stateDisconnected: '未连接' },
    })
  })
})
