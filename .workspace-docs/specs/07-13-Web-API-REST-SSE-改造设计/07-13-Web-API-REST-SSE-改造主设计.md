---
type: Design Spec
title: Web API REST + SSE 改造主设计
description: 将 Web 前端从 WS 迁移到 REST(POST) + SSE，CLI 保留 WS，SSE 作为 Hub 的另一种 Client
tags:
  - spec
  - architecture
  - web
  - rest
  - sse
status: draft
repos:
  xbot: 3def45b807e4ed93c9df5b33479373f5e75a8c81
---

# Web API REST + SSE 改造主设计

## 概述

将 xbot Web 前端从 WebSocket 双向通讯迁移到 REST API（POST）+ SSE（Server-Sent Events）单向推送。CLI 远程模式继续使用 WS，不受影响。

### 目标

- Web 前端不再连接 `/ws`，所有请求走 REST POST，所有推送走 SSE
- 保留 seq 机制，支持 `Last-Event-ID` 重连重放
- 前端三层缓存优化弱网体验
- 删除插件市场功能（端点 + 前端实现）
- `web.go` 按职责拆分为多文件，提升可维护性
- 所有 REST 端点统一 POST 方法
- HTTP/2 优先，兼容 HTTP/1.1

### 非目标

- 不修改 CLI 远程模式的 WS 通讯（`transport_remote.go`、`web_remote_cli.go`、`web_hub.go`、`web_eventstream.go` 核心逻辑）
- 不修改 Agent 引擎的 progress 推送逻辑
- 不修改 RPCTable 的 81 个 handler 实现

## 关键决策

| 决策 | 选择 | 理由 |
|------|------|------|
| 改造范围 | 仅 Web 前端，CLI 保留 WS | 用户明确要求 |
| SSE 承载内容 | 所有 server→Web 推送 | KISS，一个通道管所有推送 |
| SSE 连接模型 | 每会话一条 SSE | 隔离清晰，前端缓存配合 |
| 传输层架构 | SSE 作为 Hub 的另一种 Client | 引擎零改动，Hub 不区分客户端类型 |
| REST 方法 | 统一 POST | 用户要求 |
| HTTP/2 | 优先 h2，兼容 h1.1 | 享受多路复用，不破坏非 TLS 环境 |
| SSE 鉴权 | 标准 EventSource（Cookie） | 原生自动重连 + Last-Event-ID |
| 迁移策略 | 一次性切换（Big Bang） | 不关心中途 Web 是否可用 |
| 插件市场 | 删除端点 + 前端实现 | 用户要求 |

## 整体架构

```
                    ┌─────────────────────────────────────────┐
                    │              xbot Server                 │
                    │                                          │
  Web Browser       │  POST /api/message     ──┐               │
  (Cookie auth)     │  POST /api/cancel       ├──→ RPCTable    │
       │            │  POST /api/ask_user/resp │   .Dispatch   │
       │            │  POST /api/rpc        ──┘               │
       │            │  POST /api/... (语义端点)                 │
       │            │                                          │
       ├──REST─────►│  Hub (不变)                               │
       │            │    ├─ WS Client ──→ writePump ──→ WS 帧  │──WS──► CLI 远程模式
       ├──SSE──────►│    │   (CLI, 不动)                       │
       │  GET /api/ │    └─ SSE Client ──→ writeLoop ──→ SSE  │
       │  sse?      │                  (新增, 复用 sendCh)     │
       │  chat_id=  │                                          │
                    │  eventStream (共享 seq + 缓冲)            │
                    │  WebChannel.Send* (零改动)               │
                    └─────────────────────────────────────────┘
```

核心设计：Hub 不区分客户端类型。SSE handler 注册为 Hub 的一个 Client，和 WS client 一样从 `sendCh` 读取 `WSMessage`，只是写出格式不同（WS 帧 → SSE 事件）。引擎代码零改动。

## 共享契约

### Hub Client 结构体扩展

现有 Hub `Client` 结构体需要扩展以支持 SSE 客户端：

- `sendCh chan WSMessage` — 消息通道（SSE 和 WS 共用）
- `chatID string` — 订阅的会话
- `userID string` — 用户身份
- `connType "ws" | "sse"` — 客户端类型（新增字段）
- `wsConn *websocket.Conn` — WS 连接（仅 ws 类型使用）
- `w http.ResponseWriter` — HTTP 响应（仅 sse 类型使用）
- `flusher http.Flusher` — SSE flush 用

SSE client 的 writeLoop 从 `sendCh` 读取 `WSMessage`，转换为 SSE 事件格式写出。WS client 的 `writePump` 逻辑不变。

### SSE 事件格式

每种 `WSMessage.Type` 映射为一种 SSE event type，`WSMessage` 的 JSON 作为 data：

```
id: 42
event: progress_structured
data: {"type":"progress_structured","seq":42,"chat_id":"/home/user","progress":{...}}

id: 43
event: stream_content
data: {"type":"stream_content","seq":43,"content":"Hello","chat_id":"/home/user"}
```

- `id:` 字段 = seq，浏览器自动记入 `Last-Event-ID`
- `event:` 字段 = 原 WS 消息类型
- `data:` = 完整 `WSMessage` JSON

### 消息类型映射

| SSE event | 原 WS type | 说明 |
|-----------|-----------|------|
| `text` | `MsgTypeText` | 最终回复 |
| `progress_structured` | `MsgTypeProgress` | 工具进度 |
| `stream_content` | `MsgTypeStreamContent` | 流式 LLM |
| `ask_user` | `MsgTypeAskUser` | 交互提问 |
| `card` | `MsgTypeCard` | 飞书卡片 |
| `user_echo` | `MsgTypeUserEcho` | 消息回显 |
| `inject_user` | `MsgTypeInjectUser` | bg task 注入 |
| `plugin_widgets` | `MsgTypePluginWidgets` | 插件 widget |
| `session` | `MsgTypeSession` | 会话状态 |
| `runner_status` | `MsgTypeRunnerStatus` | Runner 状态 |
| `sync_progress` | `MsgTypeSyncProgress` | 同步进度 |

不需要映射的类型：`rpc_response`（REST 同步返回）、`tui_control_req`（CLI only）、`__pong__`（WS 内部）。

### SSE 重连重放

1. 浏览器断线后自动重连（EventSource 原生行为），自动携带 `Last-Event-ID: 42` header
2. Server 端 SSE handler 读取该 header，从 eventStream 取 seq > 42 的事件重放
3. 重放完毕后继续推送新事件
4. eventStream 环形缓冲 512 条，断线太久导致缓冲溢出时，前端降级 `POST /api/rpc` 调用 `get_active_progress` 拉取全量快照

### SSE Heartbeat

每 15s 发送 SSE 注释行 `:heartbeat\n\n`，不触发前端事件，保持连接活跃，防止代理/CDN 断开空闲连接。

### 统一响应格式

成功：
```json
{ "ok": true, "data": { ... }, "error": null }
```

错误：
```json
{ "ok": false, "data": null, "error": { "code": "not_found", "message": "会话不存在" } }
```

## 文件拆分

```
channel/web/
├── web.go              // WebChannel 结构体 + Start()（路由注册）+ 生命周期 (~200行)
├── web_auth.go         // 已有 — 认证中间件 + register/login/logout
├── web_sse.go          // 新增 — SSE handler + Hub Client 适配 + heartbeat
├── web_api.go          // 已有 — REST handler（message/cancel/history/search 等）
├── web_fs.go           // 已有 — 文件系统（合并 stat/raw 后）
├── web_file.go         // 已有 — 文件上传
├── web_account.go      // 已有 — 账户/管理员
├── web_hub.go          // 已有 — Hub（保留，CLI WS + Web SSE 共用）
├── web_eventstream.go  // 已有 — eventStream（保留，SSE 重放复用 seq 机制）
├── web_remote_cli.go   // 已有 — RemoteCLIChannel（不动，CLI 远程模式）
└── web_socket.go       // 新增 — 从 web.go 拆出的 WS handler（仅 CLI 用）
```

## 子 Spec 分解

| 子 spec | 标题 | 依赖 |
|---------|------|------|
| [子 spec 1](./07-13-Web-API-REST-SSE-改造-传输层改造.md) | 传输层改造 | 无（基础层） |
| [子 spec 2](./07-13-Web-API-REST-SSE-改造-REST-API改造.md) | REST API 改造 | 子 spec 1 |
| [子 spec 3](./07-13-Web-API-REST-SSE-改造-前端迁移与缓存.md) | 前端迁移与缓存 | 子 spec 1 + 2 |
| [子 spec 4](./07-13-Web-API-REST-SSE-改造-插件市场删除.md) | 插件市场删除 | 无（独立） |

### 依赖顺序

```
子 spec 4 (插件市场删除) ──┐
                           ├── 可并行
子 spec 1 (传输层改造) ────┤
       │                   │
       ▼                   │
子 spec 2 (REST API 改造) ─┘
       │
       ▼
子 spec 3 (前端迁移与缓存)
```

子 spec 4 独立，可与子 spec 1 并行。子 spec 2 依赖子 spec 1（需要 SSE 端点存在）。子 spec 3 依赖子 spec 1 + 2（需要后端端点全部就绪）。

## 不变的部分

以下代码完全不动：

- `agent/transport_remote.go` — CLI 远程 WS 客户端
- `agent/client.go` — 统一 RPC 客户端（CLI 用）
- `agent/req_types.go` — RPC 方法常量和结构体
- `channel/web/web_eventstream.go` — eventStream seq 机制（核心逻辑）
- `channel/web/web_remote_cli.go` — RemoteCLIChannel
- `serverapp/rpc_table.go` — RPCTable handler 注册
- Agent 引擎的所有 progress 推送逻辑

## HTTP/2 配置

- Server 端使用 Go 标准库 `http.Server`，TLS 下自动 h2 协商
- 优先 HTTP/2，兼容 HTTP/1.1 降级
- SSE 在 HTTP/2 下不占用独立 TCP 连接（多路复用），与 REST 请求共享同一连接
- 非 TLS 环境（如本地开发）自动降级为 HTTP/1.1
