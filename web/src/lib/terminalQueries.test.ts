import { describe, expect, it } from 'vitest'
import { Terminal } from '@xterm/xterm'
import { suppressTerminalQueryReplies, type TerminalQueryTarget } from './terminalQueries'

/**
 * 假 parser：记录注册了哪些 handler（用于"清单/覆盖矩阵"这类结构性断言）。
 * 真 xterm 的行为由下面第二组「集成」用例证明（喂真探针、断言**不产生应答字节**）。
 */
function fakeTarget() {
  const osc: number[] = []
  const csi: string[] = []
  const target: TerminalQueryTarget = {
    parser: {
      registerOscHandler(ident, _handler) {
        osc.push(ident)
        return { dispose: () => {} }
      },
      registerCsiHandler(id, _handler) {
        csi.push(`${id.prefix ?? ''}|${id.intermediates ?? ''}|${id.final}`)
        return { dispose: () => {} }
      },
    },
  }
  return { target, osc, csi }
}

describe('suppressTerminalQueryReplies：覆盖矩阵（结构性）', () => {
  it('回调必须返回 true（= 已处理 ⇒ xterm 不再自动应答）', () => {
    const seen: boolean[] = []
    const target: TerminalQueryTarget = {
      parser: {
        registerOscHandler(_ident, handler) {
          seen.push(handler(''))
          return { dispose: () => {} }
        },
        registerCsiHandler(_id, handler) {
          seen.push(handler([]))
          return { dispose: () => {} }
        },
      },
    }
    suppressTerminalQueryReplies(target)
    expect(seen.length).toBeGreaterThan(0)
    expect(seen.every((v) => v === true)).toBe(true)
  })

  it('现场出现过的四类探针（OSC 10/11、DA1、DECRQM、CPR/DECDSR）必须在清单内', () => {
    const { target, osc, csi } = fakeTarget()
    suppressTerminalQueryReplies(target)
    expect(osc).toEqual(expect.arrayContaining([10, 11, 12]))
    expect(csi).toEqual(expect.arrayContaining(['||c', '>||c', '||n', '?||n', '?|$|p', '>||q']))
  })

  it('⚠️ `CSI ? n`（DECDSR）必须单独注册 —— `{final:"n"}` 只匹配无 prefix 的 `CSI n`', () => {
    const { target, csi } = fakeTarget()
    suppressTerminalQueryReplies(target)
    expect(csi).toContain('||n')
    expect(csi).toContain('?||n')
  })

  it('刻意不碰**输出/控制**序列：SGR、光标移动、擦除、模式、窗口操作、标题/超链接/调色板 OSC', () => {
    const { target, osc, csi } = fakeTarget()
    suppressTerminalQueryReplies(target)
    // CSI：只拦"查询"final；m(SGR)/t(窗口操作)/H/A/B(光标)/J/K(擦除)/h/l(模式) 一律不注册
    for (const forbidden of ['||m', '||t', '||H', '||A', '||B', '||J', '||K', '||h', '||l']) {
      expect(csi).not.toContain(forbidden)
    }
    // OSC：0/1/2 标题、4 调色板（含设置语义）、8 超链接、52 剪贴板 都不是"探针"
    for (const forbidden of [0, 1, 2, 4, 8, 52]) {
      expect(osc).not.toContain(forbidden)
    }
  })

  it('CPR 可关（suppressCursorPositionReport: false）⇒ DSR/DECDSR 不再注册，其余照旧', () => {
    const { target, osc, csi } = fakeTarget()
    suppressTerminalQueryReplies(target, { suppressCursorPositionReport: false })
    expect(csi).not.toContain('||n')
    expect(csi).not.toContain('?||n')
    expect(csi).toEqual(expect.arrayContaining(['||c', '>||c', '?|$|p', '>||q']))
    expect(osc).toEqual(expect.arrayContaining([10, 11, 12]))
  })
})

/**
 * 集成（真 xterm）—— **这才是原 bug 的判据**：装屏蔽后，喂入现场那些探针
 * **一个应答字节都不能产生**（没有应答 ⇒ PTY 行规程没有可回显的东西 ⇒ 不再出乱码）。
 */
describe('集成：真 xterm 下探针不产生应答字节（对照保证有判别力）', () => {
  const PROBES = '\x1b]10;?\x1b\\\x1b]11;?\x1b\\\x1b[c\x1b[?2026$p\x1b[6n'

  async function feed(term: Terminal, data: string) {
    await new Promise<void>((resolve) => term.write(data, () => resolve()))
  }

  it('装了屏蔽 ⇒ onData 收不到任何应答字节', async () => {
    const term = new Terminal()
    const replies: string[] = []
    term.onData((d) => replies.push(d))
    suppressTerminalQueryReplies(term)
    await feed(term, PROBES)
    expect(replies.join('')).toBe('')
    term.dispose()
  })

  it('对照：不装屏蔽 ⇒ 同样的探针**确实**产生应答（否则上面的用例是假绿）', async () => {
    const term = new Terminal()
    const replies: string[] = []
    term.onData((d) => replies.push(d))
    await feed(term, PROBES)
    expect(replies.join('')).not.toBe('')
    // 应答里应能看到 OSC 10/11 的颜色回答与 DA1 的 `c` 结尾（现场乱码的来源）
    expect(replies.join('')).toMatch(/rgb:|c/)
    term.dispose()
  })
})
