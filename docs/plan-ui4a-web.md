# 计划：为 xbot WebUI 实现 UI4A

> 生成时间：2026-07-19
> 状态：待确认

## 背景与目标

在 xbot web 渠道实现流式 Generative UI：agent 调用 `display_html` 工具生成 HTML+Tailwind 代码，前端增量解析并实时渲染。用户与生成 UI 的交互（按钮点击等）通过 bgnotify 管道回注给 agent。

核心设计原则（来自实验验证）：
- **不编译，直接解析**：增量 HTML 扫描器，不依赖 Babel/WASM
- **HTML + Tailwind**：不搞组件注册表，LLM 写原生 HTML
- **`{variable}` 插值 + `onclick` 表达式**：轻量状态绑定
- **防抖动三件套**：`<` 孤立停止、不完整属性跳过、rAF 批处理 + growing min-height

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `tools/display_html.go` | **新增**：`display_html` 工具定义 | 新增 |
| `tools/task_manager.go` | `AsyncMessageNotification` 已有，新增 `AsyncSourceUIAction` | 修改 |
| `agent/agent.go` | `injectAsyncMessage` 已有，无需改动 | 不改 |
| `agent/engine_wire.go` | `buildStreamCallbacks` 中 `streamContentFunc` 已推送 `stream_content` | 不改 |
| `channel/web/web.go` | `SendStreamContent` 已推送 `stream_content` 到前端 | 不改 |
| `protocol/ws.go` | 新增 `MsgTypeGenUI` 消息类型 | 修改 |
| `protocol/events.go` | `ProgressEvent` 新增 `GenUIContent` 字段 | 修改 |
| `web/src/genui/` | **新增**：前端 GenUI 核心模块目录 | 新增 |
| `web/src/hooks/useProgressStream.ts` | 处理 `stream_content` 中的 GenUI 内容 | 修改 |
| `web/src/components/agent/LiveIteration.tsx` | 渲染 GenUI 预览 | 修改 |
| `web/src/types/shared.ts` | 新增 GenUI 相关类型 | 修改 |

### 数据流

```
Agent LLM 流式输出
  │
  │  tool_call: display_html { code: "<div class='...'>...", state: {count:0} }
  │
  ▼
streamContentFunc(content)  ← 已有机制，content 是累积的 tool arguments
  │
  ▼
WebChannel.SendStreamContent → WSMessage{type:"stream_content", progress.stream_content}
  │
  ▼
前端 useProgressStream → stream_content 事件
  │  检测到 active tool 是 display_html
  │  从 streaming_tools 中提取 partial arguments
  │  { "code": "<div class='...'>..." }  ← 部分 JSON
  ▼
GenUIPreview 组件
  │  parseHtml(partialCode) → UINode 树
  │  rAF 批处理 + lastGoodRef + growing min-height
  │  渲染为 React 元素
  ▼
用户点击按钮
  │  onclick="setCount(count+1)" → 本地状态更新
  │  data-action="confirm_booking" → 回调
  ▼
POST /api/rpc { method: "genui_action", params: { chat_id, action, data } }
  │
  ▼
RPC handler → agent.injectAsyncMessage(channel, chatID, senderID, content, "ui_action")
  │
  ▼
bgnotify 管道（已有机制）
  │  Busy: 合成 tool call/result 注入当前 Run
  │  Idle: 用户消息注入触发新 turn
  ▼
Agent 收到回调，继续处理
```

## 详细计划

### 阶段一：Go 侧 — `display_html` 工具 + 消息协议

#### 1.1 新增 `display_html` 工具

**文件**：`tools/display_html.go`（新增）

```go
type DisplayHTMLTool struct{}

func (t *DisplayHTMLTool) Name() string { return "display_html" }
func (t *DisplayHTMLTool) Description() string {
    return "Render an interactive HTML UI for the user. Use Tailwind CSS classes for styling. " +
           "Supports {variable} interpolation in text and onclick='setX(...)' for state. " +
           "Declare initial state in a <script>state = { count: 0 }</script> block. " +
           "Use data-action='action_name' on elements to trigger agent callbacks on click."
}
func (t *DisplayHTMLTool) Parameters() []llm.ToolParam {
    return []llm.ToolParam{
        {Name: "code", Type: "string", Description: "Complete HTML module with Tailwind CSS classes", Required: true},
        {Name: "state", Type: "object", Description: "Initial state variables as JSON key-value pairs", Required: false},
    }
}
```

Execute 方法：
- 解析 `code` 和 `state` 参数
- 通过 `ctx.SendFunc` 发送 `MsgTypeGenUI` 消息（携带完整 HTML + state）
- 返回 `NewResult("UI rendered")` — 工具本身不等待用户交互
- **大 HTML offload**：如果 code 超过阈值，写入 offload 文件，summary 中引用文件名

#### 1.2 注册为 web channel 专属工具

**文件**：`agent/agent.go`（修改 `initStores` 或 `initServices`）

```go
// 在 initStores 中，与 feishu card tools 注册并列
registry.RegisterForChannel("web", &tools.DisplayHTMLTool{})
```

#### 1.3 消息协议扩展

**文件**：`protocol/ws.go`（修改）

```go
MsgTypeGenUI = "genui"  // 新增消息类型
```

**文件**：`protocol/events.go`（修改）

```go
// ProgressEvent 新增字段
GenUIContent string `json:"genui_content,omitempty"`  // 流式 GenUI HTML
```

#### 1.4 流式推送

**关键设计**：不新增流式通道。复用已有的 `streamContentFunc` 机制。

LLM 流式生成 `display_html` 的 tool arguments 时，`streamContentFunc` 已经被调用（推送累积的 LLM 文本输出）。但 tool arguments 的流式增量走的是 `streamToolCallFunc`，不是 `streamContentFunc`。

**方案**：在 `streamToolCallFunc` 中检测 tool name 为 `display_html` 时，提取 partial arguments 中的 `code` 字段，推送到前端。

**文件**：`agent/engine_wire.go`（修改 `buildStreamCallbacks`）

```go
streamToolCallFunc = func(toolCalls []llm.ToolCallDelta) {
    // ... 已有逻辑 ...
    // 检测 display_html tool，提取 partial code
    for _, tc := range toolCalls {
        if tc.Name == "display_html" {
            // tc.Arguments 是 partial JSON: {"code":"<div class='...'>...
            code := extractPartialCode(tc.Arguments)
            if code != "" {
                broadcastProgress(&protocol.ProgressEvent{
                    ChatID:        progressKey,
                    GenUIContent:  code,
                })
            }
        }
    }
}
```

#### 1.5 GenUI 回调 RPC

**文件**：`serverapp/rpc_table.go`（修改）

```go
t["genui_action"] = rpc1(func(ctx context.Context, p struct {
    ChatID string `json:"chat_id"`
    Action string `json:"action"`
    Data   string `json:"data"`  // JSON string of action data
}) (json.RawMessage, error) {
    content := fmt.Sprintf("🖱️ [UI Action] %s\nData: %s", p.Action, p.Data)
    h.Ag.injectAsyncMessage("web", p.ChatID, "", content, tools.AsyncSourceUIAction)
    return json.RawMessage(`{"ok":true}`), nil
})
```

**文件**：`tools/task_manager.go`（修改）

```go
const AsyncSourceUIAction = "ui_action"
```

**文件**：`channel/web/web_rest.go`（修改）

在 RPC 白名单中添加 `"genui_action"`。

### 阶段二：前端 — GenUI 渲染引擎

#### 2.1 GenUI 核心模块

**文件**：`web/src/genui/streamParser.ts`（新增，从 demo 项目移植）

从实验项目移植 HTML 增量解析器，包括：
- `parseHtml(code)` → `ParseResult`
- `HtmlScanner` 状态机
- `findTagClose` — 标签完整性检测
- `tryScanInterpolation` — `{expression}` 插值
- `<` 孤立停止、不完整属性跳过

**文件**：`web/src/genui/store.ts`（新增，从 demo 项目移植）

`GenUIStore` + `useGenUIStore` hook。

**文件**：`web/src/genui/expressionEval.ts`（新增，从 demo 项目移植）

`evalExpression` — `new Function` 求值。

**文件**：`web/src/genui/GenUIPreview.tsx`（新增，从 demo 项目移植）

UINode 树 → React 元素渲染器。包括：
- `onclick` → `onClick` 转换
- `class` → `className` 转换
- `{variable}` 插值解析
- `data-action` 属性拦截 → RPC 调用

**文件**：`web/src/genui/types.ts`（新增，从 demo 项目移植）

UINode 类型定义。

#### 2.2 Tailwind CSS 引入

**文件**：`web/index.html`（修改）

```html
<script src="https://cdn.tailwindcss.com"></script>
```

或更好：在 `web/package.json` 中添加 `tailwindcss` 依赖，在 vite 配置中集成（xbot web 已有 `@tailwindcss/vite`）。

#### 2.3 消息流集成

**文件**：`web/src/types/shared.ts`（修改）

```typescript
export type WSMessageType = 
  | 'text'
  | 'progress_structured'
  | 'stream_content'
  | 'genui'           // 新增
  | ...

export interface ProgressEvent {
  // ... 已有字段 ...
  genui_content?: string  // 新增
}
```

**文件**：`web/src/hooks/useProgressStream.ts`（修改）

在 `handleProgressMessage` 的 `stream_content` case 中，检测 `genui_content` 字段：

```typescript
case 'stream_content': {
    const p = msg.progress
    if (p.genui_content) {
        store.setGenUIContent(p.genui_content)  // 新增 store 方法
    }
    // ... 已有 stream_content 逻辑 ...
}
```

新增 `genui` 消息类型处理（最终完整 HTML）：

```typescript
case 'genui': {
    store.setGenUIContent(msg.content)
    store.setGenUIState(msg.metadata?.state)
    return
}
```

**文件**：`web/src/components/agent/progressStore.ts`（修改）

新增 `genuiContent` 字段和 `setGenUIContent` 方法。

#### 2.4 渲染组件

**文件**：`web/src/components/agent/GenUIBlock.tsx`（新增）

```tsx
function GenUIBlock({ code, state }: { code: string; state?: Record<string, unknown> }) {
    const storeRef = useRef(new GenUIStore())
    // rAF 批处理 + lastGoodRef + growing min-height
    // 从 demo 项目移植 App.tsx 的调度逻辑
    const parseResult = useMemo(() => parseHtml(code), [code])
    // ...
    return <GenUIPreview nodes={nodes} store={storeRef.current} />
}
```

**文件**：`web/src/components/agent/LiveIteration.tsx`（修改）

在流式迭代渲染中，检测 `genuiContent`，渲染 `<GenUIBlock>`：

```tsx
{snap.genuiContent && (
    <GenUIBlock code={snap.genuiContent} />
)}
```

**文件**：`web/src/components/agent/ToolRender.tsx`（修改）

添加 `display_html` 工具的渲染 case：

```typescript
case 'display_html':
    return <GenUIBlock code={tool.args.code} state={tool.args.state} />
```

#### 2.5 交互回调

**文件**：`web/src/genui/GenUIPreview.tsx`（修改）

`data-action` 属性的元素点击时发送 RPC：

```tsx
if (name.startsWith('data-action')) {
    props['data-action'] = value
    // onClick 拦截
    props.onClick = () => {
        rpc('genui_action', { chat_id: currentChatId, action: value, data: JSON.stringify(store.getContext()) })
    }
}
```

### 阶段三：Offload 与 Agent 上下文

#### 3.1 大 HTML Offload

**文件**：`tools/display_html.go`（修改 Execute）

```go
func (t *DisplayHTMLTool) Execute(ctx *ToolContext, input string) (*ToolResult, error) {
    // ... 解析参数 ...
    
    // 如果 HTML 超过阈值（如 4KB），offload 到文件
    if len(code) > 4096 {
        filename := fmt.Sprintf("genui_%s.html", generateID())
        filepath := filepath.Join(ctx.WorkingDir, ".xbot", "genui", filename)
        os.WriteFile(filepath, []byte(code), 0o644)
        
        summary := fmt.Sprintf("🎨 UI rendered (%d chars). Source: .xbot/genui/%s", len(code), filename)
        return &tools.ToolResult{
            Summary: summary,
            Detail:  summary,  // agent 只看到文件引用，不占 context
        }, nil
    }
    
    // 小 HTML 直接返回
    return tools.NewResult(fmt.Sprintf("🎨 UI rendered (%d chars)", len(code))), nil
}
```

Agent 回调时，content 中引用文件名：
```
🖱️ [UI Action] confirm_booking
Data: {"count":3}
UI Source: .xbot/genui/genui_abc123.html
```

Agent 可以用 Read 工具读取该文件获取完整 HTML 上下文。

#### 3.2 回调内容格式

**文件**：`serverapp/rpc_table.go`（修改 `genui_action` handler）

```go
content := fmt.Sprintf("🖱️ [UI Action] %s\n\nState: %s\nUI Source: %s",
    p.Action, p.Data, p.UISource)
```

`UISource` 由前端在 RPC 请求中携带（从 `display_html` 工具结果中获取文件路径）。

## 验证方案

1. **工具注册**：启动 server，检查 web 会话的 tool definitions 中包含 `display_html`
2. **流式渲染**：agent 调用 `display_html`，前端在流式过程中看到 HTML 逐步渲染
3. **交互回调**：点击生成 UI 中的按钮，agent 收到 bgnotify 消息
4. **Offload**：生成大 HTML（>4KB），检查 `.xbot/genui/` 目录下有文件，agent context 中只有文件引用
5. **防抖动**：流式过程中预览区不闪烁、不抖动

## 风险点

1. **`streamToolCallFunc` 的 partial JSON 解析**：LLM 流式输出的 tool arguments 是不完整的 JSON（`{"code":"<div cla...`）。需要用 `partial-json` 库（xbot 已有依赖）解析 partial JSON 提取 `code` 字段。
2. **Tailwind CSS 与 xbot web 现有样式冲突**：xbot web 已有 Tailwind v4。生成的 HTML 用的 Tailwind 类名需要与现有配置兼容。需确认 `tailwind.config` 的 `content` 字段覆盖动态生成的 HTML。
3. **`data-action` 与 `onclick` 共存**：一个元素可以同时有 `onclick`（本地状态操作）和 `data-action`（agent 回调）。需要明确优先级：先执行本地操作，再发送回调。
4. **多 GenUI 实例**：一个对话中可能有多个 `display_html` 调用。每个需要独立的 store 实例。通过 React key 隔离。

## 注意事项

- **不修改 bgnotify 管道**：回调完全走已有的 `injectAsyncMessage` → `bgRunPending` → drain 管道。不新增注入路径。
- **不修改 WS 协议核心**：只新增消息类型和字段，不改变现有消息流。
- **前端模块独立**：`web/src/genui/` 目录是自包含的，不依赖 xbot web 现有组件（除了 Tailwind 和 RPC 接口）。

✅ 自审通过
