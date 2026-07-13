---
type: Design Spec
title: Web API REST + SSE 改造 — 前端迁移与缓存
description: Web 前端从 WS 迁移到 REST + SSE、三层缓存、弱网降级、缓存清除功能
tags:
  - spec
  - frontend
  - cache
  - sse
status: draft
repos:
  xbot: 3def45b807e4ed93c9df5b33479373f5e75a8c81
---

# 前端迁移与缓存

> 主设计: [Web API REST + SSE 改造主设计](./07-13-Web-API-REST-SSE-改造主设计.md)
> 依赖: [传输层改造](./07-13-Web-API-REST-SSE-改造-传输层改造.md) + [REST API 改造](./07-13-Web-API-REST-SSE-改造-REST-API改造.md)

## 目标

- Web 前端断开 WS 连接，改用 REST POST + SSE
- 实现三层缓存优化弱网体验
- 弱网降级链路完整
- 前端设置页新增缓存清除功能

## 范围

### 包含

- 前端 WS 客户端代码替换为 REST + EventSource
- 三层缓存实现（消息缓存、进度快照、会话树）
- 弱网降级逻辑
- SSE 连接生命周期管理（打开/切换/关闭）
- 设置页缓存清除功能

### 不包含

- 后端端点实现（子 spec 1 + 2）
- 前端 UI 设计变更（仅替换通讯层，不改 UI 布局）

## 前端通讯层迁移

### WS 客户端 → REST + SSE

#### 当前架构

前端通过 `WebSocket` 连接 `/ws`，双向通讯：
- 发送消息、cancel、ask_user_response、subscribe、rpc 请求 → WS send
- 接收 text、progress、stream_content、ask_user 等 → WS onmessage

#### 迁移后

```
前端发送 → POST /api/...（REST）
前端接收 ← GET /api/sse?chat_id=xxx（SSE）
```

- 所有 client→server 操作改为 `fetch` POST 请求
- 所有 server→client 推送改为 `EventSource` 监听

### EventSource 管理

```javascript
// 每会话一条 SSE
let eventSource = null;
let currentChatID = null;

function connectSSE(chatID) {
    if (eventSource) eventSource.close();
    currentChatID = chatID;
    eventSource = new EventSource(`/api/sse?chat_id=${chatID}`);

    // 通用事件处理
    eventSource.addEventListener('text', (e) => { ... });
    eventSource.addEventListener('progress_structured', (e) => { ... });
    eventSource.addEventListener('stream_content', (e) => { ... });
    eventSource.addEventListener('ask_user', (e) => { ... });
    eventSource.addEventListener('card', (e) => { ... });
    eventSource.addEventListener('user_echo', (e) => { ... });
    eventSource.addEventListener('inject_user', (e) => { ... });
    eventSource.addEventListener('plugin_widgets', (e) => { ... });
    eventSource.addEventListener('session', (e) => { ... });
    eventSource.addEventListener('runner_status', (e) => { ... });
    eventSource.addEventListener('sync_progress', (e) => { ... });

    // EventSource 自动重连 — 无需手动实现
    // Last-Event-ID 自动携带 — 无需手动管理
}

function disconnectSSE() {
    if (eventSource) {
        eventSource.close();
        eventSource = null;
    }
}
```

### REST 请求封装

```javascript
async function postAPI(endpoint, body) {
    const resp = await fetch(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    const result = await resp.json();
    if (!result.ok) throw new Error(result.error?.message || 'Unknown error');
    return result.data;
}
```

## 三层缓存

### 1. 消息缓存（Memory）

```javascript
const messagesCache = {};      // chatID → 消息列表
const lastSeqCache = {};       // chatID → 最后接收的 seq
```

- SSE 事件到达时，检查 seq > lastSeq[chatID]，是则 append，否则丢弃（去重）
- 更新 lastSeq[chatID] = seq
- 切换会话时先渲染 messagesCache[chatID]（如有），SSE 接续增量

### 2. 进度快照缓存（Memory）

```javascript
const progressSnapshotCache = {};  // chatID → 最近一次 ProgressEvent
```

- 每次 `progress_structured` 事件更新 progressSnapshotCache[chatID]
- SSE 重连后如果重放为空（seq 差距过大），降级 `POST /api/rpc` 调 `get_active_progress` 拉取全量快照

### 3. 会话树缓存（LocalStorage）

```javascript
// localStorage key: "xbot_session_tree"
```

- 启动时先从 LocalStorage 读取并渲染，再后台 `POST /api/session-tree` 刷新
- SSE `session` 事件触发刷新
- 会话操作（创建/删除/重命名）后主动刷新

## 弱网降级链

| 场景 | 正常路径 | 降级路径 |
|------|---------|---------|
| SSE 断线短时 | 浏览器自动重连 + Last-Event-ID 重放 | — |
| SSE 断线久（缓冲溢出） | — | 重连后重放为空 → POST /api/rpc 调 get_active_progress 拉全量快照 |
| SSE 连接失败 | — | 轮询 POST /api/session/status（5s 间隔）直到 SSE 恢复 |
| 发送消息失败 | — | 前端重试（指数退避，最多 3 次），失败后提示用户 |
| 历史加载 | SSE 事件增量追加 | POST /api/history 拉全量 |

### SSE 连接失败降级

```javascript
let pollTimer = null;

eventSource.onerror = () => {
    // EventSource 会自动重连，但如果持续失败则降级轮询
    if (pollTimer) return;  // 已在轮询
    pollTimer = setInterval(async () => {
        try {
            await postAPI('/api/session/status', { chat_id: currentChatID });
            // 如果成功，尝试重连 SSE
            connectSSE(currentChatID);
            clearInterval(pollTimer);
            pollTimer = null;
        } catch (e) {
            // 继续轮询
        }
    }, 5000);
};
```

### 发送消息重试

```javascript
async function sendMessage(content, chatID) {
    const maxRetries = 3;
    let delay = 1000;  // 1s, 2s, 4s
    for (let i = 0; i < maxRetries; i++) {
        try {
            return await postAPI('/api/message', { content, chat_id: chatID });
        } catch (e) {
            if (i === maxRetries - 1) throw e;
            await new Promise(r => setTimeout(r, delay));
            delay *= 2;
        }
    }
}
```

## SSE 连接生命周期

```
用户打开会话 A
  → connectSSE("A")
  → EventSource 维持连接，接收事件
  → 前端按 event type 分发到对应 handler
  → 更新 messagesCache["A"] + lastSeqCache["A"]

用户切换到会话 B
  → disconnectSSE()  // 关闭 A 的 SSE
  → connectSSE("B")
  → 先渲染 messagesCache["B"]（如有）
  → 接收 B 的事件

会话 A 有新消息（bg task 完成等）
  → SSE 不推送（已断开）
  → 用户切回 A 时 connectSSE("A")
  → EventSource 自动携带 Last-Event-ID 重放遗漏事件
```

## 缓存清除功能

前端设置页新增"清除缓存"按钮，清除内容：

1. LocalStorage：删除 `xbot_session_tree`
2. Memory：清空 `messagesCache`、`lastSeqCache`、`progressSnapshotCache`
3. 清除后重新加载页面（`location.reload()`），从 server 拉取全量数据

```javascript
function clearCache() {
    localStorage.removeItem('xbot_session_tree');
    Object.keys(messagesCache).forEach(k => delete messagesCache[k]);
    Object.keys(lastSeqCache).forEach(k => delete lastSeqCache[k]);
    Object.keys(progressSnapshotCache).forEach(k => delete progressSnapshotCache[k]);
    location.reload();
}
```

## 验收标准

1. 前端不再连接 `/ws`，所有通讯走 REST POST + SSE
2. SSE 事件正确接收和分发（11 种 event type）
3. EventSource 自动重连 + Last-Event-ID 重放正常工作
4. 三层缓存正确更新和读取
5. 弱网降级链路完整（缓冲溢出降级、SSE 失败轮询、消息重试）
6. 缓存清除功能正常工作
7. 切换会话时 SSE 正确断开重连，缓存正确切换
