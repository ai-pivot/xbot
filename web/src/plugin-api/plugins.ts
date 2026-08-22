/**
 * 插件互操作（§3.7）——Exports API 与激活依赖。
 *
 * 插件的公共 API = 模块命名导出（保留 manifest/activate/deactivate 除外）。
 * 消费方用 `ctx.plugins.get/require` 获取；类型由 `PluginExportsMap` 声明合并扩展。
 */
import type { Disposable } from './manifest'

/** 空表；各插件发布类型包扩展（声明合并）。 */
// eslint-disable-next-line @typescript-eslint/no-empty-object-type -- 声明合并的合法空表，扩展点由各插件类型包填充
export interface PluginExportsMap {}

export interface PluginsAPI {
  /** 同步取已激活插件的公共 API；未激活/禁用/崩溃 → undefined。 */
  get<K extends keyof PluginExportsMap>(id: K): PluginExportsMap[K] | undefined
  /** 异步确保依赖插件激活后返回其 API（可选依赖的懒加载入口）。 */
  require<K extends keyof PluginExportsMap>(id: K): Promise<PluginExportsMap[K]>
  /** 订阅依赖插件的激活/停用（动态上下线时降级/恢复）。 */
  onActivated<K extends keyof PluginExportsMap>(id: K, h: (api: PluginExportsMap[K]) => void): Disposable
  onDeactivated<K extends keyof PluginExportsMap>(id: K, h: () => void): Disposable
}
