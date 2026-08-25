---
title: "热加载"
weight: 11
---

热加载在无需刷新页面的前提下，用最新代码替换运行中的插件。生命周期由后端 WS 消息驱动，由三个机制保证：先卸载后激活、module-map 缓存穿破、按插件清理。

## 生命周期流程

```
后端检测到变更
        │
        ▼
WS web_plugin_init（decl + module_url）
        │
        ▼
PluginRuntimeBootstrap handler
        ├─ bumpPluginLoadToken(decl.id)     ← 缓存穿破（新 import URL）
        ├─ toManifest(decl)                  ← 构造类型化 manifest
        └─ runtime.activate(manifest, module_url)
             ├─ registry.isActive(id)?  → deactivate(id)   ← 先卸载旧实例
             ├─ loadPluginModule(versionedUrl(url, version))
             ├─ registry.registerPlugin()    ← 单一门控校验 + 挂载
             │     └─ 贡献点挂载失败 → 贡献点级回滚，
             │        插件标记 'error'，已挂载 disposer 全部释放
             ├─ buildContext(perms, services)
             ├─ mod.activate(ctx)            ← activate 返回函数作为 disposer
             └─ plugins.notifyActivated(id)
```

卸载（`web_plugin_deactivate` WS 消息 → `runtime.deactivate(id)`）：

```ts
deactivate(pluginId: string): boolean {
  const removed = this.registry.unregisterPlugin(pluginId)   // disposables 逆序执行
  if (removed) {
    this.events.unsubscribePlugin(pluginId)      // 移除全部事件订阅
    this.commands.removePlugin(pluginId)         // 移除命令 + 快捷键
    this.plugins.notifyDeactivated(pluginId)     // 互操作消费方降级
    this.modules.delete(pluginId)
    // 清掉视图缓存（热加载后重新加载模块）
    for (const key of [...this.viewCache.keys()]) {
      if (key.startsWith(pluginId)) this.viewCache.delete(key)
    }
  }
  return removed
}
```

## 缓存穿破 —— load-token 机制

浏览器 ES module map 以完整 URL 为缓存键。URL 不变时，重载的插件持续命中缓存模块（连网络请求都不发）——磁盘更新多少次，前端永远跑旧代码。`usePluginRuntimeHost.ts` 维护 per-plugin load token，每次 activate 时 bump：

```ts
const pluginLoadTokens = new Map<string, string>()

function bumpPluginLoadToken(pluginId: string): void {
  pluginLoadTokens.set(pluginId, Date.now().toString(36))
}

// 在 loadPluginViewComponent 中：
const token = pluginLoadTokens.get(pluginId)
const bust = token ? `&_t=${token}` : ''
const mod = await import(/* @vite-ignore */ `${url}?view=${encodeURIComponent(view.id)}${bust}`)
```

主模块加载使用 `versionedUrl(url, manifest.version)`（`?v=<version>`）。两者共同保证：重载 → URL 变化 → module map miss → 网络请求 → 拿到磁盘上的最新代码。同一会话内不重载则 token 不变 → URL 稳定 → module map 命中（不重复请求）。

## 卸载顺序保证

- **Disposables 按注册逆序执行**（`unregisterPlugin` 迭代 `disposables.splice(0).reverse()`），单个 disposer 抛错不中止其余。
- **事件订阅**由 `unsubscribePlugin` 批量移除（订阅时记录了插件归属）。
- **命令与快捷键**由 `removePlugin` 移除。
- **视图**经 `notifyViewsChanged` → `usePluginViewPanels` 重算从宿主侧栏消失，布局项由 `PluginRuntimeBootstrap` 的同步 effect 注销。
- **互操作消费方**收到 `onDeactivated` 通知降级。

## 贡献点级回滚

挂载某个贡献点中途抛错时，registry 回滚该插件**已挂载**的贡献点并标记 `error`——宿主永远看不到半挂载的插件：

```ts
for (const c of manifest.contributes) {
  try {
    this.mount(manifest, c, disposables)
  } catch (error) {
    for (const d of disposables.splice(0).reverse()) {
      try { d() } catch { /* ignore */ }
    }
    this.plugins.delete(manifest.id)
    record.state.status = 'error'
    record.state.error = error instanceof Error ? error.message : String(error)
    this.hooks.onStateChange?.({ ...record.state })
    return { ok: false, error: String(error) }
  }
}
```

## 渲染期崩溃隔离

`PluginView` 把每个插件视图包在 `PluginViewErrorBoundary` 里（`web/src/plugin-runtime/PluginView.tsx`）：渲染异常只在 tab 内显示错误占位（message + componentStack 内联渲染，方便截图诊断）——**绝不**卸载整棵树。渲染器派发（`renderTool`）同样捕获渲染器崩溃并回退默认渲染。

## 内置插件

`activateBuiltin(manifest, mod)` 与第三方插件走同一条门控/校验/激活路径——内置插件无特权差异（自举纪律）。唯一区别是模块来源：静态 import vs URL 加载。

## 手动触发点

- `PluginRuntimeBootstrap` 启动：拉 `web_plugin_list`（带 `rescan: true`）并激活 enabled 插件。**禁用插件必须跳过**——否则禁用后的前端插件仍注册 view 并生效。
- WS `web_plugin_init`：重载单个插件（bump token + activate）。
- WS `web_plugin_deactivate`：按 id 卸载。
- 插件管理面板：启用/禁用/重载操作**主动**同步前端运行时（WS 广播不可靠；见[插件管理面板](plugin-manager.md)）。
