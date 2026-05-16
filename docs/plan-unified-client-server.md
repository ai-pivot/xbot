# 计划：统一 Client-Server 架构

> 生成时间：2026-05-15
> 状态：待确认

## 核心目标

**本地模式也起 Server，完全统一为 Client → Transport → Server 架构。**

CLI 永远只接触 Client，Client 通过 Transport 和 Server 通信，Server 持有 Agent。
无论本地还是远程，代码路径完全一致。

## 现状问题

```
当前本地模式调用链（荒谬的 6 层绕路）:
CLI → Backend.callVoid("set_max_iterations")
    → ChannelTransport.Call("set_max_iterations")
        → RPCTable.Dispatch("set_max_iterations")
            → DirectBackend.SetMaxIterations()
                → Agent.SetMaxIterations()

当前本地模式有 3 个"Backend":
1. Backend (backend_impl.go) — RPC 客户端伪装层
2. DirectBackend (direct_backend.go) — 服务端 handler 用的
3. LocalLifecycle (lifecycle.go) — 生命周期管理

实际上本地和远程应该走完全相同的代码路径:
Client → Transport.Call() → Server (RPCTable) → Agent
```

## 新架构

```
┌─────────────────────────────────────────────────────┐
│ Client (agent/client.go)                            │
│  CLI 唯一接触的对象。只持有 Transport。               │
│  所有方法 = Transport.Call("method", params)         │
│  事件订阅 = Transport 的事件推送                      │
└───────────────┬─────────────────────────────────────┘
                │ Transport (Call + Close + 事件推送)
                │
┌───────────────┴─────────────────────────────────────┐
│ Server (serverapp/server_core.go)                   │
│  持有 Agent + RPCTable + Dispatcher + Bus            │
│  接收 Transport 请求 → RPCTable.Dispatch → Agent     │
│                                                      │
│  本地模式: InProcessServer (不监听 HTTP/WS)           │
│  远程模式: WSServer (监听 HTTP + WebSocket)           │
└──────────────────────────────────────────────────────┘
```

### Transport 接口（精简）

```go
type Transport interface {
    Call(method string, payload json.RawMessage) (json.RawMessage, error)
    Close() error
}
```

Transport 实现两种：
- `InProcessTransport` — 直接调 `RPCTable.Dispatch`（零开销）
- `RemoteTransport` — WebSocket RPC（已有，基本不变）

### 事件推送（不通过 Call）

事件是 Server → Client 的单向推送。通过 eventCh channel：
- 本地模式：Server 直接写入 eventCh（同进程）
- 远程模式：WS readPump 写入 eventCh（已有）

这个 eventCh 挂在 Transport 上，Client 构造时传入。

### 生命周期/回调 — 全部在 Server 端

WireCallbacks、SetChatRenameFn 等**不再从 Client 注册**。
Server 创建时自己完成所有回调注入（用自己内部的 disp/bus/agent 组件）。

TUI 控制请求走 RPC：`tui_control_request` → RPCTable handler → 调用 Server 端注册的回调 → 返回结果。

## 关键文件变更

| 文件 | 操作 | 说明 |
|------|------|------|
| `serverapp/server_core.go` | **新建** | Server Core：Agent + RPCTable + Disp + Bus + WireCallbacks。本地和远程共用 |
| `agent/client.go` | **新建** | Client：纯 RPC 客户端，所有方法 = Transport.Call() |
| `agent/transport.go` | **不变** | Transport 接口已精简为 Call+Close |
| `agent/transport_channel.go` | **重写** | InProcessTransport：直接调 RPCTable.Dispatch |
| `agent/transport_remote.go` | **保留** | RemoteTransport 基本不变，已经实现了 Call+Close |
| `agent/backend.go` | **删除** | AgentBackend/RPCHandlerBackend 接口 → Client 接口替代 |
| `agent/backend_impl.go` | **删除** | Backend 实现 → Client 替代 |
| `agent/backend_config.go` | **删除** | 不再需要 |
| `agent/direct_backend.go` | **删除** | RPCTable handler 直接用 `h.Ag` |
| `agent/lifecycle.go` | **删除** | 生命周期由 Server 管理 |
| `agent/base_transport.go` | **删除** | 事件订阅移到 Client 端 |
| `serverapp/server.go` | **重写** | WSServer = ServerCore + HTTP/WS 监听 |
| `serverapp/rpc_table.go` | **精简** | 删除 `h.Backend` 字段（改用 `h.Ag`），删除 RPCHandlerBackend 依赖 |
| `serverapp/rpc.go` | **精简** | 删除 RPCHandlerBackend 引用 |
| `cmd/xbot-cli/main.go` | **重写** | newCLIApp: 创建 ServerCore + InProcessTransport + Client |
| `agent/setting_runtime.go` | **修改** | `backend RPCHandlerBackend` → `ag *Agent`（handler 直接操作 Agent） |

## 详细计划

### 阶段一：创建 ServerCore（serverapp/server_core.go）

从 server.go 中提取核心逻辑：

```go
// ServerCore 是不包含网络监听的 Server 核心。
// 本地模式和远程模式共用。
type ServerCore struct {
    Agent   *agent.Agent
    Config  *config.Config
    Disp    *channel.Dispatcher
    MsgBus  *bus.MessageBus
    RPCTable RPCTable
}

func NewServerCore(opts ServerCoreOpts) (*ServerCore, error) {
    // 1. 加载配置
    // 2. 创建 LLM 客户端
    // 3. 创建 Agent
    // 4. 创建 Dispatcher + Bus
    // 5. 创建 DirectBackend (nil — 不再需要)
    // 6. 构建 RPCTable
    // 7. WireCallbacks（用内部的 disp/bus）
    // 8. 注册核心工具
    // 9. 配置 LLM tiers/contexts 等
    // 10. 注册 Channels（Feishu/Web/QQ — 本地模式可选）
    return core, nil
}

func (s *ServerCore) HandleRPC(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
    return s.RPCTable.Dispatch(ctx, method, params)
}
```

涉及文件：
- **新建** `serverapp/server_core.go` (~200 行)
- 从 `serverapp/server.go` 的 Run() 中提取步骤 1-9

### 阶段二：创建 Client（agent/client.go）

Client 是 CLI 唯一接触的对象，替代当前的 Backend：

```go
type Client struct {
    transport Transport
    eventCh   chan protocol.WSMessage  // Server → Client 事件推送
}

// 所有 RPC 方法 = transport.Call()
func (c *Client) ListModels() []string { ... }
func (c *Client) SetMaxIterations(n int) { ... }
// ... ~50 个方法
```

涉及文件：
- **新建** `agent/client.go` (~500 行，从 backend_impl.go 迁移 RPC 方法)

### 阶段三：重写 InProcessTransport（agent/transport_channel.go）

```go
type InProcessTransport struct {
    dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
    eventCh  chan protocol.WSMessage
}

func NewInProcessTransport(core *serverapp.ServerCore, eventCh chan protocol.WSMessage) *InProcessTransport {
    return &InProcessTransport{
        dispatch: core.HandleRPC,
        eventCh:  eventCh,
    }
}
```

### 阶段四：重写 CLI main.go

```go
func newCLIApp(cfg *config.Config) {
    if remoteMode {
        // 远程模式
        transport = NewRemoteTransport(serverURL, token, eventCh)
        client = NewClient(transport, eventCh)
    } else {
        // 本地模式 — 起 ServerCore
        core, err := serverapp.NewServerCore(opts)
        transport = agent.NewInProcessTransport(core, eventCh)
        client = agent.NewClient(transport, eventCh)
    }
    // 之后所有代码只用 client，完全统一
}
```

### 阶段五：精简 RPCTable

- 删除 `h.Backend RPCHandlerBackend` 字段
- 所有 `h.Backend.XXX()` 调用改为 `h.Ag.XXX()`
- `setting_runtime.go` 的 `backend RPCHandlerBackend` 参数改为 `ag *Agent`

### 阶段六：删除旧文件

- `agent/backend.go` (接口定义 → Client 替代)
- `agent/backend_impl.go` (实现 → Client 替代)
- `agent/backend_config.go`
- `agent/direct_backend.go`
- `agent/lifecycle.go`
- `agent/base_transport.go`
- `agent/req_types.go` 中的旧类型清理

### 阶段七：RemoteTransport 精简

RemoteTransport 只需实现 Transport（Call+Close）+ 事件推送。
删除 AgentRunner/EventRouter/CallbackRegistry 的实现。
生命周期管理移到 Client 层。

## 验证方案

- `go build ./...` — 编译通过
- `go test ./...` — 所有测试通过
- 本地模式启动测试：`./xbot-cli` 能正常启动 TUI 并交互
- 代码行数对比：预期删除 ~1500+ 行

## 风险与注意事项

1. **TUI 回调注册时机**：当前 TUI 回调在 CLI main.go 中注册到 Agent。新架构需要通过 RPC `set_tui_control_handler` 注册，或者在 ServerCore 创建时注入一个通用的回调管道
2. **eventCh 容量**：本地模式 eventCh 可能需要更大容量（没有网络延迟）
3. **循环依赖**：agent 包不能 import serverapp。InProcessTransport 需要接收 `func dispatch` 而不是 `*ServerCore`
4. **测试文件**：所有使用 `Backend` 的测试需要迁移到 `Client`
5. **Channel 注册**：本地模式不需要 Feishu/Web/QQ channel，但 Dispatcher 和 AgentChannel 仍需要

## 预期效果

```
删除文件（6 个）:
  agent/backend.go          (~178 行)
  agent/backend_impl.go     (~753 行)
  agent/backend_config.go   (~87 行)
  agent/direct_backend.go   (~235 行)
  agent/lifecycle.go        (~257 行)
  agent/base_transport.go   (~80 行)
  合计删除: ~1590 行

新建文件（2 个）:
  serverapp/server_core.go  (~200 行)
  agent/client.go           (~500 行)
  合计新增: ~700 行

净减: ~890 行
架构复杂度: 从 6 层绕路 → 3 层直通
```
