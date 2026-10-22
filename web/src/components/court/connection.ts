/**
 * 通信视图「连接状态」与「有无数据」的语义分离。
 *
 * 2026-10-22 用户报障：通信视图永远显示「离线」，永远什么都看不到。
 * 根因（前端侧）：`CourtConsole.load()` 把「0 个可见群组 / 0 条消息」当成
 * 错误 throw 出去，而顶栏把**任何** error 渲染成「离线」。
 * 于是只要后端正常但没有数据（生产库 groups 表为空），面板就永远「离线」。
 *
 * 本模块把这两个概念拆开：
 *   - conn：只有**请求本身失败**（网络/HTTP/鉴权）才是 offline；
 *   - emptyReason：在线但没有数据时的原因文案（不是错误）。
 */
export type CourtConn = 'loading' | 'online' | 'offline'

export interface CourtStatus {
  conn: CourtConn
  /** 仅在 offline 时非空：导致断连的真实错误。 */
  error: string | null
  /** 仅在 online 且无数据时非空：可展示的空态原因。 */
  emptyReason: string | null
}

/** 在线但无数据的原因；有数据返回 null。 */
export function emptyReasonFor(groupCount: number, messageCount: number): string | null {
  if (groupCount === 0) return '当前账号还没有可见群组（需先在群里发过言/被加入群）'
  if (messageCount === 0) return '本群在当前会话视角还没有消息'
  return null
}

/** 顶栏状态徽标文案。 */
export function statusLabel(status: Pick<CourtStatus, 'conn'>, demo = false): string {
  if (status.conn === 'loading') return '加载中…'
  if (status.conn === 'offline') return '离线'
  if (demo) return '预览'
  return '已连接'
}

/** 徽标 hover 提示：断连说原因，在线说空态原因或「已连接服务端」。 */
export function statusTitle(status: CourtStatus, demo = false): string {
  if (status.conn === 'offline') return status.error ?? '离线'
  if (status.conn === 'loading') return '正在连接服务端'
  if (demo) return '预览数据（/court-demo）'
  return status.emptyReason ?? '已连接服务端'
}
