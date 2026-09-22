import { describe, expect, it } from 'vitest'

import { suppressTerminalQueryReplies } from './terminalQueries'

/**
 * 用户报告（2026-09-22）：网页终端面板里凭空出现
 * `10;rgb:cccc/cccc/cccc11;rgb:1e1e/1e1e/1e1e12;2$y…;0c` 这类字符 ——
 * 它们是 **OSC 10/11（颜色）/ DA1（设备属性）/ DECRQM（模式）/ DSR（光标位置）** 的
 * 应答被 PTY 行规程回显。修法 = 让 xterm.js **不产生应答**（handler 返回 true = 已处理）。
 *
 * 本测试把"必须被拦截的探针清单"钉死（漏一个 ⇒ 那类应答又会回显）。
 */
function fakeTerm() {
  type CsiParams = (number | number[])[]
  const osc: { ident: number; handler: (data: string) => boolean }[] = []
  const csi: {
    id: { prefix?: string; intermediates?: string; final: string }
    handler: (p: CsiParams) => boolean
  }[] = []
  return {
    osc,
    csi,
    parser: {
      registerOscHandler: (ident: number, handler: (data: string) => boolean) => {
        osc.push({ ident, handler })
        return { dispose: () => {} }
      },
      registerCsiHandler: (
        id: { prefix?: string; intermediates?: string; final: string },
        handler: (p: CsiParams) => boolean,
      ) => {
        csi.push({ id, handler })
        return { dispose: () => {} }
      },
    },
  }
}

describe('suppressTerminalQueryReplies（终端探针应答拦截）', () => {
  it('OSC 10/11/12（前景/背景/光标色查询）必须拦截，且回调返回 true（= 已处理 ⇒ 无应答）', () => {
    const t = fakeTerm()
    suppressTerminalQueryReplies(t)

    for (const ident of [10, 11, 12]) {
      const reg = t.osc.find((o) => o.ident === ident)
      expect(reg, `OSC ${ident} 必须注册拦截器`).toBeTruthy()
      expect(reg!.handler('?'), `OSC ${ident} 的 handler 必须返回 true（吞掉，不产生应答）`).toBe(true)
    }
  })

  it('DA1 / DA2 / DSR(CPR) / DECRQM / XTVERSION 必须拦截', () => {
    const t = fakeTerm()
    suppressTerminalQueryReplies(t)

    const ids = t.csi.map((c) => JSON.stringify(c.id))
    for (const want of [
      { final: 'c' }, // DA1（现场 `…;0c`）
      { prefix: '>', final: 'c' }, // DA2
      { final: 'n' }, // DSR/CPR（现场 `0;276R`）
      { prefix: '?', intermediates: '$', final: 'p' }, // DECRQM（现场 `2$y`）
      { prefix: '>', final: 'q' }, // XTVERSION
    ]) {
      expect(ids, `必须拦截 ${JSON.stringify(want)}`).toContain(JSON.stringify(want))
    }
    for (const c of t.csi) {
      expect(c.handler([]), `CSI ${JSON.stringify(c.id)} 的 handler 必须返回 true`).toBe(true)
    }
  })

  it('回归守护：现场出现过的四类应答（OSC10 / OSC11 / DA1 / DECRQM）都在拦截清单内', () => {
    const t = fakeTerm()
    suppressTerminalQueryReplies(t)

    // 只断言"现场那四类"—— 少一个就会让对应字符重新出现在终端里。
    expect(t.osc.map((o) => o.ident)).toEqual(expect.arrayContaining([10, 11]))
    expect(t.csi.map((c) => JSON.stringify(c.id))).toEqual(
      expect.arrayContaining([JSON.stringify({ final: 'c' }), JSON.stringify({ prefix: '?', intermediates: '$', final: 'p' })]),
    )
  })
})
