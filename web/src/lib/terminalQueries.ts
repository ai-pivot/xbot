/**
 * 屏蔽"终端能力探针"的**应答** —— 防止应答被 PTY 行规程回显成正文。
 *
 * ## 现象与根因（用户报告 2026-09-22）
 *
 * 网页终端面板里会凭空出现这类字符（每次回车/每条命令重复出现）：
 *
 * ```
 * 10;rgb:cccc/cccc/cccc11;rgb:1e1e/1e1e/1e1e12;2$y2…;0c
 * ```
 *
 * 解码后它们分别是 **OSC 10/11**（前景/背景色）、**DA1**（设备属性）、**DECRQM**
 * （模式查询，如同步输出 2026）、**DSR/CPR**（光标位置）的**应答**，配色正是本面板的
 * dark 主题（`#1e1e1e` / `#cccccc`）。
 *
 * 机制：终端里跑的程序（典型是分页器 `less` —— `git`/`systemctl`/`man` 都会走它，
 * 且常配 `-F` 一屏即退）发出探针后**已经退出/不在 raw 模式**，于是 xterm.js 的应答
 * 落在 tty 输入队列里 → 被 **PTY 行规程的 ECHO 回显**成可见字符。
 *
 * ## 修法（方案 3：屏蔽应答，最小且无副作用）
 *
 * 给 xterm.js 注册 **no-op handler**：`register*Handler` 的回调返回 `true` 表示
 * "该序列已被处理" ⇒ xterm.js **不再自动应答** ⇒ 没有应答就没有可回显的东西。
 * 探针程序因此退化为保守默认（颜色/能力检测降级），不影响正常交互。
 *
 * 只拦截"查询"序列；普通输出、按键、resize 完全不受影响。
 */

/** 只需要 parser 的两个注册入口（便于单测用假实现）；真实类型见 `@xterm/xterm`。 */
export interface TerminalQueryTarget {
  parser: {
    registerOscHandler(ident: number, handler: (data: string) => boolean): { dispose(): void }
    registerCsiHandler(
      id: { prefix?: string; intermediates?: string; final: string },
      handler: (params: (number | number[])[]) => boolean,
    ): { dispose(): void }
  }
}

/** 一律应答"已处理"（= 不产生任何应答字节）。 */
const swallow = (): boolean => true

/**
 * 屏蔽终端查询的应答。必须在 `new Terminal(...)` 之后、`term.open()` 之前（或之后
 * 立即）调用 —— 注册是即时生效的。
 */
export function suppressTerminalQueryReplies(term: TerminalQueryTarget): void {
  // ① OSC 10/11/12：前景色 / 背景色 / 光标色查询（用户现场出现的就是 10/11）
  for (const ident of [10, 11, 12]) {
    term.parser.registerOscHandler(ident, swallow)
  }
  // ② DA1 `CSI c` / DA2 `CSI > c`：设备属性（现场出现的 `…;0c` 就是 DA1 应答尾巴）
  term.parser.registerCsiHandler({ final: 'c' }, swallow)
  term.parser.registerCsiHandler({ prefix: '>', final: 'c' }, swallow)
  // ③ DSR/CPR `CSI 6 n`：光标位置报告（现场出现的 `0;276R` 就是它）
  term.parser.registerCsiHandler({ final: 'n' }, swallow)
  // ④ DECRQM `CSI ? … $ p`：模式查询（现场出现的 `2$y` 就是它，如"支持同步输出吗"）
  term.parser.registerCsiHandler({ prefix: '?', intermediates: '$', final: 'p' }, swallow)
  // ⑤ XTVERSION `CSI > q`：终端版本查询（同类探针，顺手一起拦）
  term.parser.registerCsiHandler({ prefix: '>', final: 'q' }, swallow)
}
