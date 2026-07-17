# 请求链路日志追踪审查报告

## 概览

**审查目标**: 验证 xbot 项目整个请求链路中日志是否携带 requestID 等关键追踪信息，保证日志可追踪。

**审查范围**: `logger/`, `agent/`, `llm/`, `tools/`, `channel/`, `serverapp/`, `plugin/`, `agent/hooks/`, `cron/`, `bus/`, `protocol/`

**审查结论**: ⚠️ **部分可追踪** — 基础设施已具备（`logger/context.go` + `InboundMessage.RequestID`），主请求链路（agent loop → LLM）覆盖良好（77%-90%），但 **RPC 路径、SubAgent 继承、工具执行、插件系统、Cron 触发** 5 个关键环节存在断链。

---

## 基础设施评估

### 已有的 requestID 基础设施 ✅

| 组件 | 位置 | 说明 |
|------|------|------|
| `logger/context.go` | `WithRequestID(ctx, id)` | 将 requestID 注入 context |
| `logger/context.go` | `RequestID(ctx)` | 从 context 提取 requestID |
| `logger/context.go` | `NewRequestID()` | 生成 UUID（无横线） |
| `logger/context.go` | `Ctx(ctx)` | 返回带 `request_id` 字段的 logrus Entry |
| `bus.InboundMessage.RequestID` | `bus/bus.go:82` | 消息总线入站消息携带 requestID |
| `protocol.WSMessage.RequestID` | `protocol/events.go:230` | WS 消息协议携带 requestID |
| `protocol.ProgressEvent.RequestID` | `protocol/events.go:85` | 进度事件携带 requestID |

**结论**: 基础设施设计合理，`log.Ctx(ctx)` 模式是正确的使用方式。

---

## 请求链路追踪分析

### 1. 请求入口（requestID 生成）

| 入口 | 生成 requestID? | 位置 | 方式 |
|------|------------------|------|------|
| 飞书消息 | ✅ | `feishu.go:850` | `log.NewRequestID()` + `log.WithField("request_id", requestID)` |
| CLI 消息 | ✅ | `cli_inbound.go:24` | `strings.ReplaceAll(uuid.New().String(), "-", "")` |
| NapCat 消息 | ✅ | `napcat.go:441` | `log.NewRequestID()` |
| Web WS 消息 | ✅ | `web.go:1146` | `strings.ReplaceAll(uuid.New().String(), "-", "")` |
| Cron 触发 | ❌ | `cron/scheduler.go` | 无 requestID 生成 |
| SubAgent | ❌ | `engine.go:889-949` | `buildMsg` 未设置 `RequestID` |
| RPC 调用（本地） | ❌ | `transport_channel.go:32` | `context.Background()` 无 requestID |
| RPC 调用（远程） | ❌ | `server.go:798` | `context.Background()` 无 requestID |

### 2. requestID 注入 context

**唯一注入点**: `agent/agent.go:2536-2540` (`processMessage`)

```go
func (a *Agent) processMessage(ctx context.Context, msg bus.InboundMessage) (*channel.OutboundMsg, error) {
    reqID := msg.RequestID
    if reqID == "" {
        reqID = log.NewRequestID()
    }
    ctx = log.WithRequestID(ctx, reqID)
    // ...
}
```

✅ 主请求链路正确：消息通过 `msgBus.Inbound` → `processMessage` 注入 context。

❌ RPC 路径无注入：`serverapp/rpc_table.go:1344` 和 `server.go:798` 使用 `context.Background()` 创建 RPC context，未注入 requestID。

### 3. requestID 在链路中的传播

```
[渠道入口] → msgBus.Inbound → processMessage → Run() → engine loop
     ✅            ✅              ✅            ✅        ✅
                                                    ↓
                                            callLLM → generateResponse → llm/openai.go
                                              ✅          ✅                    ✅ (26/29 calls use log.Ctx)
                                                    ↓
                                            defaultToolExecutor → buildToolContext → Tool.Execute
                                              ✅ ctx携带          ✅ ctx传递        ❌ 0/39 使用 log.Ctx
                                                    ↓
                                            hooks.Manager.Emit
                                              ⚠️ ctx携带但 1/13 使用 log.Ctx
                                                    ↓
                                            SubAgent (buildMsg)
                                              ❌ RequestID 未设置，SubAgent 生成新 ID
```

### 4. 各模块 log.Ctx(ctx) 覆盖率

| 模块 | log.Ctx(ctx) 调用 | 裸 log.Xxx() 调用 | 覆盖率 | 评级 |
|------|-------------------|-------------------|--------|------|
| `agent/` (核心) | 114 | 35 | 77% | ⚠️ |
| `llm/` | 26 | 3 | 90% | ✅ |
| `tools/` | 0 | 39 | 0% | ❌ |
| `channel/` | 0 | 42 | 0%* | ❌ |
| `serverapp/` | 0 | 11 | 0% | ❌ |
| `plugin/` | 0 | 12 | 0% | ❌ |
| `agent/hooks/` | 1 | 12 | 8% | ❌ |
| `config/` | 0 | 3 | 0% | — |
| `cron/` | 0 | 4 | 0% | — |
| `memory/` | 0 | 2 | 0% | — |

> *channel/ 在入口处使用 `log.WithField("request_id", requestID)` 手动添加，但下游代码不再携带。

---

## 发现的问题

### 严重 (Critical)

#### C1. SubAgent 不继承父 Agent 的 requestID

**位置**: `agent/engine.go:889-949` (`buildMsg`)

`buildMsg` 构建 SubAgent 的 `InboundMessage` 时未设置 `RequestID` 字段：

```go
return bus.InboundMessage{
    From: bus.NewIMAddress(a.channel, a.senderID),
    Channel: bus.SchemeAgent,
    // ... 其他字段
    // ❌ 缺少 RequestID
}
```

**影响**: SubAgent 的 `processMessage` 会生成一个全新的 requestID，与父 Agent 的请求无法关联。当 SubAgent 执行出错时，无法通过父 Agent 的 requestID 追溯到 SubAgent 的日志。

**建议**: 在 `buildMsg` 中从 `parentCtx.Ctx` 提取 requestID 并传递：

```go
parentReqID := log.RequestID(parentCtx.Ctx)
return bus.InboundMessage{
    // ...
    RequestID: parentReqID, // 继承父 Agent 的 requestID
}
```

---

#### C2. RPC 路径完全没有 requestID

**位置**: 
- `serverapp/rpc_table.go:1344` — `WithRPCCtxResolved(context.Background(), ...)`
- `server.go:798` — `WithRPCCtxResolved(context.Background(), ...)`
- `cmd/xbot-cli/main.go:873-874` — `ctxFn` 返回 `context.Background()` 无 requestID
- `agent/transport_channel.go:32` — `ChannelTransport.Call` 使用 `ctxFn()` 或 `context.Background()`

**影响**: 所有 RPC 调用（`/settings`、`/set-llm`、`/models`、会话管理、订阅管理、工具调用等）的日志**完全无法追踪**。RPC handler 中的 11 处 `log.Xxx()` 调用没有任何追踪标识。在远程模式下，CLI → WS → Server 的 RPC 请求无法与用户的消息关联。

**建议**: 

1. 在 `Client.call()` 中生成 requestID 并注入 WS 消息 header：
```go
// agent/client.go
func (c *Client) call(method string, payload json.RawMessage, ...) (json.RawMessage, error) {
    reqID := log.NewRequestID()
    // 注入 WS 消息 header
    msg.Header = map[string]string{"request_id": reqID}
    // ...
}
```

2. 在 RPC dispatch 入口提取并注入 context：
```go
// serverapp/server.go / rpc_table.go
func handleCLIRPC(ctx context.Context, ...) {
    reqID := wsMsg.Header["request_id"]
    if reqID == "" { reqID = log.NewRequestID() }
    ctx = log.WithRequestID(ctx, reqID)
    ctx = WithRPCCtxResolved(ctx, senderID, bizID, userID, role)
    return rpcTable.Dispatch(ctx, method, params)
}
```

---

#### C3. 工具执行日志完全无 requestID

**位置**: `tools/` 目录，39 处裸 `log.Xxx()` 调用

虽然 `ToolContext.Ctx` 携带了带 requestID 的 context（`buildToolContext` 正确传递了 `ctx`），但工具代码全部使用 `log.Xxx()` 而非 `log.Ctx(ctx)`：

```go
// tools/docker_sandbox.go:65 — 当前
log.Infof("Stopped Docker container %s", c.name)

// 应改为
log.Ctx(ctx).Infof("Stopped Docker container %s", c.name)
// 但 docker_sandbox.go 的方法没有 ctx 参数 — 需要通过 ToolContext.Ctx 传递
```

**影响**: 工具执行过程中的日志（Docker 容器管理、sandbox 路径映射、MCP 配置解析等）无法与触发它的用户请求关联。当工具执行出错时，无法通过 requestID 追溯。

**建议**: 工具内部日志改为使用 `log.Ctx(tc.Ctx)`，至少在关键路径（错误、警告）上添加 requestID。

---

#### C4. Cron 触发的任务无 requestID

**位置**: `cron/scheduler.go`

Cron job 触发时通过 `InjectInbound` 注入消息，但未生成 requestID：

```go
// cron 触发的消息进入 msgBus.Inbound 时没有 RequestID
// processMessage 会生成新的，但无法区分是 cron 触发还是用户触发
```

**影响**: 定时任务的执行日志无法与 cron job ID 关联，排查定时任务问题时无法区分不同 job 的日志。

**建议**: Cron 触发时生成 requestID 并写入 `EventSource`/`EventTrigger` 元数据：

```go
msg := bus.InboundMessage{
    // ...
    RequestID:    log.NewRequestID(),
    EventSource:  "cron",
    EventTrigger: job.ID,
}
```

---

### 重要 (Major)

#### M1. Channel 适配器入口后日志断链

**位置**: `channel/feishu/`, `channel/web/`, `channel/qq/`, `channel/napcat/`

各渠道在入口处正确生成 requestID 并用 `log.WithField("request_id", requestID)` 记录日志，但后续处理链路中的日志不再携带 requestID：

```go
// feishu.go:850-851 — 入口正确
requestID := log.NewRequestID()
l := log.WithField("request_id", requestID)

// feishu.go 后续 — 断链
log.Infof("Sending message to chat %s", chatID)  // ❌ 没有 requestID
```

**影响**: 渠道内部处理逻辑（消息发送、错误处理、回调处理）的日志无法追踪。

**建议**: 渠道内部日志统一使用 `l.WithField(...)` 或 `log.Ctx(ctx)` 模式。

---

#### M2. Hooks Manager 几乎不使用 requestID

**位置**: `agent/hooks/manager.go` — 12 处裸 `log.Xxx()`，仅 1 处 `log.Ctx(ctx)`

```go
// manager.go:165 — 当前
log.Warnf("hooks: event %s matched %d handlers, truncating to 10", eventName, len(handlers))

// manager.go:202 — 当前
log.Errorf("hooks: handler %q panicked: %v — skipping", name, r)
```

**影响**: Hook 执行出错时无法关联到触发它的请求。Hook 的 deny/defer 决策日志无法追踪。

**建议**: `Manager.Emit` 方法已接收 `ctx`，内部日志改为 `log.Ctx(ctx)`。

---

#### M3. Plugin 系统无 requestID

**位置**: `plugin/` 目录 — 12 处裸 `log.Xxx()`

插件管理器、runtime、widget 渲染等日志完全没有追踪标识。插件执行（hook 触发、工具调用、widget 刷新）无法关联到用户请求。

**建议**: `PluginContext` 已携带 `context.Context`，插件内部日志应使用 `log.Ctx(ctx)`。

---

#### M4. ServerApp RPC handler 无 requestID 且无关键业务字段

**位置**: `serverapp/rpc_table.go`, `serverapp/server.go`

11 处 `log.Xxx()` 调用不仅没有 requestID，也缺少 `senderID`、`chatID` 等业务标识。例如：

```go
// server.go:595 — 当前
log.Warn("GetDefault returned nil — no default or system subscription in DB")

// 应改为
log.Ctx(ctx).WithFields(log.Fields{
    "sender_id": rpcBizID(ctx),
}).Warn("GetDefault returned nil — no default or system subscription in DB")
```

---

#### M5. agent/ 核心模块仍有 35 处裸日志调用

**位置**: `agent/agent.go`, `agent/engine_wire.go`, `agent/interactive.go`, `agent/user_context_middleware.go`, `agent/context.go`

这些调用分布在关键路径上，包括：
- `agent.go:2031` — Cancel signal 日志
- `agent.go:2445/2451` — 发送响应/取消确认失败
- `engine_wire.go:317` — 进度发送失败
- `interactive.go:483/663` — SubAgent 会话清理失败
- `user_context_middleware.go:30` — 用户上下文解析失败

**影响**: 关键错误路径的日志无法追踪。

**建议**: 这些函数大多有 `ctx` 参数可用，改为 `log.Ctx(ctx)`。

---

#### M6. 远程传输（RemoteTransport）无 requestID 关联

**位置**: `agent/transport_remote.go`

RemoteTransport 的 RPC 调用使用 `requestID` 作为 pending call 的内部追踪键（`transport_remote.go:80`），但这个 requestID 仅用于 RPC 请求/响应匹配，**未注入 context**，也未出现在日志中。

**影响**: 远程模式下 CLI → Server 的 RPC 调用完全无法追踪。WS 断连重连时的日志也无法关联。

---

### 一般 (Minor)

#### m1. ChannelTransport.Call 的 ctxFn 不注入 requestID

**位置**: `agent/transport_channel.go:31-37`

```go
func (t *ChannelTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
    ctx := context.Background()
    if t.ctxFn != nil {
        ctx = t.ctxFn()  // ← 仅注入 auth，不注入 requestID
    }
    return t.dispatch(ctx, method, payload)
}
```

`ctxFn` 在 `main.go:873` 中只设置了 `WithRPCCtxResolved`，没有 `WithRequestID`。

---

#### m2. 飞书 AskUser 回调的 requestID 依赖 metadata 传递

**位置**: `agent/agent_process.go:507`, `channel/cli/cli_askuser_persist.go:49`

AskUser 的 requestID 通过 `metadata["request_id"]` 手动传递，而非通过 context 链。虽然功能正确，但模式不一致。

---

#### m3. 日志缺少统一的 trace span 概念

当前只有 `request_id` 一个维度，没有 span_id（单次工具调用/LLM 调用的标识）。当一个 request 内有多次工具调用时，同一 requestID 下的日志混在一起，难以区分具体是哪次工具调用产生的。

---

#### m4. config/、memory/、cron/ 模块的日志无追踪信息

这些模块的日志（共 9 处）都是启动/配置级别的日志，不直接与用户请求关联，优先级较低。但如果 cron job 执行出错，`cron/scheduler.go` 中的日志无法追踪到具体的 job。

---

## 完整链路追踪矩阵

```
用户消息入口
├── 飞书 ──── ✅ 生成 requestID ──→ msgBus ──→ processMessage ──→ ctx 注入 ✅
├── CLI ───── ✅ 生成 requestID ──→ msgBus ──→ processMessage ──→ ctx 注入 ✅
├── NapCat ── ✅ 生成 requestID ──→ msgBus ──→ processMessage ──→ ctx 注入 ✅
├── Web ───── ✅ 生成 requestID ──→ msgBus ──→ processMessage ──→ ctx 注入 ✅
├── Cron ──── ❌ 无 requestID ────→ msgBus ──→ processMessage ──→ 生成新 ID ⚠️
└── SubAgent ─❌ 无 requestID ────→ msgBus ──→ processMessage ──→ 生成新 ID ❌ (断链)

请求处理链路
processMessage (ctx 有 requestID)
├── Run() ──────────── ✅ log.Ctx(ctx) 114 处
├── callLLM ─────────── ✅ ctx 链路保持
│   └── llm/openai.go ─ ✅ log.Ctx(ctx) 26 处
│   └── llm/retry.go ── ✅ log.Ctx(ctx) 2 处
├── defaultToolExecutor  ✅ ctx 传递到 ToolContext.Ctx
│   └── tools/*.go ──── ❌ 0 处 log.Ctx(ctx)，39 处裸调用
├── hooks.Manager ───── ⚠️ ctx 传递，1/13 使用 log.Ctx(ctx)
├── SubAgent ────────── ❌ buildMsg 不传 requestID
└── 进度事件 ─────────── ✅ ProgressEvent.RequestID 携带

RPC 链路（完全断链）
├── ChannelTransport.Call ── ❌ context.Background() 无 requestID
├── RemoteTransport.Call ─── ❌ 内部 requestID 仅用于匹配，不在日志中
├── server.go RPC dispatch ─❌ context.Background() 无 requestID
└── rpc_table.go handlers ── ❌ 0 处 log.Ctx(ctx)，11 处裸调用

Channel 内部处理
├── feishu.go 入口 ──── ✅ log.WithField("request_id", ...)
├── feishu.go 后续 ──── ❌ 裸 log.Xxx()
├── web.go 入口 ─────── ✅ 生成 requestID
├── web.go 后续 ─────── ❌ 裸 log.Xxx()
└── napcat/qq ───────── ❌ 裸 log.Xxx()
```

---

## 修复优先级建议

| 优先级 | 问题 | 影响范围 | 修复复杂度 |
|--------|------|----------|-----------|
| P0 | C2: RPC 路径注入 requestID | 全部 RPC 调用 | 中（需改 Client + Server） |
| P0 | C1: SubAgent 继承 requestID | SubAgent 链路 | 低（buildMsg 加一行） |
| P1 | C3: 工具日志改用 log.Ctx(ctx) | 工具执行 | 中（39 处，部分需传 ctx） |
| P1 | M1: Channel 后续日志断链 | 渠道内部处理 | 中（42 处） |
| P1 | M5: agent/ 35 处裸日志 | 核心路径 | 低（ctx 已可用） |
| P2 | C4: Cron 触发生成 requestID | 定时任务 | 低 |
| P2 | M2: Hooks Manager | Hook 执行 | 低（ctx 已可用） |
| P2 | M3: Plugin 系统 | 插件执行 | 中 |
| P3 | M4: ServerApp handler | RPC 日志 | 低 |
| P3 | M6: RemoteTransport | 远程 RPC | 中 |

---

## 总结

xbot 项目的请求链路日志追踪能力 **主链路覆盖良好，但存在 5 处关键断链**：

1. ✅ **基础设施完善**：`logger/context.go` + `log.Ctx(ctx)` 模式设计正确
2. ✅ **主请求链路完整**：渠道入口 → processMessage → engine → LLM，requestID 正确注入和传播
3. ✅ **LLM 层覆盖最佳**：90% 的日志使用 `log.Ctx(ctx)`
4. ❌ **RPC 路径完全断链**：所有 RPC 调用无 requestID（最严重）
5. ❌ **SubAgent 不继承 requestID**：嵌套调用无法关联
6. ❌ **工具执行无追踪**：39 处日志无 requestID
7. ❌ **Cron 无追踪**：定时任务无法区分
8. ⚠️ **Channel 入口后断链**：入口正确但后续丢失

**修复核心思路**: 确保每个进入系统的请求在生成 requestID 后，通过 context 链一路传递到所有下游组件，日志统一使用 `log.Ctx(ctx)` 而非裸 `log.Xxx()`。
