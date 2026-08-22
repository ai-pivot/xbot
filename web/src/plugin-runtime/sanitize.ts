/**
 * SafeMessage 裁剪——把内部消息模型转换为插件可见的安全副本。
 *
 * 只保留公共字段（id/turnID/role/content/createdAt），丢弃内部状态
 * （persisted/eventSeq/dbID 等）。供 StateAPI 与事件载荷使用。
 */
import type { SafeMessageFactory } from '@/plugin-api'

/** 内部消息模型（web/src/types/agent.ts 的 ChatMessage 结构，仅取公共字段）。 */
interface InternalMessageLike {
  id?: number
  dbID?: number
  turnID?: number
  role?: string
  content?: string
  createdAt?: string | number
}

/** 实现 SafeMessageFactory：内部消息 → 安全副本。 */
export const toSafeMessage: SafeMessageFactory = (m) => {
  const raw = (m ?? {}) as InternalMessageLike
  const role = raw.role === 'assistant' || raw.role === 'user' || raw.role === 'system' || raw.role === 'tool'
    ? raw.role
    : 'system'
  return {
    id: typeof raw.id === 'number' ? raw.id : typeof raw.dbID === 'number' ? raw.dbID : 0,
    turnID: typeof raw.turnID === 'number' ? raw.turnID : 0,
    role,
    content: typeof raw.content === 'string' ? raw.content : '',
    createdAt: typeof raw.createdAt === 'string' ? raw.createdAt : '',
  }
}
