/**
 * sessionSwitch —— 会话切换的**过渡态**（唯一事实源）。
 *
 * 契约（用户 2026-09-18，多次）：「会话只要开始切换就应该渲染 loading 了，这才是修复」。
 *
 * 为什么必须有这个模块：切换窗口期（点击 → dockview 建/激活 tab → 目标面板历史就绪）
 * 里，主区会经历若干布局变化帧。此前 loading 由**每个面板根据自己的状态**决定，于是
 * 存在「还没有任何面板进入 loading 态」的帧 —— 那一帧里旧面板仍以正常态渲染，一旦
 * 被以未兑现的尺寸布局，历史窗口（flex-1 min-h-0）塌陷、自然高度的输入框被顶到面板
 * 上方 = 用户截图里「一闪而过」的错乱帧（DOM 抓不到，因为它只存在一帧）。
 *
 * 修法：切换开始（点击）的**同一帧**由 `openAgentSessionTab` 调 `begin(key)`，所有
 * 可见面板据此只渲染 loading；目标面板历史就绪后 `end(key)`。切换窗口期里其他内容
 * 一概不可见 ⇒ 错乱帧从结构上不可能出现。这是切换流程自有的过渡 UI，不是某个组件的
 * 防御性判断。
 *
 * key 与 tab 逻辑键同构：`agent:<channel>:<chatID>`（见 useTabManager.tabLogicalKey）。
 */
export type SessionSwitchState = { key: string } | null

let state: SessionSwitchState = null
const listeners = new Set<() => void>()
const emit = (): void => {
  for (const l of listeners) l()
}

export const sessionSwitch = {
  /** 切换开始：同帧生效（点击处理器里同步调用）。重复 begin 同一 key 是 no-op。 */
  begin(key: string): void {
    if (state?.key === key) return
    state = { key }
    emit()
  },
  /** 切换结束：仅目标 key 的面板（历史就绪）调用。key 不匹配是 no-op（防旧面板误清）。 */
  end(key: string): void {
    if (state?.key !== key) return
    state = null
    emit()
  },
  /** useSyncExternalStore 订阅。 */
  subscribe(listener: () => void): () => void {
    listeners.add(listener)
    return () => {
      listeners.delete(listener)
    }
  },
  /** 快照（引用稳定，仅在 begin/end 时变化）。 */
  get(): SessionSwitchState {
    return state
  },
}
