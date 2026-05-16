# 计划：Transport 层彻底解耦 — client → transport → server 三层架构

> 生成时间：2026-05-15
> 状态：待确认

## 背景与目标

### 当前问题

当前 `Transport` 接口职责严重越界：

```
Transport 接口（当前，15 个方法）：
  Call()              ← 唯一真正属于 transport 的
  Start/Stop/Close/Run ← 生命周期管理（不应该在 transport 里）
  SendMessage()        ← 业务方法（调用 Agent.SendInbound）
  BindChat()           ← 事件路由（调用 Agent 内部逻辑）
  SetTUIControlHandler ← UI 回调（和传输无关）
  WireCallbacks()      ← Agent ↔ Channel 双向绑定
  SetChatRenameFn()    ← UI 回调
  ConnState/IsRemote/ServerURL ← 状态查询
  Agent()              ← 直接暴露 Agent 指针
```

Transport 持有 Agent、Bus、Channel Dispatcher，是"上帝对象"。
导致 local 模式不得不维护一套独立的 handler 表（`local_transport.go`，1082 行）。

### 目标架构

```
┌─────────────────────────────────────────────────┐
│                   Client 层                      │
│  Backend (类型安全 RPC 客户端)                     │
│  - 65 个类型安全方法 (GetSettings, ListModels...)  │
│  - 通过 Transport.Call() 发送请求                  │
│  - 不持有 Agent, 不持有 Bus                        │
├─────────────────────────────────────────────────┤
│                  Transport 层                     │
│  纯传输协议，只负责搬运字节                          │
│                                                   │
│  Transport 接口 (精简后)：                         │
│    Call(method, payload) → (result, error)        │
│    Close() error                                  │
│                                                   │
│  实现：                                           │
│  ┌─ ChannelTransport  (进程内直连，local 模式)     │
│  ├─ WSTransport      (WebSocket，remote 模式)     │
│  └─ GRPCTransport    (gRPC，未来)                  │
├─────────────────────────────────────────────────┤
│                   Server 层                       │
│  RPCTable (唯一的 handler truth source)            │
│  - buildRPCTable(agent, ...) → RPCTable           │
│  - 每个方法：Agent.Xxx() → json result             │
│  - 含 auth/middleware/subscription 等 server 逻辑  │
│                                                   │
│  local 模式：直接在进程内调用 RPCTable.Dispatch()   │
│  server 模式：通过 HTTP/WS handler 调用 Dispatch()  │
└─────────────────────────────────────────────────┘
```

**核心原则**：
1. **Transport 不知道 Agent 的存在**——它只搬运 (method, json) → (json, error)
2. **只有一套 handler**——serverapp 的 RPCTable，local 模式直接复用
3. **Backend 不变**——CLI 代码零改动，继续调 `backend.GetSettings()` 等类型安全方法
4. **新增 transport 只需实现 Call + Close**

## 现状分析

### 关键文件

| 文件 | 行数 | 当前职责 | 计划 |
|------|------|----------|------|
| `agent/transport.go` | 63 | Transport 接口定义 | **重写**：精简为 Call + Close |
| `agent/transport_channel.go` | 432 | channelTransport（local 模式）| **删除**：被新的 ChannelTransport 替代 |
| `agent/local_transport.go` | 1082 | localTransport（handler 表）| **删除**：handler 统一到 serverapp |
| `agent/transport_remote.go` | 669 | RemoteTransport（WS 客户端）| **精简**：剥离非 transport 逻辑 |
| `agent/backend.go` | 151 | Backend 接口 | **精简**：接口瘦身 |
| `agent/backend_impl.go` | 651 | Backend 实现 | **改动**：生命周期管理从 transport 移到 Backend |
| `agent/req_types.go` | 388 | 请求类型 + 方法常量 | 不动 |
| `serverapp/rpc.go` | 103 | RPC helper 函数 | **导出**：类型和函数大写导出 |
| `serverapp/rpc_table.go` | 1321 | 唯一的 handler 表 | **轻微改动**：导出类型 |
| `serverapp/server.go` | ~300 | server 启动 | 轻微改动 |
| `cmd/xbot-cli/main.go` | ~900 | CLI 入口 | **改动**：启动流程重组 |

### 依赖关系（当前）

```
cmd/xbot-cli/main.go
  → agent.NewChannelBackend(cfg)        // 创建 Backend
    → agent.New(config)                  // 创建 Agent
    → newLocalTransport(agent, bus)      // 创建 local handler 表
    → newChannelTransport(lt, disp, ch)  // 包裹 channel 逻辑
  → backend.Start()                      // 启动一切
    → channelTransport.Start()
      → agent.Run()                      // !! transport 启动 agent !!
      → eventLoop()                      // !! transport 处理 WS 事件 !!
```

### 依赖关系（目标）

```
cmd/xbot-cli/main.go
  → agent.New(config)                    // 创建 Agent
  → serverapp.BuildRPCTable(rpcCtx)      // 构建 handler 表
  → agent.NewChannelTransport(table.Dispatch) // 创建纯 transport
  → agent.NewBackend(transport)          // 创建 Backend
  // 独立启动：
  go agent.Run(ctx)                      // Agent 自己跑
  → backend.Start()                      // Backend 启动回调等
```

### 风险点

1. **Transport 接口精简后，Backend 的 Start/Stop/Run 语义变化**——当前 `Backend.Run()` 委托给 `transport.Run()`，后者调 `agent.Run()`。新架构下 Backend 需要自己管理 Agent 生命周期
2. **channelTransport 里的 WS eventLoop**——当前 channelTransport 处理 BubbleTea TUI 的 WS 事件。这些事件路由逻辑需要搬家
3. **SendMessage 和 BindChat**——当前通过 transport 传递，新架构下需要另一个通道
4. **WireCallbacks / SetTUIControlHandler / SetChatRenameFn**——当前在 transport 上注册回调。新架构下应该在 Backend 或独立的 EventBus 上注册

## 详细计划

### 阶段一：导出 Server 层 API

让 serverapp 的 RPCTable 可以被 agent 包外部使用。

- [ ] **1.1** 在 `serverapp/rpc.go` 中导出类型：
  - `rpcHandler` → `RPCHandler`
  - `rpcTable` → `RPCTable`  
  - `dispatch()` → `Dispatch()`
  - `withRPCCtx()` → `WithRPCCtx()`
  - 涉及文件：`serverapp/rpc.go`, `serverapp/rpc_table.go`, `serverapp/server.go`

- [ ] **1.2** 导出 `buildRPCTable` 为 `BuildRPCTable`，接受一个公开的 `RPCContext` struct（替代私有的 `rpcContext`）
  - 涉及文件：`serverapp/rpc_table.go`

- [ ] **1.3** 验证：`go build ./...` 通过

### 阶段二：精简 Transport 接口

把 Transport 从 15 个方法瘦身为纯传输协议。

- [ ] **2.1** 定义新的 Transport 接口（`agent/transport.go`）：
  ```go
  type Transport interface {
      Call(method string, payload json.RawMessage) (json.RawMessage, error)
      Close() error
  }
  ```

- [ ] **2.2** 新建 `agent/lifecycle.go`，将以下职责从 Transport 移出：
  - `AgentRunner` 接口：封装 `Agent.Run(ctx)` 的启动/停止
  - `EventRouter` 接口：封装 `SendMessage`、`BindChat`、WS event 路由
  - `CallbackRegistry` 接口：封装 `SetTUIControlHandler`、`WireCallbacks`、`SetChatRenameFn`
  - 涉及文件：新建 `agent/lifecycle.go`

- [ ] **2.3** 更新 Backend 接口（`agent/backend.go`）：
  - Backend 持有 `Transport` + `AgentRunner` + `EventRouter` + `CallbackRegistry`
  - `Start/Stop/Run` 改为管理这些组件，不再委托给 transport
  - `SendMessage/BindChat` 改为委托给 `EventRouter`
  - `WireCallbacks/SetTUIControlHandler` 改为委托给 `CallbackRegistry`
  - 涉及文件：`agent/backend.go`, `agent/backend_impl.go`

- [ ] **2.4** 验证：编译检查（此阶段会暂时 break，后续阶段修复）

### 阶段三：新建 ChannelTransport（纯传输）

替代 `channelTransport + localTransport`，只做进程内 RPC 转发。

- [ ] **3.1** 创建 `agent/transport_channel.go`（新文件，替换旧的）：
  ```go
  // ChannelTransport 是进程内直连 transport。
  // 它直接调用 RPCTable.Dispatch，无网络开销。
  type ChannelTransport struct {
      dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
  }
  
  func NewChannelTransport(dispatch func(context.Context, string, json.RawMessage) (json.RawMessage, error)) *ChannelTransport
  func (t *ChannelTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error)
  func (t *ChannelTransport) Close() error
  ```
  - 涉及文件：新建 `agent/transport_channel.go`

- [ ] **3.2** 删除旧 `agent/transport_channel.go`（432 行）
- [ ] **3.3** 删除 `agent/local_transport.go`（1082 行）
  - **净减 1514 行**

### 阶段四：适配 RemoteTransport

让现有的 WS transport 实现精简后的 Transport 接口。

- [ ] **4.1** 修改 `agent/transport_remote.go`：
  - 只保留 `Call()` + `Close()` 实现
  - `Start/Stop/Run/SendMessage/BindChat` 等方法移到 Backend 或单独的 `WSClient` struct
  - 涉及文件：`agent/transport_remote.go`

### 阶段五：重组 CLI 启动流程

让 CLI local 模式走 "创建 RPCTable → ChannelTransport → Backend"。

- [ ] **5.1** 重写 `NewChannelBackend`（在 `agent/backend_impl.go` 中）：
  ```go
  func NewLocalBackend(cfg LocalBackendConfig) (*Backend, error) {
      // 1. 创建 Agent
      agent := New(cfg.AgentConfig)
      // 2. 构建 RPCTable (由调用方传入，因为依赖 serverapp)
      transport := NewChannelTransport(cfg.RPCTableDispatch)
      // 3. 创建 Backend
      backend := &Backend{
          transport:     transport,
          agent:         agent,
          eventRouter:   cfg.EventRouter,
          callbackReg:   cfg.CallbackRegistry,
      }
      return backend, nil
  }
  ```
  - 涉及文件：`agent/backend_impl.go`

- [ ] **5.2** 修改 `cmd/xbot-cli/main.go`：
  - 创建 Agent
  - 创建 `serverapp.RPCContext`，构建 `serverapp.BuildRPCTable()`
  - 创建 `ChannelTransport(table.Dispatch)`
  - 创建 `LocalBackend(transport, agent, ...)`
  - 启动 `go agent.Run(ctx)` + `backend.Start()`
  - 涉及文件：`cmd/xbot-cli/main.go`

### 阶段六：清理和验证

- [ ] **6.1** 确认删除的文件不被任何代码引用
- [ ] **6.2** `go build ./...` 通过
- [ ] **6.3** `go test ./...` 通过
- [ ] **6.4** `golangci-lint run ./...` 通过
- [ ] **6.5** 手动测试：CLI local 模式启动正常，settings/model/subscription 操作正常

## 验证方案

| 验证项 | 方法 | 预期 |
|--------|------|------|
| 编译 | `go build ./...` | 零错误 |
| 单测 | `go test ./...` | 全部通过 |
| Lint | `golangci-lint run ./...` | 零告警 |
| CLI 启动 | `go run ./cmd/xbot-cli/` | 正常启动，可对话 |
| RPC 通道 | CLI 中执行 /settings, /model | 正常响应 |
| 代码量 | `wc -l agent/local_transport.go agent/transport_channel.go` | 两个文件不存在 |

## 代码量预估

| 操作 | 文件 | 行数变化 |
|------|------|----------|
| **删除** | `agent/local_transport.go` | -1082 |
| **删除** | `agent/transport_channel.go`（旧） | -432 |
| **新增** | `agent/transport_channel.go`（新） | +30 |
| **新增** | `agent/lifecycle.go` | +80 |
| **修改** | `agent/transport.go` | -40, +10 |
| **修改** | `agent/backend.go` | -30, +30 |
| **修改** | `agent/backend_impl.go` | -100, +80 |
| **修改** | `agent/transport_remote.go` | -200, +50 |
| **修改** | `serverapp/rpc.go` + `rpc_table.go` | +10 (导出) |
| **修改** | `cmd/xbot-cli/main.go` | -50, +60 |
| **净效果** | | **约 -1600 行** |

## 回滚策略

- 每个阶段独立 commit，可逐阶段 revert
- 阶段三（删文件）之前确保阶段一二已验证通过
- 保留 `git stash` 和 `git reflog` 作为安全网

## 注意事项

1. **BubbleTea TUI 事件循环**：当前 `channelTransport.eventLoop()` 处理 TUI 的 WS 消息。这些不是 RPC，需要搬到 `EventRouter` 或 `lifecycle.go`
2. **Backend 生命周期**：当前 `Backend.Run()` 阻塞等待 transport 结束。新架构下 Backend.Run() 应该等待 Agent.Run() 结束
3. **context 传播**：当前 localTransport 不用 context，但 RPCTable.Dispatch 需要 ctx。新 ChannelTransport.Call() 需要创建一个带默认 senderID 的 context
4. **并发安全**：Agent.Run() 和 Backend.Call() 在不同 goroutine 中运行，需要确保 Agent 的方法是并发安全的（目前已经是）
