/**
 * 发送成功后，是否应把会话**乐观**标记为 `running`（busy）。
 *
 * 契约（2026-09-17 用户报告「`!pwd` 输出下方多出一个『思考中』」根治）：
 * **只有会开启 turn 的发送才可以乐观置 busy。**
 *
 *  - 普通消息：REST 响应带 `turn_id` ⇒ 一定走 turn 生命周期
 *    （`turn_started` … `session(idle)`）⇒ 乐观置位会被正确清除 ✓
 *  - 排队消息（`queued === true`）：当前 turn 结束后会被处理，同样有 turn
 *    生命周期 ✓
 *  - **命令（`!cmd` / slash）**：后端按设计**没有 turn**（Concurrent 分支：无
 *    `turn_started`、无 `turn_id`、也无 `session(idle)`）⇒ 乐观置位**永远得不到
 *    清除** ⇒ 会话在会话树里永久 busy。
 *    渲染后果：`MessageList` 的 busy 占位符条件是
 *    `busy && !(loading && rows.length === 0) && liveId === null` —— 命令不产生
 *    live 行（`liveId === null`）而 `busy` 为真 ⇒ 占位符在**消息列表最底部**
 *    渲染 `ShimmerThinking`（「思考中…」），看起来就像"命令执行完了还在思考"。
 *
 * ⛔ 反面写法（已修掉）：无条件 `store.setStatus(selector, 'running')`。
 * 那种写法对命令等于写入一个**没有清除路径**的状态，是"状态泄漏"而非"乐观更新"。
 */
export function sendStartsTurn(info?: { turnID?: number; queued?: boolean }): boolean {
  if (!info) return false
  if (info.queued === true) return true
  return (info.turnID ?? 0) > 0
}
