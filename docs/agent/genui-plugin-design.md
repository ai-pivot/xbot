# xbot-genui 插件化设计优化计划

> 状态：设计稿（待评审）
> 目标：把内置 `display_html`（genui）改造成**单独插件项目**，最大化 fancy（数据可视化 + 动画动效 + 3D + 综合组件库），插件系统缺少的支持协议由主仓库补齐。

---

## 1. 现状分析

### 1.1 当前 genui 全链路

```
LLM 生成 TSX
  → display_html 工具（tools/display_html.go，注册 web channel only）
  → 流式：engine_wire.go streamToolCallFunc 硬编码 tc.Name=="display_html"
          extractPartialCodeFromArgs(tc.Arguments) → GenUIContent progress 事件
  → 完整：Execute 里 ctx.SendFunc(channel, chatID, code, {genui:true})
          → WS "genui" 消息
  → 前端：useProgressStream 'genui' case → store.setGenUIContent
          LiveIteration 渲染 streaming <SandboxedUI streaming>（GenUIBlock 已删除）
          TurnBody/FoldedToolGroup/ToolRender 从 iterations 提取
          display_html 工具 → ToolRender → renderTool 派发 → 插件 renderer → SandboxedUI
  → 交互：⚠️ data-action 点击链路已断——前端 genui_action 调用点已随 GenUIBlock
          删除（web/src 中 0 处引用）；后端 genui_action handler 仍在
          （serverapp/rpc_table.go registerGenUIHandlers → InjectAsyncMessage，
          busy=合成 tool result，idle=新 turn）。SandboxedUI 支持 onAction prop，
          但 genui renderer（plugins/genui/index.tsx）与 LiveIteration 均未传入。
          插件 web UI 组件走 web_ui_action（plugins/api.ts sendWebUIAction）仍然可用。
```

### 1.2 关键文件

| 层 | 文件 | 职责 |
|---|---|---|
| 工具 | `tools/display_html.go` | 工具定义、校验（括号平衡/App 存在/非空渲染）、SendFunc 发送、>4096 落盘 |
| 流式 | `agent/engine_wire.go:2088-2144` | `streamToolCallFunc` 提取部分 code → `GenUIContent` |
| 协议 | `protocol/events.go:101-103` | `GenUIContent` progress 字段 |
| 协议 | `protocol/ws.go:25` | `MsgTypeGenUI = "genui"` |
| RPC | `serverapp/rpc_table.go:489-509` | `genui_action`（registerGenUIHandlers）→ agent loop；⚠️ 前端无调用点 |
| RPC | `serverapp/rpc_table.go:515-557` | `web_ui_action` → channel plugin → native → agent loop |
| 前端 | `web/src/plugins/SandboxedUI.tsx` | 泛化沙箱：sucrase 编译 TSX + 独立 React root（inline，非 iframe）+ 编译缓存 + UIErrorBoundary + data-action 委托（GenUIBlock 已删除，由它取代） |
| 前端 | `web/src/components/agent/LiveIteration.tsx:261` | streaming GenUI 渲染（`<SandboxedUI code={genuiContent} streaming>`，无 onAction） |
| 前端 | `web/src/components/agent/TurnBody.tsx` / `FoldedToolGroup.tsx` / `ToolRender.tsx:138` | 从工具列表提取 uiMode 工具 → `renderTool(tool, {chatID:''})` 派发（无 onAction，chatID 为空占位） |
| 前端 | `web/src/plugins/genui/index.tsx` | genui messageRenderer（matches uiMode='genui' + tool='display_html'）→ `SandboxedUI({code, streaming:false})`（无 onAction/onError） |
| 安全 | `web/e2e/genui-escape.spec.ts` | 沙箱逃逸 E2E |
| 样式 | `web/src/genui-safelist.html` | Tailwind v4 safelist（75242 字符，全色彩/间距/布局） |

### 1.3 现状局限（fancy 的阻碍）

1. **沙箱能力弱**：`sandbox="allow-scripts"`（opaque origin），iframe 无法访问父页面全局库；组件在父页面编译（`new Function`），只能拿到注入的 React hooks —— **没有图表/动画/3D 库**。
2. **无组件库**：LLM 只能用 Tailwind 类 + 基础 React hooks，没有预置组件（Card/Chart/Modal/Table…）。
3. **无 import**：sucrase 编译后 strip 所有 import —— 无法 `import { LineChart } from 'echarts'`。
4. **无主题**：iframe body 硬编码白底，dark mode 不生效。
5. **工具名硬编码**：`engine_wire.go` 的 `if tc.Name == "display_html"` 和前端 4 处 `tool.name === 'display_html'` 特判 —— 插件化后工具名变化会全断。
6. **无状态保持**：交互（genui_action）只把 action 注入 agent loop，UI 自身无状态/无组件间通信。
7. **无跨会话复用**：display_html 是 agent 工具，与插件 web_ui（静态组件）是两套体系。

---

## 2. 目标架构

### 2.1 一句话

**genui 变成独立 Go stdio channel 插件 `xbot-genui`**：插件声明 `display_html` 工具（带 UI 元数据），工具执行由插件进程处理（返回 UI 代码 + 流式提取），前端渲染器升级为 fancy 运行时（全局组件库 + 图表 + 动画 + 3D + 主题），交互经 `web_ui_action`/`genui_action` 路由回插件或 agent。主仓库移除内置 display_html，补齐插件协议缺口。

### 2.2 架构图

```
┌─ 主仓库 xbot ──────────────────────────────────────────────┐
│  tools.Registry ── RegisterForChannel("web", genuiBridge)  │
│      ▲                                                      │
│  channel_tools 声明（插件→xbot）                            │
│      │                                                      │
│  ChannelPluginTransport ── JSON-RPC over stdio ──────────┐  │
│      │  execute_tool RPC（xbot→插件）                     │  │
│      │  web_ui_action RPC（xbot→插件，交互回调）          │  │
│      ▼                                                    │  │
│  engine_wire streamToolCallFunc                            │  │
│    查工具 UI 元数据（非硬编码工具名）                      │  │
│    → GenUIContent progress 事件                           │  │
│      │                                                    │  │
│  web channel：genui 消息 / web_widgets 组件                │  │
└─────┼─────────────────────────────────────────────────────┘
      │ SSE/WS
┌─────▼─────────────────────────────────────────────────────┐
│  web 前端                                                  │
│  GenUIBlock（升级）→ XBOT_UI 全局运行时                    │
│    · 全局组件库（Card/Button/Table/Modal/Form…）           │
│    · 图表（ECharts option 驱动）                           │
│    · 动画（framer-motion 封装）                            │
│    · 3D（three.js Canvas 封装）                            │
│    · 主题变量（light/dark）                                │
│    · 组件间 bus + data-action 交互协议                     │
│  data-action 点击 → web_ui_action / genui_action RPC       │
└────────────────────────────────────────────────────────────┘

┌─ 插件项目 plugins/xbot-genui/ ────────────────────────────┐
│  Go stdio channel plugin                                   │
│  · channelProvider: name="genui"（虚拟，不处理入站）       │
│  · channel_tools: display_html + UI 元数据                 │
│    {name, description, parameters,                         │
│     ui: {mode:"genui", param:"code", libs:[...]},          │
│     channels:["web"]}                                      │
│  · execute_tool: 校验 TSX → 返回 {content, is_error,       │
│    ui_code, ui_libs}                                       │
│  · web_ui_action handler: 可选处理交互（默认回传 agent）    │
│  · 工具描述模板：引导 LLM 用 XBOT_UI 组件库               │
└────────────────────────────────────────────────────────────┘
```

### 2.3 关键设计决策

| # | 决策 | 理由 |
|---|---|---|
| D1 | 插件 channel 名 `genui`（虚拟），工具经 `channels:["web"]` 注册到 web | channel 名保留字含 web/feishu/qq/napcat/cli；工具必须对 web 会话可见 |
| D2 | `ChannelToolDecl` 增加 `Channels []string` + `UI *ToolUIDecl` 字段 | 让插件声明工具注册到指定 channel + 携带 UI 元数据（这是"插件系统缺少支持就提供"的核心） |
| D3 | `ChannelToolBridge.Execute` 返回支持 `ui_code`，bridge 自动调 `ctx.SendFunc` 发 genui 消息 | 插件执行 display_html 后 UI 走既有 genui 推送链路，前端零改动接收 |
| D4 | `engine_wire` 流式提取改查工具 UI 元数据，删硬编码 `tc.Name=="display_html"` | 工具名不再耦合 |
| D5 | 前端 GenUIBlock 升级：组件在父页面编译（现状），父页面加载全局库后注入编译作用域；iframe 保持 `allow-scripts` opaque origin | 安全性不降级（无法读父 DOM/token），但拿到图表/动画/3D 能力 |
| D6 | 移除内置 `tools/display_html.go` + `agent.go:1589` 注册 + 前端 4 处 `display_html` 硬编码特判 | 单一路径，避免双实现漂移（用户确认） |
| D7 | 交互保留双路由：默认 genui_action → agent loop（agent 动态 UI 的语义）；插件可选 web_ui_action 拦截 | 兼容现有行为 + 给插件扩展空间 |

---

## 3. 插件系统需要提供的支持（主仓库扩展点）

### 3.1 `ChannelToolDecl` 扩展（plugin/channel_tool_bridge.go）

```go
type ChannelToolDecl struct {
    Name        string          `json:"name"`
    Description string          `json:"description"`
    Parameters  []llm.ToolParam `json:"parameters"`
    // NEW: 注册到哪些 channel（默认 = 插件自身 channel；空 = 仅自身）
    Channels []string `json:"channels,omitempty"`
    // NEW: UI 能力元数据（工具参数中哪个是 UI 代码、需要哪些库）
    UI *ToolUIDecl `json:"ui,omitempty"`
}

type ToolUIDecl struct {
    Mode  string   `json:"mode"`  // "genui"（TSX 自由代码）
    Param string   `json:"param"` // 承载 UI 代码的参数名，如 "code"
    Libs  []string `json:"libs,omitempty"` // 提示前端注入哪些全局库（echarts/three/motion）
}
```

- `handleChannelTools`（agent/transport_channel_plugin.go:491）改为：遍历 `decl.Channels`（空则用 `t.name`）逐个 `RegisterForChannel`。
- `ChannelToolBridge` 增加 `UIDecl() *ToolUIDecl` + `Channels() []string` 方法，让 engine_wire/前端能查询元数据。

### 3.2 `execute_tool` RPC 结果扩展

```go
// 插件 execute_tool 返回（channel-tools 文档扩展）
{
  "content": "🎨 UI rendered (1234 chars)",
  "is_error": false,
  "ui_code": "<完整 TSX>",          // NEW：触发 bridge 发送 genui 消息
  "ui_libs": ["echarts", "three"]  // NEW：可选，指示前端注入的全局库
}
```

`ChannelToolBridge.Execute`（plugin/channel_tool_bridge.go:48）改造：
- 解析新增 `ui_code` 字段；非空且 `ctx.SendFunc != nil` 时调用
  `ctx.SendFunc(ctx.Channel, ctx.ChatID, ui_code, {"genui":"true", ...})`，
  并把 `ui_code` 放进 `ToolResult.Detail`（保证迭代历史可恢复渲染）。
- 保持 `Summary` 简短（如现状 display_html 的 `🎨 UI rendered (N chars)`）。

### 3.3 `engine_wire` 流式提取泛化（agent/engine_wire.go:2088-2144）

```go
// 旧：if tc.Name == "display_html" { genuiContent = extractPartialCodeFromArgs(tc.Arguments) }
// 新：查工具 UI 元数据
if ui := a.toolUIDecl(progressKey, tc.Name); ui != nil && ui.Mode == "genui" {
    genuiContent = extractPartialParam(tc.Arguments, ui.Param)
}
```

- `a.toolUIDecl(sessionKey, toolName)` 通过 `a.tools.GetForSession(...)` 拿工具，类型断言 `interface{ UIDecl() *plugin.ToolUIDecl }`。
- `extractPartialCodeFromArgs` 泛化为 `extractPartialParam(args, paramName)`（提取 JSON 中指定字段的部分值，容忍截断）。
- 需要判断"工具是否为 channel tool bridge"：`tools.Registry.GetForSession` 已有；加一个 `UIDecl` 可选接口。

### 3.4 前端硬编码特判消除（web）

`AssistantMessage.tsx:180` / `FoldedToolGroup.tsx:211` / `ToolRender.tsx:82` / `LiveIteration.tsx` 中 `tool.name === 'display_html'`：
- 改为检查工具元数据：`tool.ui_mode === 'genui'` 或参数含 `code` + `tool.libraries` 标记。
- `protocol.ToolProgress` 增加 `UIMode string json:"ui_mode,omitempty"` + `UILibs []string json:"ui_libs,omitempty"`（由 engine_wire 从工具声明填充）。
- 渲染仍走 `GenUIBlock`（升级版），只是判定条件从名字变元数据。

### 3.5 `genui_action` / `web_ui_action` 兼容

- `genui_action` 保持现状（注入 agent loop）—— agent 动态 UI 交互回传 agent 是核心语义。
- 插件如需拦截：`web_ui_action` 已支持 channel plugin 优先路由（rpc_table.go:444-460）。genui 插件可选择性注册 `Handler.WebUIAction`。
- 前端 GenUIBlock 的 `ws.rpc('genui_action', ...)` 保持；新增 `ui_session` 元数据（可选）让插件感知归属。

---

## 4. 插件项目设计：`plugins/xbot-genui/`

### 4.1 目录结构

```
plugins/xbot-genui/
├── plugin.json          # id: xbot.genui, runtime: grpc(stdio), entry: ./bin/genui-plugin
├── go.mod               # module xbot-genui; replace xbot => ../../ （独立构建）
├── cmd/genui-plugin/main.go    # stdio JSON-RPC 服务入口
├── internal/
│   ├── server/          # 协议循环：activate/channel_config/channel_tools/execute_tool/web_ui_action
│   ├── tool/            # display_html 工具声明 + 校验逻辑（迁移自 tools/display_html.go）
│   ├── template/        # LLM 工具描述模板（引导 XBOT_UI 组件库用法）
│   └── uistate/         # 可选：UI 会话状态（多轮交互数据）
├── static/
│   └── libs/            # 可选：本地图表库 bundle（echarts/three UMD）供前端注入
├── README.md            # 安装/构建/配置说明
└── Makefile             # build / install（build 到 ~/.xbot/plugins/xbot.genui/bin/）
```

### 4.2 plugin.json 骨架

```json
{
  "id": "xbot.genui",
  "name": "GenUI (display_html)",
  "version": "1.0.0",
  "description": "LLM 生成交互式 UI：图表/动画/3D/组件库，流式预览 + 交互回传",
  "runtime": "grpc",
  "entry": "./bin/genui-plugin",
  "activationEvents": ["onStart"],
  "permissions": ["channels.register", "tools.register", "ui.contribute"],
  "contributes": {
    "channelProvider": {
      "name": "genui",
      "config_schema": [
        {"key": "enabled", "label": "Enable", "type": "toggle", "default_value": "true"},
        {"key": "libs_cdn", "label": "Charts CDN (echarts/three)", "type": "text",
         "default_value": "https://cdn.jsdelivr.net/npm/", "description": "留空用本地 bundle"}
      ]
    }
  }
}
```

### 4.3 channel_tools 声明（插件启动后发送）

```json
{
  "type": "channel_tools",
  "tools": [{
    "name": "display_html",
    "description": "Render an interactive React UI...（详细描述见 4.4）",
    "parameters": [{"name": "code", "type": "string", "description": "TSX module with default export App", "required": true}],
    "channels": ["web"],
    "ui": {"mode": "genui", "param": "code", "libs": ["echarts", "three", "motion"]}
  }]
}
```

### 4.4 LLM 工具描述模板（fancy 引导）

> 核心：告诉 LLM 可用的 `XBOT_UI` 全局 API —— 组件库、图表、动画、3D、主题、交互。

```
You write a single TSX module with `export default function App()`.
React hooks are available. Tailwind classes for styling (light/dark via
`dark:` variants). The preview auto-sizes to content height.

GLOBAL COMPONENT LIBRARY (window.XBOT_UI — use directly, no import):
- <XBOT_UI.Button variant="primary|ghost|outline" onClick={...}>  （or data-action）
- <XBOT_UI.Card title="..." className="...">...</XBOT_UI.Card>
- <XBOT_UI.Table data={rows} columns={[{key,label}]} />
- <XBOT_UI.Stat label="..." value="..." delta={0.12} trend="up"/>
- <XBOT_UI.Sparkline data={[1,5,3,8]} color="#22c55e"/>
- <XBOT_UI.Progress value={0.7} label="Training"/>
- <XBOT_UI.Badge text="NEW" color="green"/>
- <XBOT_UI.Tabs tabs={[{key,label,content}]} />
- <XBOT_UI.Modal open onClose title>...</XBOT_UI.Modal>
- <XBOT_UI.Form onSubmit={...} fields={[{name,label,type}]} />
- <XBOT_UI.Toast show text="saved" />

CHARTS (ECharts option — declarative, no imperative init):
<XBOT_UI.Chart option={{
  xAxis: {type:'category', data:['Mon','Tue',...]},
  yAxis: {type:'value'},
  series: [{type:'line', data:[...], smooth:true, areaStyle:{}}]
}} height={280} />

3D (three.js scene, imperative inside useEffect):
const ref = XBOT_UI.useThreeScene((scene, THREE) => {
  scene.add(new THREE.Mesh(new THREE.BoxGeometry(1,1,1),
    new THREE.MeshStandardMaterial({color:0x6366f1})))
})

MOTION (framer-motion):
<XBOT_UI.motion.div initial={{opacity:0,y:20}} animate={{opacity:1,y:0}}
  transition={{duration:0.5}}>...</XBOT_UI.motion.div>

INTERACTION (two ways — pick based on need):
1. Agent callback: data-action="save" data-* attrs → click routed to agent
   (agent sees 🖱️ [UI Action] save State: {...})
2. Local state: React useState/useEffect — pure client-side interactivity.

THEME: use Tailwind dark: variants; background is white in light mode,
   slate-950 in dark mode. Text must be dark in light (text-gray-900) and
   light in dark (text-slate-100). Avoid hardcoded colors.

Example:
export default function App() {
  const [n, setN] = useState(0)
  return (
    <div className="p-4">
      <XBOT_UI.Stat label="Clicks" value={n} />
      <XBOT_UI.Button variant="primary" onClick={() => setN(n+1)}>+1</XBOT_UI.Button>
      <XBOT_UI.Button variant="ghost" data-action="reset">Reset</XBOT_UI.Button>
    </div>
  )
}
```

### 4.5 独立构建

`plugins/xbot-genui/go.mod`：

```go
module xbot-genui

go 1.22

require xbot v0.0.0
replace xbot => ../..
```

- 依赖 `xbot/plugin/protocol`（协议结构）、`xbot/llm`（ToolParam）、`xbot/tools`（ToolResult）—— 不依赖 agent/ 大包，编译快。
- `make build` → `bin/genui-plugin`（纯静态二进制，无 CGO）。
- `make install` → 复制到 `~/.xbot/plugins/xbot.genui/` + 热重载。

---

## 5. 前端改造（fancy 运行时）

### 5.1 GenUIBlock 升级（web/src/components/agent/GenUIBlock.tsx）

保持核心机制（父页面编译 + iframe opaque origin + 编译缓存 + 流式节流 + ResizeObserver），新增：

1. **全局库注入到编译作用域**：父页面动态加载 `echarts` / `three` / `framer-motion`（CDN 或本地 bundle），编译 wrapped 时注入：
   ```js
   const XBOT_UI = arguments[1]; // 组件库注册表
   const echarts = XBOT_UI.echarts, THREE = XBOT_UI.THREE;
   const { motion } = XBOT_UI;
   ```
2. **`XBOT_UI` 运行时**（新文件 `web/src/genui/runtime.tsx`）：注册
   - 基础组件：Button/Card/Table/Stat/Sparkline/Progress/Badge/Tabs/Modal/Form/Toast
   - `Chart`（ECharts 声明式封装：useEffect + echarts.init + resize observer + dispose）
   - `motion`（framer-motion 子集 re-export）
   - `useThreeScene`（three.js Canvas 场景 hook）
   - `useBus`（iframe 内组件间事件，非跨 iframe）
   - 主题变量（从父页面读取当前 theme，注入 `--genui-*` CSS 变量 + body class）
3. **主题适配**：iframe doc 写 `light/dark` class + CSS 变量；编译后的组件用 `dark:` 变体自动适配。
4. **错误处理**：库加载失败时 `XBOT_UI.Chart` 渲染 fallback（提示 + option 文本），不白屏。
5. **懒加载**：echarts/three/framer-motion 仅在 `libs` 声明包含时加载（按需，不拖慢首屏）。

### 5.2 SandboxedUI 与 GenUIBlock 统一

- `SandboxedUI`（插件自由代码）与 `GenUIBlock`（agent genui）共享 `XBOT_UI` 运行时和编译内核。
- 抽公共模块：`web/src/genui/compile.tsx`（TSX→Component，含缓存/流式节流/autoClose/样式块处理，迁移自 GenUIBlock）+ `web/src/genui/runtime.tsx`（组件库）。
- `GenUIBlock` / `SandboxedUI` 各自保留外壳（props/iframe 管理），编译与运行时复用。

### 5.3 消息与渲染接线

- `useProgressStream.ts` `'genui'` case + `genui_content`：保持 `store.setGenUIContent`（协议不变）。
- `LiveIteration.tsx` / `AssistantMessage.tsx` / `FoldedToolGroup.tsx` / `ToolRender.tsx`：`display_html` 硬编码 → 检查 `tool.ui_mode === 'genui'`。
- `genui_action` RPC 参数增加 `ui_session`（可选）：插件可据此识别归属（4.5 D7）。

### 5.4 E2E

- `genui-escape.spec.ts` 保持（沙箱逃逸回归）。
- 新增：图表渲染 smoke（`XBOT_UI.Chart` 挂载后 canvas 存在）、主题切换（dark class 注入）、组件库组件渲染、流式节流不闪烁。

---

## 6. 实施里程碑

| 阶段 | 内容 | 验证 |
|---|---|---|
| **M1 协议扩展** | `ChannelToolDecl.Channels/UI` + `handleChannelTools` 多 channel 注册 + `ChannelToolBridge.Execute` 支持 `ui_code` + `engine_wire` 流式提取泛化 | Go 测试：`TestChannelToolDecl_UI` / `TestBridge_UICodeSend` / `TestEngineWire_UIDecl` |
| **M2 前端运行时** | `XBOT_UI` 组件库 + Chart/motion/three + 主题 + GenUIBlock 注入 + SandboxedUI 统一 | vitest：`runtime.test.tsx` / `GenUIBlock.test.tsx`；tsc -b |
| **M3 移除内置** | 删 `tools/display_html.go`、`agent.go:1589` 注册、前端 4 处 `display_html` 特判改元数据 | go test ./... + vitest 全绿；`genui-escape.spec.ts` 通过 |
| **M4 插件项目** | `plugins/xbot-genui/` 完整实现（plugin.json + stdio 服务 + 工具声明 + 校验迁移 + 描述模板 + Makefile） | 插件激活 + 会话内调 display_html + 流式/完整 UI 渲染 + 交互回传 |
| **M5 文档+部署** | AGENTS.md 更新（genui gotcha + 插件协议扩展）、docs/agent/ 索引、前端部署 | 全量测试 + E2E |

**依赖顺序**：M1 → M2 → M3 可并行；M4 依赖 M1；M5 最后。

---

## 7. 测试策略

| 层 | 用例 |
|---|---|
| Go 单测 | `ChannelToolDecl` 解析（含 Channels/UI）；`ChannelToolBridge.Execute` 解析 `ui_code` 并触发 `SendFunc`；`handleChannelTools` 多 channel 注册；`engine_wire` 流式提取用 UI 元数据而非工具名；`extractPartialParam` 容忍截断 |
| vitest | `XBOT_UI` 组件库渲染（Button/Chart/Stat/Table…）；Chart 挂载 canvas；主题注入（dark class）；GenUIBlock 编译注入全局库；`ui_mode==='genui'` 判定替代 `display_html` 名字 |
| E2E | `genui-escape.spec.ts`（安全回归）；新增图表/主题/组件库 smoke |
| 手动 | 真机：agent 生成含 Chart 的 UI → 流式预览 → 点击 data-action → agent 收到 action → 刷新后历史仍渲染 UI |

---

## 8. 风险与对策

| 风险 | 对策 |
|---|---|
| 库体积：echarts(~1MB)+three(~600KB) 注入父页面 | 按需加载（仅 `ui.libs` 声明时动态 import）；CDN 懒加载 + 本地 bundle 可选 |
| 沙箱逃逸（opaque origin 保护）被 allow-same-origin 破坏 | **坚持 allow-scripts-only**；库注入发生在父页面编译作用域，iframe 内不执行 untrusted 代码的 DOM 访问 |
| 工具名硬编码改元数据的兼容性 | `protocol.ToolProgress.UIMode` 默认空；历史消息无元数据时 fallback 到名字 `display_html`（过渡期） |
| 插件 channel 名 `genui` 与现有 channel 冲突 | `channels.register` 校验保留字列表外可用；虚拟 channel 不处理入站，无真实消息流 |
| 插件二进制与主仓库版本漂移 | `replace xbot => ../..` 同仓库构建；主仓库协议变更时插件同步 rebuild |
| 流式提取对非 code 参数 | `extractPartialParam` 通用化 + UI 声明指定 param；校验失败降级为无流式（等完整 code） |

---

## 9. 通用性设计（不只服务 display_html）

> 核心原则：**UI 能力由工具元数据声明，不由工具名决定**。任何插件工具只要声明
> `ui: {mode, param, libs}`，就自动获得：流式提取、前端 fancy 渲染、交互回传。
> 主仓库零硬编码 —— 无一处 `display_html` 字符串。

### 9.1 元数据驱动链路（全链路通用）

```
工具声明（任意插件）                 消费方（全部读元数据，不读名字）
┌─────────────────────────┐
│ ChannelToolDecl:        │
│   name: "render_chart"  │→ engine_wire.streamToolCallFunc
│   ui: {                 │    a.toolUIDecl(progressKey, tc.Name)
│     mode: "genui",      │    → ui.Mode=="genui" ? 提取 ui.Param : 跳过
│     param: "tsx",       │
│     libs: ["echarts"]   │→ execute_tool result {ui_code} → bridge 发 genui 消息
│   }                     │
│   channels: ["web"]     │→ handleChannelTools → RegisterForChannel(每 channel)
└─────────────────────────┘
        ↓ AsDefinitionsForSession 注入 LLM（工具 schema 本身即声明）
        ↓ 工具执行 result.ui_code → ChannelToolBridge.Execute → ctx.SendFunc
        ↓ protocol.ToolProgress.UIMode/UILibs（由 engine_wire 从声明填充）
        ↓ 前端：tool.ui_mode==='genui' → GenUIBlock（不再 tool.name==='display_html'）
```

### 9.2 通用接口设计

**tools 包新增（不依赖 plugin，避免循环依赖）**：

```go
// tools/ui_decl.go
package tools

// UIDecl 描述工具的 UI 能力。任何实现 Tool 的工具（内置/插件/MCP）都可声明。
type UIDecl struct {
    Mode  string   // "genui" = TSX 自由代码（当前支持）；未来可扩展 "markdown"/"html"
    Param string   // 承载 UI 代码的参数名（流式提取的目标字段）
    Libs  []string // 提示前端注入的全局库（echarts/three/motion…）
}

// UIDeclProvider 可选接口：工具实现它即声明 UI 能力。
type UIDeclProvider interface {
    UIDecl() *UIDecl // nil = 无 UI 能力
}
```

**plugin 包**：`ChannelToolDecl` 增加 `Channels []string` + `UI *UIDecl`（JSON 字段 `channels`/`ui`），
`ChannelToolBridge` 实现 `tools.UIDeclProvider`（返回 `decl.UI`）。

**engine_wire**：`streamToolCallFunc` 内用 `a.tools.GetForSession(tc.Name, tenantID, sessionKey)` 查工具，
类型断言 `tools.UIDeclProvider` → `UIDecl()` → 提取 `ui.Param`。**完全工具名无关**。

**前端**：`protocol.ToolProgress` 增加 `UIMode`/`UILibs`；渲染判定 `tool.ui_mode==='genui'`。
历史消息无 UIMode 时按名字 fallback（兼容旧记录，过渡期后删除）。

### 9.3 扩展场景（证明通用性，非演示代码）

| 场景 | 插件声明 | 系统行为（零主仓库改动） |
|---|---|---|
| 图表工具 | `ui:{mode:"genui",param:"code",libs:["echarts"]}` | 流式提取 code + 前端注入 echarts + GenUIBlock 渲染 |
| 3D 工具 | `ui:{mode:"genui",param:"scene",libs:["three"]}` | 同上，注入 three |
| 多工具 | 两个工具都声明 ui | 各自独立流式提取（`a.toolUIDecl` 按名查） |
| 非 web channel | `channels:["feishu"]` | 工具仅 feishu 会话可见；genui 消息发往 feishu（由该 channel 决定渲染） |
| 无 UI 工具 | 不声明 ui | 行为与现在完全一致（零影响） |

---

## 10. 形式化证明：正确性 + 流畅性

### 10.1 系统模型

**状态**：`S = (D, E, R, P)`，其中：
- `D` = 工具声明集合（`tools.Registry` + 各插件声明）
- `E` = 流式 UI 提取状态（`streamState.GenUIContent`，engine_wire 持有）
- `R` = 前端渲染状态（`progressStore.genuiContent` + `GenUIBlock` 编译缓存）
- `P` = 持久化状态（`ToolResult.Detail` → session_messages → 历史重建）

**操作**：
- `decl(name, ui)` — 插件声明工具（D 更新）
- `extract(tc)` — 流式提取（E 更新）
- `push(ui)` — 完整 UI 推送（genui 消息）
- `compile(ui)` — 前端编译（R 更新）
- `commit(tool)` — 工具结果持久化（P 更新）
- `action(a, d)` — 用户交互回传

### 10.2 不变量

- **I1（元数据完备）**：`∀ tool ∈ D : tool 的 ui 声明 ⇔ engine_wire 对该工具执行提取`。
  证明：`streamToolCallFunc` 只对 `UIDeclProvider.UIDecl() != nil` 的工具提取（engine_wire 逻辑），
  且提取参数名 = `ui.Param`（声明即定义）。工具名不参与判定 → I1 不随工具名变化而破坏。
- **I2（流式单调）**：流式提取的 `E.GenUIContent` 是累积的（每次覆盖为更长或相等的完整前缀）。
  证明：`extractPartialParam` 每次从 `tc.Arguments`（LLM 流式增量累积的完整参数 JSON）重新提取
  当前完整值 → 覆盖写（非追加）。`E = 最新完整前缀`，单调不减。
- **I3（完整覆盖）**：工具执行结束时，`P.Detail` 包含完整 UI 代码。
  证明：`ChannelToolBridge.Execute` 解析 `ui_code` → 写入 `ToolResult.Detail`；内置逻辑同样
  （`display_html` 的 Detail=code）。持久化在迭代提交时写入 session_messages（append-only）。
- **I4（渲染等价）**：`R` 显示的组件与 `P.Detail`（或 `E`）中的代码语义等价。
  证明：`compile(ui)` 是确定性函数（sucrase 编译，无随机）；编译缓存以 code hash 为键，
  同 code 必命中同组件。若代码非法 → error boundary 显示 fallback（不崩溃、不显示错误内容）。
- **I5（交互可达）**：用户点击 `data-action` 最终到达 agent（或插件）。
  证明：GenUIBlock 的 `handleClick` 捕获冒泡，`ws.rpc('genui_action')` 是可靠投递（RPC 有响应/错误处理）；
  服务端 `genui_action` → `InjectAsyncMessage` 进入 agent loop 消息队列（busy 合成 tool result / idle 新 turn）。
- **I6（历史恢复）**：刷新/重连后 UI 从 `P` 重建。
  证明：`AssistantMessage` 从迭代历史提取 `UIMode==='genui'` 工具 → `tool.detail`（=完整代码）
  → GenUIBlock 渲染。`I3` 保证 Detail 完备。

### 10.3 正确性定理

- **T1（端到端可达）**：LLM 生成完整 UI 代码后，前端最终渲染等价 UI。
  证明：两条路径都收敛——
  1. 流式路径：`extract` 单调逼近完整代码（I2），最后一次提取 = 完整 code；`push` 或 `commit` 后 `R` 收到完整值。
  2. 提交路径：`I3` 保证 `P.Detail` = 完整 code；`I4` 保证 `compile` 渲染等价组件；`I6` 保证任何时刻可恢复。
  流式路径可能丢中间帧，但最终值由提交路径权威补齐（与 web 消息一致性的 pull-authoritative 原则相同）。
- **T2（无幽灵 UI）**：前端不会渲染 LLM 未生成的 UI。
  证明：`R` 只接受来自 `E`（流式提取）或 `P`（提交 Detail）的代码，二者都源自 LLM 工具调用。
  `compile` 在 iframe opaque origin 内执行，无法访问父页面状态；编译失败 → error boundary，不合成内容。
- **T3（交互一致）**：`action` 携带的数据与用户点击的元素属性一致，且不跨会话混淆。
  证明：`handleClick` 从 `e.target` 向上冒泡收集 `data-*` 属性（确定性 DOM 遍历）；RPC 携带
  `chat_id`（前端 `effectiveChatId`）→ 服务端按 chatID 注入 → 不会进错会话。
- **T4（持久化幂等）**：UI 在消息流中最多渲染一次，刷新后重建且不重复。
  证明：提交路径中 GenUI 从 `iterations` 提取渲染（"永不折叠"位置），live 路径只渲染
  `progress.genuiContent`；`AssistantMessage` 在 committed 后替换 live（既有 turn 边界原子性
  —— 见 web-linearizability.md T5）。历史重建去重由 MessageStore 结构保证。

### 10.4 流畅性定理（无闪烁、无跳动、无卡顿）

- **T5（流式编译节流）**：流式期间前端编译频率 ≤ 10Hz，不阻塞主线程。
  证明：`GenUIBlock` 用 100ms 节流窗口（timer + `lastRenderRef`）；`compileAndLoad` 在
  `setTimeout` 内执行，React 渲染异步批处理。每 tick 最多一次 sucrase 编译（~1ms 级）。
- **T6（无闪烁）**：编译失败时保持上一可用组件，不闪空白。
  证明：`compileCache` 缓存成功组件；`catch` 分支保留 `component` 状态（"keep previous component,
  no flash to empty"）。`codeHash` 不变 → 命中缓存直接复用。
- **T7（无高度跳动）**：流式期间 iframe 高度只增不缩；完成后精确设置。
  证明：`ResizeObserver` 测量 `doc.body.scrollHeight`；`streaming=true` 时 `h > heightRef` 才更新
  （只增）；`streaming=false` 时无条件设置（精确收敛）。2px 死区防止滚动条振荡。
- **T8（无内容回退）**：流式更新是"覆盖为更长值"而非追加/清空。
  证明：`setGenUIContent` 用整体替换（progressStore.ts `draft.genuiContent = content`）；
  组件 hash 变化 → 重新编译；hash 相同（前缀未变）→ 缓存命中零重编译。配合 I2，视觉上文本持续增长。
- **T9（迭代边界连续）**：迭代切换不清空 GenUI，跨迭代保持最后 UI。
  证明：`setStructuredTools` 的迭代边界只清 `streamContent`/`activeTools` 等流字段；
  `genuiContent` 保留到 `reset`（turn 结束）或新 genui 到达（覆盖）。LiveIteration 在 `hasGenUI`
  时渲染，不因迭代号变化而消失。

### 10.5 证明的诚实标注（局限）

| # | 局限 | 说明 |
|---|---|---|
| L1 | 流式中间帧可能丢失（SSE 弱网） | 最终一致由 T1 提交路径兜底；中间帧是 best-effort 预览 |
| L2 | `compile` 失败时显示 error boundary，不等价原 UI | 这是安全降级（I4），不违反正确性（不显示错误内容） |
| L3 | 极端恶意代码可通过数据属性注入超长内容 | `data-*` 收集有上限（单元素属性数），RPC 层不设硬限制 —— 低风险，插件信任模型同 web_ui |
| L4 | iframe 内 `XBOT_UI` 库注入依赖父页面网络 | 库加载失败时组件 fallback（Chart 显示 option 文本），不白屏 |

---

## 11. 待办决策点（评审时确认）

1. **库来源**：CDN（jsdelivr，默认）vs 本地 bundle（`static/libs/` 随插件分发）vs 主仓库 web 打包。→ 建议 CDN 默认 + 插件 config 可切本地。
2. **组件库范围**：首版组件集（Button/Card/Table/Stat/Sparkline/Progress/Badge/Tabs/Modal/Form/Toast）是否够；Chart 是否支持多 series/动画。
3. **genui_action 拦截**：插件是否默认拦截交互（维护 UI 状态）还是默认回传 agent。→ 建议默认回传 agent（兼容现状），插件可选拦截。
4. **`ui_session` 状态保持**：首版是否需要跨迭代 UI 状态（如多步表单）。→ 建议 M5 后续版本，首版无状态。
5. **是否保留 `tools/display_html.go` 作为插件侧迁移参考**：建议删除主仓库文件，逻辑迁移到插件 `internal/tool/`（避免双实现）。
