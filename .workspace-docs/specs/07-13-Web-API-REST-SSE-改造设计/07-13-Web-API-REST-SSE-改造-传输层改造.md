---
type: Design Spec
title: Web API REST + SSE 改造 — 传输层改造
description: Hub Client 扩展支持 SSE、SSE handler 实现、web.go 文件拆分
tags:
  - spec
  - transport
  - sse
  - hub
status: draft
repos:
  xbot: 3def45b807e4ed93c9df5b33479373f5e75a8c81
---

# 传输层改造

> 主设计: [Web API REST + SSE 改造主设计](./07-13-Web-API-REST-SSE-改造主设计.md)

## 目标

- 扩展 Hub `Client` 结构体支持 SSE 客户端
- 实现 SSE handler（连接、事件推送、heartbeat、重连重放）
- 将 `web.go` 拆分为多文件，WS handler 提取到 `web_socket.go`
- 引擎和 Hub 核心逻辑零改动

## 范围

### 包含

- Hub `Client` 结构体扩展（新增 `connType`、`w`、`flusher` 字段）
- `web_sse.go` 新建（SSE handler + writeLoop + heartbeat）
- `web_socket.go` 新建（从 `web.go` 拆出 `handleWS`、`readPump`、`writePump`）
- `web.go` 瘦身（仅保留 `WebChannel` 结构体 + `Start()` 路由注册 + 生命周期）
- SSE 连接注册为 Hub Client 的逻辑
- SSE `Last-Event-ID` 重连重放（复用 eventStream）

### 不包含

- REST 端点实现（子 spec 2）
- 前端代码（子 spec 3）
- Hub 核心路由逻辑改动（`web_hub.go` 仅扩展 Client 结构体字段，不改路由逻辑）
- eventStream 核心逻辑改动（`web_eventstream.go` 零修改）

## Hub Client 扩展

### 当前状态

Hub `Client` 结构体（`web_hub.go`）当前仅支持 WS 连接，字段包含 `wsConn *websocket.Conn`、`sendCh chan WSMessage`、`chatID`、`userID` 等。

### 改造内容

扩展 `Client` 结构体：

```go
type Client struct {
    // 共有字段
    sendCh  chan WSMessage
    chatID  string
    userID  string

    // 新增：客户端类型
    connType string  // "ws" | "sse"

    // WS 专用（connType == "ws"）
    wsConn *websocket.Conn

    // SSE 专用（connType == "sse"）
    w       http.ResponseWriter
    flusher http.Flusher
    done    chan struct{}
}
```

Hub 的 `sendToClient`、`addClient`、`removeClient` 等路由方法不变 — 它们只操作 `sendCh`，不关心客户端类型。

### writeLoop 分支

Client 的写出循环根据 `connType` 分支：

- `connType == "ws"`：现有 `writePump` 逻辑不变（30s ping + WS 帧写出）
- `connType == "sse"`：新增 `sseWriteLoop` — 从 `sendCh` 读取 `WSMessage`，转为 SSE 事件格式写出 + 15s heartbeat

## SSE Handler 实现

### 端点

`GET /api/sse?chat_id=xxx`

### 鉴权

`authMiddleware` 验证 Cookie session，注入 senderID。

### 连接建立流程

1. `authMiddleware` 验证 Cookie → 注入 senderID
2. 读取 `chat_id` query 参数
3. 设置 SSE 响应头：
   - `Content-Type: text/event-stream`
   - `Cache-Control: no-cache`
   - `Connection: keep-alive`
   - `X-Accel-Buffering: no`（禁用 Nginx 缓冲）
4. 获取 `http.Flusher`
5. 创建 SSE `Client`（`connType: "sse"`），注册到 Hub
6. 读取 `Last-Event-ID` header → 解析为 last_seq
7. 如果 last_seq > 0：从 eventStream 重放 seq > last_seq 的事件
8. 启动 `sseWriteLoop` goroutine（阻塞直到连接断开）

### sseWriteLoop 逻辑

```
ticker := 15s heartbeat ticker
for {
    select {
    case msg := <-sendCh:
        写出 SSE 事件:
        fmt.Fprintf(w, "id: %d\n", msg.Seq)
        fmt.Fprintf(w, "event: %s\n", msg.Type)
        json, _ := json.Marshal(msg)
        fmt.Fprintf(w, "data: %s\n\n", json)
        flusher.Flush()

    case <-ticker.C:
        fmt.Fprintf(w, ":heartbeat\n\n")
        flusher.Flush()

    case <-done:
        return  // 客户端断开
    }
}
```

### 重连重放

SSE handler 读取 `Last-Event-ID` header（浏览器自动携带），解析为整数 last_seq。调用 eventStream 的重放逻辑（与 WS 的 `replayMissedEvents` 相同），将 seq > last_seq 的事件逐条通过 SSE 格式写出。

如果重放后仍无 progress 事件，额外发送当前 `GetActiveProgress` 快照（与 WS 重连逻辑一致）。

## web.go 文件拆分

### 拆分前

`web.go`（~1624 行）混合了：WebChannel 结构体、路由注册、WS handler（handleWS/readPump/writePump）、中间件、Hub 桥接等。

### 拆分后

#### `web.go`（~200 行）

保留：
- `WebChannel` 结构体定义
- `Start()` 方法（路由注册 — 新增 SSE 路由 + REST 路由）
- `Stop()` 方法
- 生命周期管理
- `securityHeadersMiddleware`

#### `web_socket.go`（新建）

从 `web.go` 移出：
- `handleWS` — WS 连接入口（仅 CLI 远程模式连接）
- `readPump` — WS 消息读取和分发
- `writePump` — WS 消息推送 + 30s ping
- `wsUpgrader`
- WS 相关的辅助函数

注意：`readPump` 中处理 Web 客户端消息的逻辑（message/cancel/ask_user_response 等）保留在本文件中，但子 spec 2 会新增对应的 REST handler。WS 的 `readPump` 仅服务 CLI 后不再处理 Web 消息类型（但代码先保留，不影响功能）。

#### `web_sse.go`（新建）

- `handleSSE` — SSE 连接入口
- `sseWriteLoop` — SSE 事件写出循环
- SSE heartbeat 逻辑
- `Last-Event-ID` 重放逻辑
- SSE Client 创建和 Hub 注册

### Hub 文件影响

`web_hub.go` 仅扩展 `Client` 结构体字段，不改路由逻辑。`addClient`/`removeClient`/`sendToClient` 等方法不变。

## 验收标准

1. SSE 连接建立后能接收 Hub 推送的事件，格式为标准 SSE（`id:`/`event:`/`data:`）
2. 15s heartbeat 正常发送
3. 断线后浏览器自动重连，`Last-Event-ID` 重放正确
4. WS 客户端（CLI 远程模式）功能不受影响
5. `web.go` 拆分后编译通过，WS 功能不回归
6. Hub 同时服务 WS client 和 SSE client，seq 一致
