# 计划：gRPC Transport for Plugin Channels

> 生成时间：2026-05-29
> 状态：已实现 ✅

## 背景与目标

当前 Channel Plugin 使用有限的 channel_start/channel_send/channel_poll 协议，
只支持收发纯文本，没有 streaming、reasoning、progress 事件。

**目标**: 实现 gRPC Transport（与 WebSocket 同级），让 plugin 进程通过 stdio 连接到 xbot 的完整 RPC 服务。
Plugin 成为全功能客户端，获得与远程 CLI 完全相同的能力。

## 架构设计

```
┌─────────────────────────────────────────────────────────────┐
│ xbot (Server)                                                │
│                                                               │
│  ServerCore.Agent ──→ RPCTable ──→ handlers                   │
│        ↑                                    │                  │
│  GrpcPluginConn.Call() ────────────────────┘                  │
│        │                                                      │
│  GrpcPluginConn.pushEvent() ──→ plugin stdin                  │
│                                                               │
│  plugin stdout ──→ readLoop ──→ RPCTable.Dispatch()            │
└─────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────┐
│ Plugin (Client)                                               │
│                                                               │
│  stdin ← WSMessage (events: progress, reply, reasoning...)    │
│  stdout → RPC request (method + params)                       │
│                                                               │
│  Plugin calls: send_inbound, get_history, set_settings...     │
│  Plugin receives: agent reply (streaming), progress, tools    │
└─────────────────────────────────────────────────────────────┘
```

**协议格式**: 与 WS 完全相同的 JSON-RPC

- Plugin → xbot (RPC request): `{"id":"1","method":"send_inbound","params":{...}}`
- Plugin → xbot (RPC response): `{"id":"1","result":{...}}` (for server→client calls)
- xbot → Plugin (event push): `{"type":"progress","progress":{...}}`
- xbot → Plugin (RPC request): `{"id":"2","method":"channel_send","params":{...}}`
- xbot → Plugin (RPC response): `{"id":"2","result":"ok"}` (for plugin's requests)

## 详细计划

### 阶段一：新建 agent/transport_grpc.go — GrpcPluginTransport

Transport 实现，包装 stdin/stdout 为双向 JSON-RPC 通道。

```go
type GrpcPluginTransport struct {
    process *exec.Cmd
    stdin   *jsonLineWriter
    stdout  *jsonLineReader
    
    // RPC dispatch (same as ChannelTransport)
    dispatch func(ctx context.Context, method string, payload json.RawMessage) (json.RawMessage, error)
    
    // Event push to plugin
    eventCh chan protocol.WSMessage
}
```

实现：
- `Call(method, payload)` → 写 JSON-RPC 到 plugin stdin → 等待 stdout response
- `Close()` → kill process
- `PushEvent(msg)` → 写 WSMessage JSON 到 plugin stdin
- `Run(ctx)` → readLoop 从 plugin stdout 读取 RPC request → dispatch

### 阶段二：修改 serverapp — 注册 gRPC transport 连接点

在 `server.go` 的 `registerChannels` 中：
1. 遍历 ChannelProviderRegistry
2. 对每个 gRPC plugin provider，spawn plugin process
3. 创建 GrpcPluginTransport
4. 注册为 Channel（与 feishu/qq/web 同级）
5. 订阅 bus.Outbound 事件推送到 plugin

### 阶段三：清理旧代码

删除：
- `plugin/runtime.go` 中的 channel-specific protocol types (channelProviderDecl, PluginInbound 等)
- `serverapp/channel_bridge_grpc.go` 整个文件
- `plugin/channel_provider.go` 中的 GrpcChannelBridgeFactory
- `channel/provider.go` 中的 ChannelHistoryProvider/ChannelUpdateProvider/ChannelMediaProvider (这些通过 RPC 实现)

保留：
- `channel/provider.go` 中的 ChannelProvider 接口（简化：只需 Name/CreateChannel/ConfigSchema/IsEnabled）
- Channel 和 Dispatcher 架构不变
- Plugin 发现和激活机制不变

### 阶段四：更新 xbot-ch-example

Plugin 从"channel 实现"变为"RPC 客户端"：
- 通过 stdin 接收 WSMessage 事件
- 通过 stdout 发送 RPC 请求
- 收到 HTTP 消息 → 调用 send_inbound RPC
- 收到 progress 事件 → 转发给 HTTP 客户端
- 收到 agent reply → 转发给 HTTP 客户端

## 验证方案

1. `go build ./...` 编译通过
2. `go test ./...` 测试通过
3. 重启 xbot，echo channel 在 TUI 中可见
4. `curl -X POST http://localhost:9876/message -d 'hello'` → 收到完整 streaming reply

## 风险点

1. stdin/stdout 双向同时读写需要正确处理（readLoop + writeLock）
2. plugin 进程退出时需要清理 transport
3. 事件推送频率可能很高，需要考虑背压

## 注意事项

- 与 WS 协议完全复用 protocol.WSMessage 格式
- Plugin 可以选择忽略不关心的事件类型
- channel_send 走标准 RPC，不再是特殊方法
