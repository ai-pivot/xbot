# xbot-genui — GenUI (display_html) 插件

将 xbot 内置的 `display_html`（genui）改造成**独立 Go stdio channel 插件**。

## 功能

LLM 通过 `display_html` 工具生成交互式 React UI，前端以流式预览渲染，支持：

- **组件库**：`XBOT_UI.Button/Card/Table/Stat/Sparkline/Progress/Badge/Tabs/Modal/Form/Toast`
- **图表**：`<XBOT_UI.Chart option={...}>`（ECharts，CDN 惰性加载）
- **3D**：`XBOT_UI.useThreeScene`（three.js，CDN 惰性加载）
- **动画**：`XBOT_UI.motion`（framer-motion）
- **主题**：light/dark 自动适配（`dark:` variants）
- **交互**：`data-action` → agent 回传（默认）或插件拦截（可选）

## 架构

```
LLM 生成 TSX
  → display_html 工具（本插件 channel_tools 声明，channels:["web"]）
  → execute_tool RPC → 校验 → {content, is_error, ui_code}
  → xbot ChannelToolBridge：ui_code → genui 消息推前端 + Detail 存历史
  → 前端 GenUIBlock + XBOT_UI 运行时渲染
  → data-action 点击 → genui_action → agent loop
```

**通用性**（见 `docs/agent/genui-plugin-design.md` §9）：本插件通过工具 UI 元数据
（`ui: {mode:"genui", param:"code", libs:[...]}`）声明能力。主仓库无任何硬编码工具名 ——
任何插件工具声明 `ui.mode="genui"` 即获得流式提取 + fancy 渲染 + 交互回传。

## 构建 / 安装

```bash
# 构建（零依赖，独立二进制）
make build

# 安装到用户插件目录 + 复制 plugin.json
make install
# → ~/.xbot/plugins/xbot.genui/

# 热重载
tui_control(action="reload_plugins")
```

## 协议

- `activate` → 返回 `channel_provider {name:"genui"}`
- `channel_config` → 发送 `channel_tools` 声明（display_html，channels:["web"]，ui 元数据）
- `execute_tool` → 校验 TSX（App 存在 / 括号平衡 / 非空渲染）→ 返回 `ui_code`
- `web_ui_action` → 默认 no-op（交互回传 agent loop）

## 测试

```bash
go test ./...
```
