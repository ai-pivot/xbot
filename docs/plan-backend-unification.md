# 计划：Backend 统一重构 v6 — 泛型 Dispatch 模式

> 生成时间：2026-05-04 23:20 CST
> 状态：待确认

## 核心洞察

之前所有版本都在两难：
- Transport 方法多 → 重但 Backend 零分支
- Transport 方法少 → 轻但 Backend 有分支

**解法**：Backend 方法用泛型 `dispatch[Req, Res]()` 封装分支逻辑。Backend 方法本身零分支，Transport 只有 ~10 个纯通信方法。

```
Backend.GetSettings(ns, sid):
  dispatch(b, "get_settings", 
    getSettingsReq{ns, sid},                           ← 强类型请求
    func(a *Agent, r) { a.Settings.GetSettings(...) }) ← 本地 handler

dispatch 内部（仅此一处）:
  if b.agent != nil → 调 localHandler(agent, req)       ← 零 JSON
  else → json.Marshal(req) → transport.Call(method)     ← JSON → 网络
```

## 架构

```
┌──────────────────────────────────────────────────────┐
│ Backend (统一实现，持有所有业务方法)                    │
│                                                      │
│ agent *Agent        ← 本地时非 nil                     │
│ transport Transport ← 远程时非 nil                     │
│                                                      │
│ func (b) GetSettings(ns,sid) (...) {                 │
│   return dispatch(b, "get_settings",                 │
│     getSettingsReq{ns,sid},                          │
│     func(a,r) { a.Settings().GetSettings(...) })     │
│ }                                                    │
│                                                      │
│ ⚠️ Backend 每个方法只有 3-4 行声明，无分支              │
│ ⚠️ 所有分支逻辑在 dispatch() 泛型函数中（仅一处）        │
└──────────┬───────────────────────────────────────────┘
           │ transport (nil for local)
┌──────────▼───────────────────────────────────────────┐
│ Transport interface (纯通信，~10 方法)                  │
│                                                      │
│ Call(method, payload) → (response, error)            │
│ Start/Stop/Close                                     │
│ SendMessage / Subscribe                              │
│ OnOutbound / OnProgress / ...                        │
│ ConnState / IsRemote / ServerURL                     │
└──────────┬───────────────────────────────────────────┘
           │ implements
    ┌──────┴──────┬──────────┐
    ▼             ▼          ▼
RemoteTransport  GRPC       MCP
(WebSocket)     Transport  Transport (未来)
```

**加新 transport**：实现 ~10 个方法（不是 85 个）。

## 关键代码

### dispatch 泛型函数（仅此一处有分支）

```go
// dispatch 是 Backend 方法统一入口。封装了本地/远程分发逻辑。
func dispatch[Req, Res any](
    b *Backend,
    method string,
    req Req,
    local func(*Agent, Req) (Res, error),
) (Res, error) {
    if b.agent != nil {
        return local(b.agent, req)           // 本地：直接调，零序列化
    }
    // 远程：序列化 → transport.Call → 反序列化
    payload, err := json.Marshal(req)
    if err != nil {
        var zero Res
        return zero, fmt.Errorf("%s: marshal: %w", method, err)
    }
    raw, err := b.transport.Call(method, payload)
    if err != nil {
        var zero Res
        return zero, err
    }
    if len(raw) == 0 || string(raw) == "null" {
        var zero Res
        return zero, nil
    }
    var result Res
    if err := json.Unmarshal(raw, &result); err != nil {
        var zero Res
        return zero, fmt.Errorf("%s: unmarshal: %w", method, err)
    }
    return result, nil
}

func dispatchVoid[Req any](b *Backend, method string, req Req, local func(*Agent, Req) error) {
    if b.agent != nil {
        _ = local(b.agent, req)
        return
    }
    payload, _ := json.Marshal(req)
    if _, err := b.transport.Call(method, payload); err != nil {
        log.WithError(err).WithField("method", method).Warn("Backend: remote call failed")
    }
}
```

### Backend 方法（每方法 3-4 行，零分支）

```go
// 有返回值
func (b *Backend) GetSettings(namespace, senderID string) (map[string]string, error) {
    return dispatch(b, "get_settings",
        getSettingsReq{Namespace: namespace, SenderID: senderID},
        func(a *Agent, r getSettingsReq) (map[string]string, error) {
            return a.SettingsService().GetSettings(r.Namespace, r.SenderID)
        })
}

// 无返回值
func (b *Backend) SetMaxIterations(n int) {
    dispatchVoid(b, "set_max_iterations", n,
        func(a *Agent, v int) error { a.SetMaxIterations(v); return nil })
}

// 返回 Go 对象（远程返回 nil）
func (b *Backend) LLMFactory() *LLMFactory {
    if b.agent == nil { return nil }
    return b.agent.LLMFactory()
}

// 本地专有方法（无远程对应）
func (b *Backend) Start(ctx context.Context) error {
    go b.agent.Run(ctx)
    return nil
}
func (b *Backend) SendInbound(msg bus.InboundMessage) error {
    select { case b.bus.Inbound <- msg: return nil
    default: return fmt.Errorf("inbound full") }
}
```

### Transport 接口（~10 方法）

```go
type Transport interface {
    Start(ctx context.Context) error
    Stop()
    Close() error

    // Call 发送请求并等待响应。method 是 RPC 方法名。
    Call(method string, payload json.RawMessage) (json.RawMessage, error)

    // SendMessage 发送用户消息到 agent。
    SendMessage(msg Message) error

    // Subscribe 通知 transport 该 chatID 的推送事件应路由到此连接。
    Subscribe(chatID string) error

    // 服务端推送事件回调
    OnOutbound(cb func(bus.OutboundMessage))
    OnProgress(cb func(*channel.CLIProgressPayload))
    OnInjectUserMessage(cb func(content string))
    OnReconnect(cb func())
    OnConnStateChange(cb func(state string))
    OnPluginWidgets(cb func(zones map[string]string, chatID string))

    // 状态
    ConnState() string
    IsRemote() bool
    ServerURL() string
}
```

### RemoteTransport（~650 行，纯通信）

```go
type RemoteTransport struct {
    serverURL string
    token     string
    conn      *websocket.Conn
    // ... WS 管理字段
}

func (t *RemoteTransport) Call(method string, payload json.RawMessage) (json.RawMessage, error) {
    // 发送 wsOutgoingMessage{Type:"rpc", Method:method, Params:payload}
    // 等待 pending[id] channel 或超时
}
```

RemoteTransport 不实现任何业务方法。只有 WS 连接管理 + `Call()`。

## 方法分类

| 类别 | 数量 | 实现方式 |
|------|------|----------|
| 泛型 dispatch | ~40 | `dispatch[Req, Res](b, method, req, localFn)` |
| 泛型 dispatchVoid | ~20 | `dispatchVoid[Req](b, method, req, localFn)` |
| 返回 Go 对象 | ~10 | `if b.agent == nil { nil } else { agent.XXX() }` |
| 本地专有 | ~10 | 直接访问 agent/bus（Start, Stop, SendInbound, Bus, Agent…） |
| Backend override | 2 | SetChannelReconfigureFn, SetChannelConfig |

## 请求类型（编译期类型安全）

```go
type getSettingsReq struct {
    Namespace string `json:"namespace"`
    SenderID  string `json:"sender_id"`
}
type setSettingReq struct {
    Namespace string `json:"namespace"`
    SenderID  string `json:"sender_id"`
    Key       string `json:"key"`
    Value     string `json:"value"`
}
// ... ~60 个请求类型
```

## 文件变化

| 文件 | 行数 | 操作 |
|------|------|------|
| `agent/transport.go` | ~60 | 🆕 Transport 接口 + Message 类型 |
| `agent/transport_remote.go` | ~650 | 🆕（从 backend_remote 搬移纯 WS 代码） |
| `agent/backend_impl.go` | ~600 | 🆕 Backend + dispatch 泛型 + ~70 个方法 |
| `agent/req_types.go` | ~150 | 🆕 请求参数类型定义 |
| `agent/backend.go` | 318→340 | ✏️ 扩展接口 |
| `agent/backend_local.go` | 836 | ❌ |
| `agent/backend_remote.go` | 1472 | ❌ |
| `cmd/xbot-cli/main.go` | ~2000 | ✏️ 消除断言 |
| `serverapp/server.go` | ~800 | ✏️ 1 行 |

**净减少**：~2300 删除，~1460 新增 = **净减 ~840 行**。

## Transport 扩展性对比

| | v3/v5 (85 方法 Transport) | v6 (10 方法 Transport) |
|---|---|---|
| 加 WebSocket transport | 实现 85 个方法 | 实现 **10** 个方法 |
| 加 gRPC transport | 实现 85 个方法 | 实现 **10** 个方法 |
| 加 MCP transport | 实现 85 个方法 | 实现 **10** 个方法 |
| 业务方法在哪 | Transport 实现 | Backend（只需写一次） |

## 详细计划

### 阶段一：Transport 接口 + Message 类型
- [ ] 创建 `agent/transport.go`

### 阶段二：请求参数类型
- [ ] 创建 `agent/req_types.go`（~60 个 struct）

### 阶段三：RemoteTransport（纯 WS 代码搬移）
- [ ] 创建 `agent/transport_remote.go`
- [ ] 从 `backend_remote.go` 搬移：connect, readPump, pingLoop, reconnectLoop, Call (原 callRPC), handleRPCResponse, dispatchProgress, convertWsProgressToCLI, 回调管理
- [ ] **不需要搬移**：~60 个业务方法

### 阶段四：Backend + dispatch 泛型
- [ ] 创建 `agent/backend_impl.go`
- [ ] `dispatch[Req, Res]()` + `dispatchVoid[Req]()` 泛型函数
- [ ] ~70 个 Backend 方法（每方法 3-4 行）
- [ ] 返回 Go 对象、本地专有、override 方法

### 阶段五：更新接口 + 消除断言
- [ ] `agent/backend.go`：扩展接口
- [ ] `cmd/xbot-cli/main.go`：消除 6 处断言
- [ ] `serverapp/server.go`：1 行改动

### 阶段六：Mock/Test + 清理
- [ ] 更新 fake 实现，删除旧文件，更新 AGENT.md

## 验证方案

1. `go build ./...` — 零错误
2. `go test ./agent/... ./cmd/... ./serverapp/...`
3. `golangci-lint run ./...`
4. 本地/远程 TUI 手动验证

---

✅ 自审通过
