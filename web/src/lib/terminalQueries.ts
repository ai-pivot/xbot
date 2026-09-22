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
 * ## 修法（屏蔽应答）
 *
 * 给 xterm.js 注册 **no-op handler**：`register*Handler` 的回调返回 `true` 表示
 * "该序列已被处理" ⇒ xterm.js **不再自动应答** ⇒ 没有应答就没有可回显的东西。
 * 探针程序因此退化为保守默认（颜色/能力检测降级），不影响正常交互。
 *
 * ## 覆盖矩阵（⚠️ 新增任何"终端探针"都要同步这张表）
 *
 * | 探针 | 序列 | 屏蔽 | 现场对应 |
 * |---|---|---|---|
 * | OSC 10 / 11 / 12 | `OSC 1x ; ? ST` | ✅ | `10;rgb:…` / `11;rgb:…` |
 * | DA1 / DA2 | `CSI c` / `CSI > c` | ✅ | `…;0c` |
 * | DSR 状态/CPR | `CSI n`（5n/6n） | ✅（可关，见下） | `0;276R` |
 * | DECDSR | `CSI ? n` | ✅（可关，见下） | 同类光标/状态报告 |
 * | DECRQM | `CSI ? … $ p` | ✅ | `2$y` |
 * | XTVERSION | `CSI > q` | ✅ | 同类探针，顺手拦 |
 *
 * **刻意不屏蔽**（盲拦会破坏正常功能，xterm 的 handler 是**精确匹配**、没有"兜底"概念）：
 * - SGR `CSI … m`、光标移动 `CSI … H/A/B/C/D`、擦除 `CSI … J/K`：**输出**语义，与探针无关；
 * - 窗口操作 `CSI … t`（如 `CSI 8 ; h ; w t` 改窗口大小）：含**主动控制**语义，一律吞会破坏
 *   resize 类功能 ⇒ 只拦明确的"查询" final；
 * - OSC 4（调色板）：既有查询也有**设置**语义，吞掉会破坏设置；OSC 0/1/2（标题）、OSC 8（超链接）、
 *   OSC 52（剪贴板）：都是**指令**而非探针 ⇒ 不碰。
 *
 * ## ⚠️ CPR 的取舍（必须知道的副作用）
 *
 * CPR 与颜色/能力探针不同，它是**载荷**序列：部分交互程序（readline 的某些配置 / `fzf` / TUI /
 * `tmux` 客户端）会**等**它的应答来定位光标；一律不应答可能让它们**卡住或重绘错位**。
 * 现场确实出现过 CPR 应答被回显（`0;276R`），所以**默认仍屏蔽**；若发现某个 TUI 卡住，可
 * `suppressTerminalQueryReplies(term, { suppressCursorPositionReport: false })` 关掉。
 *
 * 更彻底的方案（follow-up，未实现）：由**服务端**按 PTY 当前 termios 判定 —— `ECHO` 打开
 * （前台程序已退出 / cooked 模式）才屏蔽，`raw` 模式照常应答。那需要新增一次往返协议。
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

export interface SuppressTerminalQueryRepliesOptions {
  /**
   * 是否也屏蔽 DSR / CPR（`CSI n` 与 `CSI ? n`）的应答。默认 `true`。
   * ⚠️ 关掉它的场景：某个 TUI/REPL（readline 某些配置 / `fzf` / `tmux`）因为等不到 CPR 而卡住
   * 或重绘错位 —— 详见文件头「CPR 的取舍」。
   */
  suppressCursorPositionReport?: boolean
}

/** 一律应答"已处理"（= 不产生任何应答字节）。 */
const swallow = (): boolean => true

/**
 * 屏蔽终端查询的应答。必须在 `new Terminal(...)` 之后调用（注册即时生效；`term.open()` 前后皆可）。
 */
export function suppressTerminalQueryReplies(
  term: TerminalQueryTarget,
  options: SuppressTerminalQueryRepliesOptions = {},
): void {
  const { suppressCursorPositionReport = true } = options
  // ① OSC 10/11/12：前景色 / 背景色 / 光标色查询（用户现场出现的就是 10/11）
  for (const ident of [10, 11, 12]) {
    term.parser.registerOscHandler(ident, swallow)
  }
  // ② DA1 `CSI c` / DA2 `CSI > c`：设备属性（现场出现的 `…;0c` 就是 DA1 应答尾巴）
  term.parser.registerCsiHandler({ final: 'c' }, swallow)
  term.parser.registerCsiHandler({ prefix: '>', final: 'c' }, swallow)
  // ③ DSR/CPR `CSI n`（5n=状态 / 6n=光标位置）与 DECDSR `CSI ? n`
  //    ⚠️ `{final:'n'}` 只匹配**无 prefix** 的 `CSI n`；`CSI ? n` 必须单独注册（否则"以为拦了其实没拦"）。
  if (suppressCursorPositionReport) {
    term.parser.registerCsiHandler({ final: 'n' }, swallow)
    term.parser.registerCsiHandler({ prefix: '?', final: 'n' }, swallow)
  }
  // ④ DECRQM `CSI ? … $ p`：模式查询（现场出现的 `2$y` 就是它，如"支持同步输出吗"）
  term.parser.registerCsiHandler({ prefix: '?', intermediates: '$', final: 'p' }, swallow)
  // ⑤ XTVERSION `CSI > q`：终端版本查询（同类探针，顺手一起拦）
  term.parser.registerCsiHandler({ prefix: '>', final: 'q' }, swallow)
}
