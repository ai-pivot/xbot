# 计划：统一本地/远程模式 — Agent 始终通过 Transport 通信

> 生成时间：2026-05-13
> 状态：待确认

## 背景与目标

### 问题
当前本地模式下 Agent 直接运行在 CLI 进程中，CLI 代码通过 `backend.Agent()` 直接操作 Agent 对象（WireCallbacks、LLMFactory、SettingsService 等）。远程模式走 WS RPC。这导致：
1. 每个 Agent 回调/注入需要在 CLI `main.go` 和 server `server.go` **两处手动同步**
2. 容易遗漏 server 端注入（sessionStateHandler bug 就是例子）
3. `IsRemote()` 分支散布在 7+ 处
4. 两套 RPC handler（local_transport.go 900 行 + rpc_table.go）维护成本高

### 目标
**本地模式也走 Server**。CLI 进程启动时自动拉起 local server（进程内），所有通信通过 Transport。Agent 永远不暴露给 CLI 代码。

```
现在：CLI ──[直接引用]──→ Agent（本地）
      CLI ──[WS Transport]──→ Server → Agent（远程）

目标：CLI ──[Transport]──→ Server → Agent（统一）
       ↑ local 模式: Server 是进程内的（in-process）
       ↑ remote 模式: Server 是远程的
```

## 核心设计决策

### 1. 本地模式 = 进程内 Server + Channel Transport

本地模式下，`main.go` 不再直接创建 Agent。而是：
1. 创建 `serverapp.Server` 配置（同远程 server）
2. 在进程内启动 server（不监听 TCP，使用 channel-based transport）
3. CLI 通过 channel transport 连接进程内 server
4. 所有通信走 Transport RPC + Subscribe 事件

**Channel Transport**：
- `InboundMessage` → Go channel → server 的 `handleMessage`
- RPC response → Go channel → CLI 的 `Call` 返回
- 事件 emit → Go channel → CLI 的 Subscribe handler
- 零网络开销，零序列化（或 JSON 序列化保持一致性，后续优化可去掉）

### 2. 消除所有 `backend.Agent()` 调用

CLI 代码不再调用 `backend.Agent()`。所有 Agent 操作通过：
- Backend RPC 方法（已有 45+ 个）
- Subscribe 事件（progress、outbound、session、ask_user 等）
- 新增缺失的 RPC 方法（plugin、approval 等）

### 3. 消除 `Backend.agent` 和 `Backend.bus` 字段

Backend 变成纯 Transport 客户端：
```go
type Backend struct {
    transport Transport  // 唯一字段
}
```

### 4. 消除 `local_transport.go`

local_transport 的 900 行 handler 代码全部删除。统一由 serverapp/rpc_table 处理。

## 现状分析

### 关键文件
| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `agent/backend_impl.go` | Backend 结构体 | **重构**：删除 agent/bus 字段，所有方法走 RPC |
| `agent/transport.go` | Transport 接口 | **修改**：可能新增 channel transport |
| `agent/local_transport.go` | 本地 handler 表 (900行) | **删除** |
| `cmd/xbot-cli/main.go` | CLI 入口 | **重构**：去掉直接 Agent 操作 |
| `serverapp/server.go` | Server 入口 | **修改**：支持进程内启动模式 |
| `agent/wire_callbacks.go` | Agent 回调注入 | **删除**（server 端唯一注入点） |

### 需要新增 RPC 的功能
| 功能 | 当前路径 | 需要的 RPC |
|------|---------|-----------|
| Plugin 管理 | 直接操作 PluginManager | `plugin_status`, `plugin_install` 等（rpc_table 已有） |
| Approval 审批 | 直接注入 ApprovalState | 需要新增 Subscribe 事件 |
| Checkpoint 状态 | 直接操作本地文件 | 需要评估 |
| LLM 重建 | 直接调用 LLMFactory | 已有 SetDefaultSubscription 等 RPC |

### 风险点
1. **性能**：本地模式多了一层 channel/序列化开销（可忽略）
2. **Startup 时间**：进程内 server 启动可能增加延迟
3. **本地文件操作**：session JSON、todo 文件等需要 server 进程能访问 CLI 工作目录
4. **Runner Bridge**：本地 runner LLM 注入需要重新设计

## 详细计划

### 阶段一：创建 Channel Transport
- [ ] 新增 `agent/transport_channel.go`：进程内 channel-based transport
  - `Call()` → Go channel → server handler → Go channel 返回
  - `SendMessage()` → Go channel
  - `Subscribe()` → 本地事件分发（复用 baseTransport）
  - `IsRemote()` → false
- [ ] 支持两种序列化模式：JSON（保持与远程一致）和 native（优化）

### 阶段二：Server 进程内启动
- [ ] 重构 `serverapp/server.go`：抽取 `NewServer(cfg) (*Server, error)` + `Serve()` + `ServeInProcess(transport Transport)`
- [ ] 新增 `ServeInProcess()` 方法：不监听 TCP，接受 channel transport 连接
- [ ] CLI `main.go` 本地模式：创建 Server 配置 → `ServeInProcess()` → 用 channel transport 连接

### 阶段三：消除 CLI 中的直接 Agent 操作
- [ ] 删除 `WireCallbacks` 调用（main.go:1828-1846）
- [ ] 删除所有 `backend.Agent()` 调用
- [ ] 删除所有 `backend.LLMFactory()` 调用，替换为 RPC
- [ ] 删除所有 `backend.SettingsService()` 调用，替换为 RPC
- [ ] 统一本地/远程的 Subscribe 事件注册（不再有 `if !IsRemote()` 分支）
- [ ] 统一服务注入（BgTaskManager、ApprovalState、PluginManager 全走 RPC/Subscribe）

### 阶段四：Backend 瘦身
- [ ] `Backend.agent` 和 `Backend.bus` 字段 → 删除
- [ ] 所有"本地访问器"和"本地设置器" → 转为 RPC 方法或删除
- [ ] `NewBackend(cfg)` 构造函数 → 简化为只接受 Transport
- [ ] 删除 `agent/local_transport.go`（900行）

### 阶段五：清理
- [ ] 删除 `IsRemote()` 分支（所有 7 处）
- [ ] 删除 `wire_callbacks.go`
- [ ] 删除 CLI main.go 中所有本地模式专用代码块
- [ ] 更新文档

## 验证方案

1. `go build ./...` — 编译通过
2. `go test ./...` — 全部 PASS
3. 本地模式启动 xbot-cli → 功能正常
4. 远程模式连接 server → 功能正常
5. 对比两种模式的行为一致性

## 注意事项

- 这是一个大规模重构，建议分阶段 commit，每个阶段独立可验证
- 优先保持功能不变，性能优化后续再做
- 进程内 server 需要共享 CLI 的工作目录（用于 session JSON、文件操作等）
