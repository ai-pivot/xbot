---
title: "xbot-genui — GenUI（display_html）"
weight: 2
---

GenUI 插件提供 `display_html` 工具：LLM 编写一个自包含的 TSX 组件，Web 前端将其渲染为实时、流式、可交互的 UI——包含组件库、ECharts 图表、three.js 3D 场景和 framer-motion 动画。

它是一个 **channel 插件**：独立、零依赖的 Go 二进制（`plugins/xbot-genui/`），通过 stdio 上的 JSON 协议驱动，经 `channel_tools` 协议向 `web` channel 声明工具。

## 功能

- **组件库**——`XBOT_UI.Button / Card / Table / Stat / Sparkline / Progress / Badge / Tabs / Modal / Form / Toast`
- **图表**——`<XBOT_UI.Chart option={...}>`（ECharts，CDN 惰性加载）
- **3D**——`XBOT_UI.useThreeScene`（three.js，CDN 惰性加载）
- **动画**——`XBOT_UI.motion`（framer-motion）
- **主题**——light/dark 自动适配（`dark:` Tailwind variants）
- **交互**——`data-action` 属性将点击回传给 agent（`🖱️ [UI Action] ...`）；纯客户端状态用 React hooks（`useState`、`useEffect` 等）
- **流式预览**——工具执行期间 TSX 即被推送到前端；渲染代码存入迭代历史（刷新后仍存在）
- **Surface 面板**——以顶层面板渲染（fancy 标题栏 + 折叠 + 全屏，默认展开），而非折叠在工具结果列表中

## 安装

```bash
cd plugins/xbot-genui
make build          # → bin/genui-plugin（零依赖独立二进制）
make install        # → ~/.xbot/plugins/xbot.genui/
```

或在仓库根目录执行：`make plugins-install`。

不安装、直接从 checkout 运行：

```bash
make plugins-build
XBOT_PLUGIN_DIRS="$(pwd)/plugins" xbot
```

## 配置

插件在 `plugin.json` 中声明 channel 配置 schema：

| 键 | 类型 | 默认值 | 说明 |
|----|------|--------|------|
| `enabled` | toggle | `true` | 启用 GenUI channel 插件（向 web channel 注册 `display_html`） |
| `libs_cdn` | text | `https://cdn.jsdelivr.net/npm/` | 惰性加载图表/3D 库（echarts/three）的基础 URL |

**激活需要 `config.json` 中的 channel 条目**——仅安装插件不够：

```json
{
  "channels": {
    "genui": { "enabled": "true" }
  }
}
```

`stdioChannelPluginProvider.IsEnabled` 对 nil 配置返回 false，因此没有该
条目时 channel 永远不会创建、`channel_tools` 永远不会声明、`display_html`
不可见。修改配置后需重启 xbot（channel 实例在 `registerChannels` 启动时
创建）。

## 架构

```text
LLM 生成 TSX
  → display_html 工具（channel_tools 声明，channels:["web"]）
  → execute_tool RPC → 校验 → {content, is_error, ui_code}
  → xbot ChannelToolBridge：ui_code → 前端 genui 消息 + Detail 存历史
  → 前端 GenUIBlock + XBOT_UI 运行时渲染
  → data-action 点击 → genui_action → agent loop
```

### 协议（stdio 上的 JSON lines）

| 方向 | 消息 | 含义 |
|------|------|------|
| xbot → 插件 | `{"method":"activate",...}` | 返回 `channel_provider {name:"genui"}` |
| xbot → 插件 | `{"type":"channel_config",...}` | channel 已上线——插件回复 `channel_tools` |
| xbot → 插件 | `{"id","method":"execute_tool",...}` | 校验 TSX，返回 `ui_code` |
| xbot → 插件 | `{"id","method":"web_ui_action",...}` | no-op 回复——交互落到 agent loop 处理 |
| 插件 → xbot | `{"type":"channel_tools","tools":[...]}` | 向 `web` channel 声明 `display_html` |

### 工具声明

工具携带 **UI 元数据**（`tools.UIDecl`）而非依赖工具名——这是通用性设计
（见 `docs/agent/genui-plugin-design.md` §9）：

```json
{
  "name": "display_html",
  "channels": ["web"],
  "ui": {
    "mode": "genui",
    "param": "code",
    "libs": ["echarts", "three", "motion"],
    "surface": { "kind": "panel", "collapsible": true, "fullscreen": true, "default_open": true }
  }
}
```

任何声明 `ui.mode="genui"` 的插件工具都能获得同样的流式提取、fancy 渲染和
交互回传——xbot 本身没有任何硬编码的工具名。

### 校验（execute_tool）

返回 `ui_code` 之前，插件校验 LLM 生成的 TSX（这些检查迁移自已删除的
`tools/display_html.go`）：

1. `code` 参数必须非空
2. 剥离 markdown 围栏（` ```tsx ... ``` `）
3. 代码必须定义 `App` 组件
4. 括号/圆括号平衡检查（`validateSyntax`）——轻量扫描器，跳过字符串、
   模板字面量和注释
5. 空渲染守卫（`isEmptyRender`）——拒绝 `return null` / `undefined` /
   `false` / `<></>`

成功时返回 `content: "🎨 UI rendered (N chars)"` 和完整 `ui_code`。失败时
返回 `is_error: true` 及可操作的错误信息，供 LLM 修复后重试。

### 前端运行时

- `web/src/genui/runtime.tsx`——`XBOT_UI` 组件库、ECharts / three / motion
  集成、主题处理
- `builtinGenuiRenderer`（匹配 `{uiMode:'genui'}`）经
  `PluginRuntime.renderTool` 调度器（`messageRenderer`）渲染工具结果
- ECharts/three/motion 从配置的 CDN base 惰性加载
- GenUI 交互事件（`genui_action`）回传到 agent loop（`InjectAsyncMessage`）
  ——插件本身不参与

## 测试

```bash
cd plugins/xbot-genui
go test ./...
```

## 源文件

| 文件 | 作用 |
|------|------|
| `main.go` | stdio 协议主循环、工具声明、TSX 校验 |
| `plugin.json` | Manifest：id `xbot.genui`、channelProvider 声明、配置 schema |
| `main_test.go` | 校验辅助函数的单元测试 |
| `Makefile` | build / test / install / clean 目标 |
