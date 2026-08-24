/**
 * 插件配置 API（§3.8）——类型化读写插件自己的配置。
 *
 * 前端插件声明 `permissions: ['config']` 后，`ctx.config` 可用：
 * `get()` 读取合并后的配置值，`set()` 持久化单个键并触发后端热重载
 * （Go/stdio 插件走 context 订阅、script 读 env、前端插件经 onConfigChange）。
 *
 * 宿主（设置面板）不走本 API —— 它通过 `plugin.get_config` RPC 拿 schema + 值
 * 并直接渲染表单。
 */
import type { Disposable } from './manifest'

/** 单个配置属性的 schema（后端 ConfigProperty 的镜像）。 */
export interface PluginConfigProperty {
  type: 'boolean' | 'string' | 'number' | 'select' | 'multiselect'
  label: string
  description?: string
  default?: unknown
  options?: Array<{ label: string; value: string }>
  /** 分组名：同一 section 的属性归为一组。 */
  section?: string
  /** 敏感值：UI 中以掩码输入框展示。 */
  secret?: boolean
  /** 文本输入框占位符提示。 */
  placeholder?: string
  required?: boolean
  minimum?: number
  maximum?: number
}

/** 插件配置 schema（标题 + 属性表）。 */
export interface PluginConfigSchema {
  title?: string
  properties: Record<string, PluginConfigProperty>
}

/** 插件可用的配置能力。 */
export interface ConfigAPI {
  /** 读取当前合并后的配置值（默认值 + 用户覆盖）。 */
  get(): Promise<Record<string, unknown>>
  /** 设置单个配置键并持久化。后端广播变更，触发所有 onConfigChange 订阅者。 */
  set(key: string, value: unknown): Promise<void>
  /** 订阅配置变更（含其它客户端/标签页发起的修改）。返回 disposable。 */
  onConfigChange(handler: (config: Record<string, unknown>) => void): Disposable
}
