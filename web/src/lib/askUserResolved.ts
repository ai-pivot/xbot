/**
 * ask_user_resolved → 用户可见提示（G-B20261030-01 / D-03 契约的消费端）。
 *
 * 背景：`ask_user` 卡在三种情况下都会从会话里消失 —— 用户回答了、用户主动
 * 取消（/cancel、Stop）、服务端超过 pending TTL 无人回答而主动收回。前两种是
 * 用户自己的动作，不需要解释；**超时收回**必须说出来，否则用户只看到卡片
 * 凭空消失，不知道可以重新发起。
 *
 * 服务端已经把这个区别放在 wire 上了（protocol/events.go 的
 * AskUserResolvedEvent.CancelReason，wire 键 `cancel_reason`）：
 *   reason === 'cancelled' + cancel_reason === 'ttl_expired' → 超时收回
 *   reason === 'cancelled' + 无 cancel_reason（或未知值）     → 主动取消
 *   reason === 'answered' | 'rewound' | 'cleared'            → 不是"收回"，不提示
 *
 * 文案只从 i18n 实例取（zh-CN / en / ja 同键），本模块不硬编码任何中文。
 */
import i18n from '@/i18n'
import type { WSMessage } from '@/types/shared'

/** 服务端「超过 pending TTL 无人回答」的 cancel_reason 值（D-03）。 */
export const TTL_EXPIRED_CANCEL_REASON = 'ttl_expired'

/** i18n 键：超时收回 / 主动取消。两键必须给出**不同**文案（用户能分辨）。 */
export const ASK_USER_RESOLVED_TTL_KEY = 'agent.askUserResolvedTtlExpired'
export const ASK_USER_RESOLVED_CANCELLED_KEY = 'agent.askUserResolvedCancelled'

/**
 * 这条 resolved 事件该不该给用户提示？
 * 只有 `reason === 'cancelled'`（提问被收回）才提示；
 * answered / rewound / cleared 都是用户或会话自己的动作，不打扰。
 */
export function shouldTipAskUserResolved(
  msg: Pick<WSMessage, 'type' | 'reason'>,
): boolean {
  return msg.type === 'ask_user_resolved' && msg.reason === 'cancelled'
}

/**
 * 收回原因 → 文案键。
 * `ttl_expired` 之外的任何值（包括字段缺失、未知的将来值）都走主动取消文案
 * —— 宁可少说，不可把"我取消的"说成"超时"。
 */
export function askUserResolvedTipKey(cancelReason?: string | null): string {
  return cancelReason === TTL_EXPIRED_CANCEL_REASON
    ? ASK_USER_RESOLVED_TTL_KEY
    : ASK_USER_RESOLVED_CANCELLED_KEY
}

/**
 * 收回原因 → 实际文案。语言跟随宿主 i18n 实例（订阅由调用方的 React 渲染负责
 * / 本函数在事件到达时取值，切换语言后的下一条事件即用新语言）。
 */
export function askUserResolvedTip(
  cancelReason?: string | null,
  t: (key: string) => string = (key) => i18n.t(key),
): string {
  return t(askUserResolvedTipKey(cancelReason))
}
