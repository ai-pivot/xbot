---
title: "ESM 模块格式"
weight: 10
---

Web 插件是编译后的 ESM 模块，托管在 `/plugins/<id>/web/` 下，经动态 `import()` 加载。加载契约定义于 `web/src/plugin-runtime/loader.ts`。

## 模块形状

```ts
/** 插件模块导出形状（与类型包的插件契约一致）。 */
export interface PluginModule {
  manifest: PluginManifest
  activate?: (ctx: unknown) => void | Promise<void> | (() => void)
  deactivate?: () => void
  /** Exports API：命名导出即公共 API（互操作）。 */
  [key: string]: unknown
}
```

- `manifest` —— 模块导出的 manifest **覆盖**后端下发的声明（"声明即契约"；`activateModule` 使用 `mod.manifest ?? manifest`）。
- `activate(ctx)` —— 单一门控校验 + 贡献点挂载后调用一次。可返回清理函数（即 `Disposable`）。
- `deactivate` —— 保留；卸载由运行时的 disposable 链处理。
- 命名导出 —— 除 `manifest`/`activate`/`deactivate` 外全部成为公共 API（见[插件互操作](interop.md)）。
- `commandHandlers` —— 特殊命名导出：`{ [commandId]: (args) => void }`，解析贡献点声明的命令。

## 构建契约

插件以 ESM bundle 构建。参考实现（`xbot.git-fancy`，见 `web/src/plugins/git-fancy/index.tsx`）使用：

```
esbuild --bundle --splitting --format=esm --jsx=transform   （React external）
```

约束：

- **React 取自 `window`** —— 插件模块不得 import 宿主内部模块；React 从 window 全局取（宿主的 React 运行时实例）。git-fancy 插件 `import { React } from './shared'`，`shared.tsx` 读 `window.React`。
- **多入口插件必须 `--splitting`** —— `activate(ctx)` 在**主入口**执行并把 `rpc`/`ui` 单例注入共享 chunk。不 splitting 时每个 view entry 打包独立副本——注入对其他 view 不可见（单入口 bundle 给每个 view 独立实例）。共享 chunk 相对路径无 query → ESM 缓存同一实例。
- **Hooks 纪律** —— 绝不在条件提前 return 之后放置 hook（loading/error 分支）：loading→loaded 切换改变 hooks 数量（7→8）触发 React #310（"Rendered fewer hooks than expected"）。

## 版本化 URL

```ts
/** 版本化 URL：/plugins/<id>/web/... 拼接 ?v=<hash>。 */
export function versionedUrl(baseUrl: string, version: string, hash?: string): string {
  const sep = baseUrl.includes('?') ? '&' : '?'
  const v = hash ?? version.replace(/[^\w.-]/g, '_')
  return `${baseUrl}${sep}v=${encodeURIComponent(v)}`
}

/** 加载插件模块（动态 import）。失败抛错（含模块语法错误、网络错误）。 */
export async function loadPluginModule(entryUrl: string): Promise<PluginModule> {
  const mod = await import(/* @vite-ignore */ entryUrl)
  return mod as PluginModule
}
```

浏览器 ES module map 以完整 URL 为缓存键——`?v=` 穿破缓存正是热加载真正生效的机制（见[热加载](hot-reload.md)）。

## View entry 解析

view 的 `entry` 相对 `/plugins/<pluginId>/web` 解析：

```ts
const base = `/plugins/${pluginId}/web`
const url = view.entry.startsWith('/') ? view.entry : `${base}/${view.entry}`
```

`PluginRuntime.loadViewComponent` 的解析优先级：

1. **已激活模块的命名导出** —— `mod[view.id]`（多视图单模块是单例最优布局）。
2. **主模块 default 导出** —— `view.entry == null || view.entry === manifest.entry` 时，`mod.default` 充当视图。复用已激活的模块实例很重要：`activate()` 已在其上执行并注入 `ctx.rpc` 等；换不同 URL（`?view=`）重新 import 会创建**第二个模块实例**，其模块级状态（如 `rpc`）未初始化——视图会显示"插件未初始化"。
3. **宿主侧 import** —— 多入口插件的其他 entry（如 git-fancy 的 `diff.js`/`commit.js`）走 `host.loadViewComponent`（`usePluginRuntimeHost.ts`），动态 import `?view=<viewId>` + 重载 token。

只接受函数组件或带合法 `$$typeof` 的对象（memo/forwardRef）——裸对象会让 React 抛 "Element type is invalid… got: object"。

## `builtin:` 视图

`entry` 以 `builtin:` 开头的视图解析为随主 bundle 分发的静态 import 组件（`usePluginRuntimeHost.ts` 的 `builtinViews` map）：

```ts
builtinViews.set('xbot.plugin-manager.panel', PluginManagerPanel)
builtinViews.set('git-info.status', GitStatusPanel)
```

内置视图必须**静态 import**，禁止动态 `import()`——动态 import 让 rolldown 把它们打成独立 chunk，并把 `useState`/`useEffect` 符号错误绑定到 `vendor-framer-motion` 的导出，触发 React #311 整屏黑屏。静态 import 使它们留在主 bundle。

## 托管与安全

后端静态托管（`channel/web/web.go` `handlePluginStatic`）：

- 插件 ID 必须匹配 `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`（`isValidPluginIDForServe`）。
- 子路径 clean 后必须落在 `<pluginDir>/<id>/web/` 内（防路径穿越）。
- `SetPluginDirs` 注入 `plugin.DefaultPluginDirs(config.XbotHome())`。
