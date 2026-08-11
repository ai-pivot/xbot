# 计划：Web 插件系统 — 高度可扩展的 UI 组件平台

> 生成时间：2026-08-11
> 状态：待确认
> 基线：master @ 06e9ed1d

## 背景与目标

xbot 已有完整的 VSCode 风格插件系统（native/stdio/script/wasm 运行时、widget registry、channel plugin 协议），但 **widget 只推送到 CLI 客户端，Web 前端零消费**。同时 web 已有 agent 驱动的 `display_html`/`genui` 通道（LLM 写 TSX 渲染 UI），但这是 LLM 工具，不是插件协议。

**目标**：复用并扩展现有插件协议，让**插件**（而非 agent）能为 web 贡献炫酷 UI 组件，使 web 高度可扩展：

1. **复用**：WidgetRegistry / zone 概念 / ChannelPluginTransport 消息机制 / script plugin 触发机制 / StyleClass 语义
2. **扩展**：结构化 widget 数据（非 ANSI）、`web_ui` 组件声明协议、`web_ui_action` 交互回传、前端组件注册表
3. **安全**：组件白名单 + React 默认转义，不引入任意代码执行

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `channel/interfaces.go` | channel 能力接口（ProgressSender/SessionStateSender/UserMessageInjector） | 修改（新增 WidgetSubscriber 接口） |
| `plugin/plugin.go` | Plugin 接口、UISlotContribution、WidgetSpan、StyleClass | 修改（新增 WebComponent 类型） |
| `plugin/widget.go` | WidgetRegistry、RenderSessionWidgets（返回 ANSI） | 修改（新增结构化渲染） |
| `plugin/web_widget.go` | **新增**：WebWidgetSpan、WebComponent、WebUI 声明存储 | 新增 |
| `agent/transport_channel_plugin.go` | ChannelPluginTransport、handleChannelTools/Prompt | 修改（新增 handleChannelUI） |
| `agent/agent.go:1987-2025` | OnUpdated → PushPluginWidgetsPerSession（CLI-only 硬编码） | 修改（改为 channelRange 通用分发 + WidgetSubscriber 断言） |
| `channel/web/web_remote_cli.go` | RemoteCLIChannel.PushPluginWidgetsPerSession（Hub → "cli"） | 修改（迁移为 WidgetSubscriber 实现） |
| `channel/web/web.go` | WebChannel（SSE/WS 推送核心） | 修改（实现 WidgetSubscriber：SetWidgetRegistry + NotifyWidgetsUpdated + 增量检测） |
| `channel/web/web_hub.go:586` | isSSEEventType 白名单（已含 plugin_widgets） | 修改（新增 web_widgets） |
| `protocol/ws.go` | 消息类型常量 | 修改（新增 MsgTypeWebWidgets、MsgTypeWebUI） |
| `protocol/events.go` | 事件定义 | 修改（新增 PluginWebUIEvent） |
| `serverapp/rpc_table.go` | RPC 表（含 genui_action） | 修改（新增 web_ui_action） |
| `plugin/protocol/protocol.go` | stdio dispatch（activate/execute_tool/hook...） | 修改（新增 web_ui_action case） |
| `plugin/context.go` | PluginContext/UIContributor 接口 | 修改（新增 ContributeWebUI） |
| `serverapp/server.go` | channel 注册（registerChannels） | 修改（WidgetSubscriber 断言 + SetWidgetRegistry 注入） |
| `web/src/providers/sseConnection.ts` | SSE 消息分发（SSE_EVENT_TYPES 已含 plugin_widgets） | 修改（新增 web_widgets） |
| `web/src/types/shared.ts` | WSMessageType 枚举 | 修改（新增类型） |
| `web/src/plugins/` | **新增目录**：Provider/hooks/组件注册表/widget 组件 | 新增 |
| `web/src/layouts/AppShell.tsx` | 布局外壳 | 修改（挂载 widget zones） |
| `web/src/components/agent/AgentPanel.tsx` | 主面板（状态栏/标题栏） | 修改（挂载 zones） |

### 核心架构：渠道自订阅模式（WidgetSubscriber）

```
插件进程 (stdio/script)
  │  web_ui 声明 (channel plugin stdout)
  ▼
ChannelPluginTransport.handleChannelUI ──→ WidgetRegistry (zones + webComponents)
  │
  │ FireUpdated() (debounce 200ms)
  ▼
agent.go OnUpdated 回调（通用分发，channel-agnostic）
  │  channelRange：遍历所有注册 channel
  │  类型断言 channel.WidgetSubscriber
  ▼
┌──────────────┬──────────────┬──────────────┐
│ CLI channel  │ Web channel  │ 未来 channel │   ← 每个渠道自己决定
│  自订阅实现   │  自订阅实现   │  (QQ/Feishu) │     渲染格式 + 推送对象
└──────┬───────┴──────┬───────┴──────┬───────┘
       ▼              ▼              ▼
  ANSI 字符串     结构化 JSON     各自格式
  → plugin_widgets → web_widgets   (SSE)
  (现有,保持)      → 前端渲染
```

**接口定义**（`channel/interfaces.go`，与 ProgressSender 同模式）：

```go
// WidgetSubscriber 由想要接收插件 widget/UI 更新的 channel 实现。
// agent 在 WidgetRegistry 更新时统一通知，渠道自己决定渲染格式与推送对象。
type WidgetSubscriber interface {
    // SetWidgetRegistry 注入 widget 渲染能力（channel 创建时调用一次）
    SetWidgetRegistry(wr *plugin.WidgetRegistry)
    // NotifyWidgetsUpdated 通知渠道："widget 内容更新了"。
    // 渠道自行决定：渲染哪些 chatID、用 ANSI 还是结构化 JSON、是否增量推送。
    NotifyWidgetsUpdated()
}
```

**agent 侧唯一改动**（`agent.go` OnUpdated 全量替换为通用分发）：

```go
pm.WidgetRegistry().OnUpdated(func() {
    agent.channelRange(func(ch channel.Channel) {
        if ws, ok := ch.(channel.WidgetSubscriber); ok {
            ws.NotifyWidgetsUpdated()
        }
    })
})
```

**channel 创建处注入**（`serverapp/server.go` registerChannels）：

```go
if ws, ok := ch.(channel.WidgetSubscriber); ok {
    ws.SetWidgetRegistry(pm.WidgetRegistry())
}
```

> 该模式与现有 `channel.ProgressSender`（进度推送）、`channel.SessionStateSender`（会话状态）完全一致：
> agent 只做能力断言 + 通知，渠道负责推送细节。CLI 现有行为零改动（ANSI 路径保留在
> RemoteCLIChannel 的 WidgetSubscriber 实现中），Web 新增结构化路径，未来新渠道实现接口即可。

### 现有协议能力（可复用）

| 机制 | 现状 | 复用方式 |
|------|------|----------|
| WidgetRegistry | zone→slot 注册、优先级、refresh/trigger | 直接复用 |
| StyleClass | normal/dim/accent/success/warning/error/info/muted/raw | 映射 Tailwind 语义色 |
| ChannelPluginTransport | channel_tools/channel_prompt 热更新声明 | 扩展 web_ui 消息 |
| script plugin | 脚本输出 style\|text → WidgetSpan | 复用（web 端结构化渲染） |
| genui_action RPC | agent UI 交互回传 → injectAsyncMessage | 参考模式（路由到插件） |
| SSE 分发 | isSSEEventType 已含 plugin_widgets | 扩展新类型 |

### 风险点

- **XSS**：插件 props/span 文本可含 HTML/JS → 全部走 React 文本节点转义；iframe custom 组件强制 sandbox
- **SSE 高频推送**：widget 高频更新挤占 512 ring buffer → 复用 CLI 增量检测（内容 diff + revision 号）
- **dockview 独立 React root**：widget 面板需经 DockviewContext 桥接注入 ws 连接
- **CLI 兼容**：ANSI 渲染路径完全保留，web 新增独立结构化路径，互不影响
- **多 session 隔离**：沿用 getCWD/chatID 机制，widget 按 session 渲染
- **与 genui 共存**：genui = agent LLM 驱动（临时 UI）；插件 UI = 声明式组件（持久 UI），互补
- **热更新**：web_ui 声明覆盖式替换（同 channel_tools 语义）

## 协议设计

### 1. 结构化 Web Widget 数据（阶段一）

```go
// plugin/web_widget.go
type WebWidgetSpan struct {
    Text  string `json:"text"`
    Style string `json:"style,omitempty"` // normal|dim|accent|success|warning|error|info|muted
    Icon  string `json:"icon,omitempty"`  // lucide-react icon name
    Href  string `json:"href,omitempty"`
}

// 结构化渲染入口（web 专用，CLI 继续用 RenderSessionWidgets）
func RenderSessionWebWidgets(wr *WidgetRegistry, getCWD func(string) string, chatID string) map[string][]WebWidgetSpan
```

### 2. `web_ui` 组件声明协议（阶段二，plugin → xbot → 前端）

```json
{"type":"web_ui","ui":[
  {
    "widget_id":"ci-monitor",
    "title":"CI Monitor",
    "slot":"right_sidebar",        // status_bar_left/right | info_bar | title_bar_left/right | right_sidebar | panel | tool_hint
    "refresh":"30s",               // 轮询间隔
    "triggers":["PostToolUse:Shell*"],
    "component":{
      "type":"sparkline",
      "props":{"data":[1,5,3,8,2],"color":"#22c55e","height":48}
    }
  }
]}
```

**组件类型 v1 白名单**：

| type | 用途 | 核心 props |
|------|------|-----------|
| `badge` | 状态徽章 | text, tone(success/warning/error/info/muted/accent), pulse? |
| `progress` | 进度条 | value, max, tone, label |
| `metric` | 指标卡 | label, value, delta, icon, trend |
| `sparkline` | 迷你走势图 | data[], color, height, type(line/bar) |
| `table` | 数据表 | columns[], rows[], max_height |
| `list` | 键值列表 | items[{key,value,tone}], title |
| `markdown` | 富文本 | content (GFM) |
| `code` | **自由代码（完全自主）** | tsx/ts/js 源码 或 html+js 源码（sucrase 编译 + iframe 沙箱） |
| `custom` | 外部 URL 沙箱 | src(可信 http(s) URL), height, sandbox |

> **两种自由度层次并存**：简单场景用声明式组件（8 种白名单，开箱即用）；复杂场景用 `code`/`custom` 自由代码模式（完全自主）。声明式组件本质是 `code` 模式的受控子集，渲染实现统一走 SandboxedUI。

### 3. `web_ui_action` 交互协议（阶段三）

```json
// 前端 → 后端
{"method":"web_ui_action","params":{
  "chat_id":"/home/x",
  "widget_id":"ci-monitor",
  "action":"refresh",
  "data":{"page":2}
}}

// 后端 → 插件（ChannelPluginTransport.Call，30s 超时）
{"id":"srv-N","method":"web_ui_action","params":{...同上...}}

// 插件响应 → 新组件状态（推送到前端）或副作用
{"id":"srv-N","result":{"component":{"type":"table","props":{...}}}}
```

### 4. 自由代码模式（完全自主 — 核心能力）

**目标**：插件作者**完全自主**地编写贡献区域的源代码——任意 HTML/CSS/JS/TSX，不受组件白名单限制。

**可行性依据**：`web/src/components/agent/GenUIBlock.tsx`（display_html 渲染器）已实现生产级机制：
- `sucrase` 编译 TSX→JS（支持 TypeScript + JSX，strip imports/exports，React hooks 注入）
- **iframe 内独立 React root**（`createRoot(iframe.contentDocument.body)`）——代码与父页面 DOM 完全隔离
- 编译缓存（hash + LRU 8 项）、streaming 节流（100ms）、错误边界、ResizeObserver 高度自适应
- `data-action` 点击委托 → `onAction` → RPC 回传
- 滚轮事件 postMessage 转发（`genui_wheel`）

**复用方案**：将 `GenUIBlock` 泛化为通用 `SandboxedUI` 组件（`web/src/plugins/SandboxedUI.tsx`），插件 `code`/`custom` 模式复用同一渲染管线，仅替换 action 回传路由（→插件而非 agent）。

**`code` 模式声明格式**：

```json
{"type":"web_ui","ui":[
  {"widget_id":"my-dashboard","title":"我的面板","slot":"panel",
   "code": "export default function App(){\n  const [n,setN]=useState(0)\n  return <div className=\"p-4\" onClick={()=>setN(n+1)}>Count: {n}</div>\n}"}
]}
```

**`custom` 模式（外部 URL）**：

```json
{"widget_id":"grafana","title":"Grafana","slot":"panel",
 "component":{"type":"custom","props":{"src":"https://monitor.internal/d/abc","height":600}}}
```

**iframe → 宿主 postMessage 桥协议**（消息白名单）：

| 消息类型 | 方向 | 载荷 | 用途 |
|---------|------|------|------|
| `ui_action` | iframe→宿主 | `{action, data, widget_id}` | 点击/输入交互 → `web_ui_action` RPC → 插件 |
| `ui_resize` | iframe→宿主 | `{height}` | 高度自适应（复用 ResizeObserver 模式） |
| `ui_wheel` | iframe→宿主 | `{deltaY}` | 滚轮透传（复用 genui_wheel） |
| `ui_ready` | iframe→宿主 | `{}` | 组件就绪信号（可触发数据推送） |
| `ui_data` | 宿主→iframe | `{widget_id, data}` | 注入实时数据（token 用量/进度/自定义），插件代码经 `window.addEventListener('message')` 接收 |

**安全边界**：
- iframe `sandbox="allow-scripts allow-same-origin"`（与 GenUIBlock 一致，React 19 createRoot 需要 same-origin）
- 插件代码**无法访问父页面 DOM/Cookie/token**（同源策略天然隔离）——即使插件代码有 bug 或被注入，也摸不到 xbot 数据
- `src` URL 白名单校验：仅 http(s) 可信源，拒绝 file:/javascript:/data:
- `code` 模式禁用外部 import（沿用 GenUIBlock strip imports 策略；需要库时用 `custom` 模式或 CDN 内联）
- 插件信任模型：插件本就有 `execute_tool` 可执行任意命令，iframe 隔离是**防御纵深**（防插件 UI 代码 XSS 父页面），不是信任基础

### 5. 前端架构

```
web/src/plugins/
├── PluginWidgetProvider.tsx   // Context + useSyncExternalStore（沿用项目模式）
├── usePluginWidgets.ts        // hook：订阅 SSE web_widgets 消息
├── registry.tsx               // type → React 组件 映射表（可扩展）
├── api.ts                     // web_ui_action RPC 调用
├── SandboxedUI.tsx            // 泛化自 GenUIBlock：code/custom 自由代码 iframe 沙箱渲染
├── WidgetZone.tsx             // 按 slot 渲染 spans（text+style→Tailwind）
├── WidgetPanel.tsx            // dockview 面板（slot=panel 的大组件容器）
└── components/
    ├── BadgeWidget.tsx        ├── ProgressWidget.tsx
    ├── MetricWidget.tsx       ├── SparklineWidget.tsx
    ├── TableWidget.tsx        ├── ListWidget.tsx
    ├── MarkdownWidget.tsx     └── (code/custom 走 SandboxedUI)
```

**slot → 布局映射**：

| slot | 渲染位置 |
|------|---------|
| `title_bar_left/right` | AgentPanel 标题栏 |
| `status_bar_left/right` | AgentPanel 状态栏（ContextRing 旁） |
| `info_bar` | AppShell 全局 banner |
| `tool_hint` | MessageList 工具输出行内（复用 toolHints 数据） |
| `right_sidebar` | RightSidebar 区域（新增 widget tab） |
| `panel` | dockview 插件面板（大组件，如 CI 面板） |

## 详细计划

### 阶段一：Web widget 数据通道（打通基础数据流）

- [ ] 1.1 新增 `plugin/web_widget.go`：`WebWidgetSpan` 结构 + `RenderSessionWebWidgets`（结构化输出，含 span 切分而非字符串拼接）— 涉及文件：`plugin/web_widget.go`、`plugin/widget.go`
- [ ] 1.2 `protocol/ws.go` 新增 `MsgTypeWebWidgets = "web_widgets"`；`channel/web/web_hub.go` 的 `isSSEEventType` 加入该类型 — 涉及文件：`protocol/ws.go`、`channel/web/web_hub.go`
- [ ] 1.3 **渠道自订阅**：`channel/interfaces.go` 新增 `WidgetSubscriber` 接口（`SetWidgetRegistry` + `NotifyWidgetsUpdated`）；`agent/agent.go` OnUpdated 全量替换为 channelRange 通用分发（删除 CLI-only 硬编码）；`channel/web/web.go` 的 WebChannel 实现 WidgetSubscriber（SetWidgetRegistry + NotifyWidgetsUpdated：遍历 web 订阅 chatID → RenderSessionWebWidgets → 增量检测 → hub.sendToSession web_widgets）；`channel/web/web_remote_cli.go` 的 RemoteCLIChannel 迁移为 WidgetSubscriber 实现（保留现有 ANSI 路径）；`serverapp/server.go` registerChannels 统一断言注入 SetWidgetRegistry — 涉及文件：`channel/interfaces.go`、`agent/agent.go`、`channel/web/web.go`、`channel/web/web_remote_cli.go`、`serverapp/server.go`
- [ ] 1.4 前端：`web/src/types/shared.ts` 加 `web_widgets` 类型；`sseConnection.ts` SSE_EVENT_TYPES 加类型；新增 `usePluginWidgets.ts` + `PluginWidgetProvider.tsx` — 涉及文件：`web/src/types/shared.ts`、`web/src/providers/sseConnection.ts`、`web/src/plugins/*`
- [ ] 1.5 前端 `WidgetZone.tsx`：text+style→Tailwind 语义色映射，渲染到 AgentPanel 状态栏（status_bar_left/right）+ AppShell banner（info_bar）— 涉及文件：`web/src/plugins/WidgetZone.tsx`、`web/src/components/agent/AgentPanel.tsx`、`web/src/layouts/AppShell.tsx`
- [ ] 1.6 web 首次连接 pull 初始化：复用现有 `plugin_widgets` RPC（`rpc_table.go:1733`）在挂载时拉取一次全量 zones，之后靠 SSE 增量更新（避免依赖重连 replay）— 涉及文件：`web/src/plugins/usePluginWidgets.ts`、`serverapp/rpc_table.go`（确认 web 鉴权已放行）

**阶段一验收**：CLI/script 插件声明的 widget 能在 web 状态栏/信息条以富文本渲染（首帧 pull + 后续 SSE push）；CLI 路径无回归。

### 阶段二：`web_ui` 组件协议 + 前端组件库（炫酷组件）

- [ ] 2.1 `plugin/plugin.go` 新增 `WebComponent`、`WebUIDecl` 类型（type+props 白名单校验）— 涉及文件：`plugin/plugin.go`
- [ ] 2.2 `protocol/ws.go` 新增 `MsgTypeWebUI = "web_ui"`；`agent/transport_channel_plugin.go` 新增 `handleChannelUI`（解析、校验、存 `WebUIDeclRegistry`，覆盖式热更新）+ `ChannelPluginTransportConfig.OnChannelUI` 回调 — 涉及文件：`protocol/ws.go`、`agent/transport_channel_plugin.go`
- [ ] 2.3 `serverapp/channel_plugin.go` 的 `CreateChannel` 传入新回调，注册到 agent — 涉及文件：`serverapp/channel_plugin.go`、`agent/agent.go`
- [ ] 2.4 结构化数据扩展：`RenderSessionWebWidgets` 返回 `WebWidgetData{zones, components, revision}`（zones=结构化 spans，components=web_ui 声明）；WebChannel.NotifyWidgetsUpdated 合并两者推送（含 revision + 增量 diff，重复内容不推送）— 涉及文件：`plugin/web_widget.go`、`channel/web/web.go`
- [ ] 2.5 前端组件注册表 `registry.tsx` + 7 个声明式组件（badge/progress/metric/sparkline/table/list/markdown）— 涉及文件：`web/src/plugins/components/*`
- [ ] 2.6 `WidgetPanel.tsx` dockview 面板（slot=panel）+ RightSidebar widget tab（slot=right_sidebar）— 涉及文件：`web/src/plugins/WidgetPanel.tsx`、`web/src/layouts/DockviewContainer.tsx`、`web/src/layouts/RightSidebar.tsx`
- [ ] 2.7 **自由代码模式**：将 `GenUIBlock` 泛化为 `SandboxedUI.tsx`（提取编译管线 + iframe root + postMessage 桥 + `ui_action`/`ui_resize`/`ui_wheel`/`ui_ready`/`ui_data` 消息）；`code` 模式走 SandboxedUI；`custom` 模式（外部 URL）校验可信源后同管线渲染 — 涉及文件：`web/src/plugins/SandboxedUI.tsx`、`web/src/components/agent/GenUIBlock.tsx`（复用不破坏）

**阶段二验收**：channel plugin 声明 `web_ui` 后，web 立即渲染对应炫酷组件；`code` 模式任意 TSX 在 iframe 沙箱内编译渲染（复现 GenUIBlock 能力）；声明热更新生效；大组件可在 dockview 面板展示。

### 阶段三：交互事件回传（点击/输入 → 插件）

- [ ] 3.1 `serverapp/rpc_table.go` 新增 `web_ui_action` RPC：校验 widget 归属 → 路由到声明该组件的 channel plugin（`Call("web_ui_action")`）或直接调用 native/script 插件回调 — 涉及文件：`serverapp/rpc_table.go`
- [ ] 3.2 `plugin/protocol/protocol.go` dispatch 新增 `web_ui_action` case + Handler 回调字段；`plugin/context.go` 新增 `RegisterWebActionHandler` — 涉及文件：`plugin/protocol/protocol.go`、`plugin/context.go`
- [ ] 3.3 前端 `api.ts` 封装 `web_ui_action` 调用；组件 onClick/onChange → RPC；响应组件状态更新推送 — 涉及文件：`web/src/plugins/api.ts`、`web/src/plugins/components/*`
- [ ] 3.4 交互状态同步：插件返回新组件描述 → 后端校验 → 推送 `web_widgets` 增量更新 — 涉及文件：`agent/agent.go`、`plugin/web_widget.go`

**阶段三验收**：点击 web 组件触发插件处理；插件返回的更新状态实时反映到组件；无 XSS 逃逸。

### 阶段四（可选/后续）：自由代码高级扩展

> 基础自由代码（`code` 模式 + iframe 沙箱）已在阶段二实现。本阶段是进阶能力。

- [ ] 4.1 `ui_data` 数据流：宿主→iframe 实时数据注入（token 用量/进度/自定义事件流），插件代码经 postMessage 接收 — 涉及文件：`web/src/plugins/SandboxedUI.tsx`、`web/src/plugins/usePluginWidgets.ts`
- [ ] 4.2 `custom` 外部 URL 加载：可信源校验（http(s) 白名单）→ iframe `src` 渲染 → postMessage 桥（`ui_action`/`ui_ready`）打通双向通信 — 涉及文件：`web/src/plugins/SandboxedUI.tsx`
- [ ] 4.3 插件内嵌 JS bundle（进阶）：插件声明 `"bundle": "https://..."`，宿主 iframe 内 `import()` 加载 ES module 组件（组件用任意第三方库，如 ECharts/Three.js）— 涉及文件：`web/src/plugins/SandboxedUI.tsx`、`plugin/web_widget.go`（bundle 字段校验）

## 验证方案

- **Go 单元测试**
  - `RenderSessionWebWidgets`：script plugin style 解析 → 结构化 spans；per-workDir 隔离
  - `handleChannelUI`：声明注册/覆盖式热更新/非法 type 拒绝
  - `web_ui_action`：路由到正确插件；无归属 widget 拒绝；30s 超时
- **前端 vitest**
  - `registry.tsx`：未知 type → 降级为 badge/markdown 占位
  - `usePluginWidgets`：消息解析、revision 增量合并、session 切换重置
  - `WidgetZone`：style → Tailwind 类映射；icon 名映射
- **Playwright E2E**
  - 插件声明 sparkline → web 渲染 SVG；声明更新 → 热更新
  - 点击 metric 卡片 → 后端收到 `web_ui_action`
  - CLI 模式 widget 渲染无回归
- **手动验证**
  - 一个示例 channel plugin（Go/Python）声明 badge + sparkline + table，验证全链路

## 回滚策略

- 每阶段独立合入，CLI widget 路径零改动（ANSI 渲染保留），web 新增代码失败不影响既有功能
- 协议常量新增不影响旧客户端（未知类型忽略）
- `web_ui_action` RPC 独立注册，失败仅影响交互不阻塞消息流
- 插件声明校验失败 → 跳过该 widget 并 log 警告，不 crash

## 最终效果与插件例子

### 最终效果（全链路打通后）

打开 xbot web 界面，你会看到插件的 UI 散布在界面各处，**全部由插件声明驱动、实时更新**：

```
┌────────────────────────────────────────────────────────────────────────┐
│  Agent 面板标题栏                     [✓ 构建通过] [⚠ 2 个告警]   ← badge│
├────────────────────────────────────────────────────────────────────────┤
│  信息条 (info_bar):   ⟳ CI Pipeline #128 · 3/5 jobs running    ← metric│
│                                                                        │
│  ┌─ 主对话区 ──────────────────────┐  ┌─ 右侧插件栏 (right_sidebar) ──┐ │
│  │                                │  │  服务器资源            ┌─────┐ │
│  │  (对话消息...)                  │  │  CPU  ████████░░ 82%   │ 走势 │ │
│  │                                │  │  内存 ██████░░░░ 61%   │ 图   │ │
│  │  工具提示:  ⚠️ 文件权限敏感      │  │  磁盘 ███░░░░░░░ 28%  └─────┘ │ │
│  │        (tool_hint 复用)         │  │  最近构建  ┌─────────┐        │ │
│  │                                │  │  #128 ✓ 3m  │ table   │        │ │
│  └────────────────────────────────┘  │  #127 ✗ 9m  │ (可点击)│        │ │
│                                      │  #126 ✓ 12m └─────────┘        │ │
│  状态栏:  git:main ⬆3  |  ⏺ 运行中    └────────────────────────────────┘ │
└────────────────────────────────────────────────────────────────────────┘
```

- **状态栏/标题栏/信息条**：插件贡献 badge、metric、progress 等微型组件（Git 状态、CI 徽章、token 用量）
- **右侧栏**：sparkline（资源走势）、table（构建历史）、list（待办/Feed），点击可触发插件动作
- **dockview 面板**（`slot=panel`）：大型可视化（监控大盘、Diff 视图、发布流程），可拖拽/最大化
- **交互闭环**：点击组件 → `web_ui_action` RPC → 插件进程处理 → 返回新组件状态 → 实时刷新
- **热更新**：插件发送新的 `web_ui` 声明即覆盖旧组件，无需重启 server
- **全渠道统一**：CLI 继续 ANSI 渲染；Web 走结构化 JSON；QQ/Feishu 等新渠道实现 `WidgetSubscriber` 接口即自动获得能力

### 好玩的插件例子

**1. 🚀 CI/CD 监控面板**（table + sparkline + badge + metric + 交互）
- 状态栏：当前 pipeline 的构建徽章（绿色 ✓ / 红色 ✗ / 黄色 ⏳ 脉冲动画）
- 右侧栏：sparkline 展示最近 20 次构建时长趋势；table 列出构建历史（提交人、时长、结果）
- 点击"失败"行 → `web_ui_action: retry` → 插件调用 CI API 重新触发 → 徽章变黄 → 完成变绿
- 玩法：一行 `web_ui` 声明 + 几十行脚本/Go 代码，就能拥有堪比 GitHub Actions 的监控页

**2. 📊 Git 状态小组件**（metric + list + badge）
- 状态栏：`⬆3 ⬇1` 未推送/未拉取计数（metric，带 icon）
- 右侧栏：当前分支未提交文件列表（list，点击可查看 diff）
- 触发器：`PostToolUse:Shell*` 钩子——每次 agent 跑完 Shell 命令自动刷新
- 玩法：类似 GitHub Copilot 状态栏体验，但数据源完全自定义

**3. 💻 服务器资源监控**（sparkline × 3 + progress）
- 侧边栏：CPU / 内存 / 磁盘三条实时 sparkline（`refresh: "5s"` 轮询）
- 状态栏：CPU 使用率 progress 条（超 90% 变红 + pulse）
- 玩法：自己写个采集脚本（`cat /proc/stat`），配合 `refresh` 机制零后端改动

**4. 📈 自选股/加密货币行情**（table + badge + sparkline）
- 状态栏：大盘指数（badge 涨绿跌红）
- 右侧栏：自选股 table（现价、涨跌幅、迷你 K 线 sparkline）
- 点击某行 → 插件弹出 markdown 详情（研报摘要）
- 玩法：对接任何行情 API，30 分钟能出一个"专属行情面板"

**5. 🍅 番茄钟/专注计时**（progress + metric）
- 标题栏：当前番茄倒计时环形进度
- 触发器：Cron 定时任务自动切换工作/休息
- 玩法：结合 `/focus` 命令，agent 帮你管理专注节奏

**6. 🔍 后台任务状态墙**（list + badge）
- 侧边栏：显示所有后台 SubAgent/任务的实时状态（运行中/成功/失败 badge）
- 玩法：`wireBgNotificationDrain` 已有任务事件流，插件订阅即可展示

**7. 🎮 自定义 iframe 组件**（custom，阶段四）
- 插件声明 `{"type":"custom","props":{"src":"http://localhost:3000/dashboard"}}` → web 内嵌任意应用
- 玩法：把 Grafana、ECharts 大盘、公司内部工具直接嵌进 xbot

**8. 🎯 团队动态 Feed**（list + markdown）
- 右侧栏：团队 GitHub 提交/PR/评论流（markdown 富文本渲染）
- 点击"approve"按钮 → `web_ui_action` → 插件调 GitHub API 完成 review

---

### 一个最小插件长什么样（效果对照）

```json
// my-plugin manifest 声明一个右侧栏走势图组件（channel plugin 或 script plugin）
{"type":"web_ui","ui":[
  {"widget_id":"cpu","title":"CPU 负载","slot":"right_sidebar","refresh":"5s",
   "component":{"type":"sparkline","props":{"data":[12,48,35,80,62,45,91],"color":"#22c55e"}}}
]}
```

```bash
# 配套 script plugin：每 5 秒输出一行 CPU 数据（xbot 自动轮询）
# refresh: "5s" 驱动
echo "sparkline|12,48,35,80,62,45,91"
```

→ 打开 web 界面，右侧栏立即出现一个 5 秒自动刷新的 CPU 走势图。

### 自由代码模式示例（完全自主 — 炫酷上限）

```json
// 插件声明一段任意 TSX 代码，前端 iframe 沙箱内编译渲染（复用 GenUIBlock 机制）
{"type":"web_ui","ui":[
  {"widget_id":"pomodoro","title":"🍅 专注计时","slot":"right_sidebar",
   "code":"export default function App(){\n"+
     "  const [left,setLeft]=useState(25*60)\n"+
     "  useEffect(()=>{ const t=setInterval(()=>setLeft(l=>Math.max(0,l-1)),1000); return ()=>clearInterval(t) },[])\n"+
     "  const m=String(Math.floor(left/60)).padStart(2,'0'), s=String(left%60).padStart(2,'0')\n"+
     "  const pct=(left/(25*60))*100\n"+
     "  return <div className='p-3 flex flex-col items-center'>\n"+
     "    <div className='relative w-28 h-28'>\n"+
     "      <svg viewBox='0 0 100 100' className='w-full h-full -rotate-90'>\n"+
     "        <circle cx='50' cy='50' r='42' fill='none' stroke='#eee' strokeWidth='8'/>\n"+
     "        <circle cx='50' cy='50' r='42' fill='none' stroke={pct>50?'#22c55e':pct>20?'#eab308':'#ef4444'} strokeWidth='8' strokeLinecap='round'\n"+
     "          strokeDasharray={`${pct*2.64} 264.8`}/>\n"+
     "      </svg>\n"+
     "      <div className='absolute inset-0 flex flex-col items-center justify-center'>\n"+
     "        <span className='text-2xl font-bold text-gray-900'>{m}:{s}</span>\n"+
     "      </div>\n"+
     "    </div>\n"+
     "    <button data-action='toggle' data-state={left>0?'running':'idle'}\n"+
     "      className='mt-2 px-4 py-1 rounded-lg bg-indigo-500 text-white text-sm hover:bg-indigo-600'>\n"+
     "      {left>0?'暂停':'开始'}\n"+
     "    </button>\n"+
     "  </div>"
  }
]}
```

→ 一个环形番茄钟组件，几十行 TSX 完全自主编写；点击按钮经 `ui_action` → `web_ui_action` RPC → 插件进程收到通知。**复杂度没有上限**——想写什么效果都能写（ECharts 大盘、Three.js 3D、游戏、表单……只要在 iframe 里）。

---

## 注意事项

- **XSS 第一原则**：所有插件内容经 React 文本节点渲染（自动转义）；`markdown` 组件用现有 sanitize 管道；`custom` 组件 sandbox 后才放开
- **渠道自订阅原则**：agent 侧只做 `WidgetSubscriber` 断言 + 通知，**绝不硬编码 channel 分支**。渲染格式（ANSI/JSON）、推送对象、增量策略全部由渠道自己决定——与 `ProgressSender`/`SessionStateSender` 同构
- **增量推送**：web_widgets 携带 revision，前端按 revision 合并，避免全量重渲染
- **沿用项目前端模式**：Context + useSyncExternalStore（不引入 Zustand/Redux），仿 `progressStore.ts`
- **dockview 桥接**：WidgetPanel 需经 DockviewContext 获取 ws 连接（同 BackgroundPanel 模式）
- **SDK 文档**：阶段二完成后更新 `docs/agent/plugin.md` 新增 Web UI 章节；`plugin/PROTOCOL.md` 补充 web_ui/web_ui_action
- **同伴协作**：web-perf 分支并行工作中，前端修改注意避开其改动区域（进度/消息渲染），冲突时 SendMessage 协商

## ✅ 自审通过

- 目标一致性 ✅：全部步骤服务于「web 插件 + 复用扩展插件协议 + 炫酷 UI 组件 + 完全自主的贡献区域源码」
- 步骤可执行性 ✅：每步精确到文件 + 函数 + 协议 JSON 格式
- **渠道自订阅架构 ✅**：`channel.WidgetSubscriber` 接口（与 ProgressSender 同模式），agent 只做 channelRange 断言 + NotifyWidgetsUpdated 通知；CLI（ANSI）与 Web（结构化 JSON）各自实现，未来渠道实现接口即获得能力；agent.go 删除 CLI-only 硬编码
- **自由代码模式 ✅**：复用生产级 `GenUIBlock`（sucrase 编译 + iframe React root + postMessage 桥），泛化为 `SandboxedUI`；`code`/`custom` 声明式 + 自由代码两层并存；iframe sandbox 隔离父页面 DOM（防御纵深）；`src` URL 白名单校验；阶段二即实现（非可选）
- 遗漏检查 ✅：pull 初始化、CLI 兼容、XSS、SSE 高频、dockview root、genui 共存、sandbox 细节（allow-scripts allow-same-origin）、postMessage 白名单均已覆盖
- 依赖检查 ✅：阶段 1→2→3→4 依赖合理（接口先于渠道实现，SandboxedUI 泛化在阶段二先于阶段三交互）
- 文件准确性 ✅：所有路径/行号经 grep/sed 验证（agent.go:1987、web_hub.go:586、interfaces.go:9-41、rpc_table.go:405、GenUIBlock.tsx、sseConnection.ts:32、shared.ts:133）
- 风险评估 ✅：风险 + 应对措施
- 计划自洽性 ✅：无矛盾修改
