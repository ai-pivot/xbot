---
type: Design Spec
title: Web API REST + SSE 改造 — REST API 改造
description: WS 消息迁移为 REST POST 端点、现有端点统一 POST、合并/删除冗余端点
tags:
  - spec
  - rest
  - api
status: draft
repos:
  xbot: 3def45b807e4ed93c9df5b33479373f5e75a8c81
---

# REST API 改造

> 主设计: [Web API REST + SSE 改造主设计](./07-13-Web-API-REST-SSE-改造主设计.md)
> 依赖: [传输层改造](./07-13-Web-API-REST-SSE-改造-传输层改造.md)

## 目标

- 将 WS `readPump` 中的 Web 消息处理逻辑迁移为 REST POST 端点
- 现有 REST 端点统一改为 POST 方法
- 合并冗余端点、删除不需要的端点
- 所有 REST handler 复用 `RPCTable.Dispatch` 和 `WebCallbacks`，不重新实现业务逻辑

## 范围

### 包含

- 新增 REST 端点：`POST /api/message`、`POST /api/cancel`、`POST /api/ask_user/respond`、`POST /api/rpc`
- 现有 GET 端点改为 POST（`/api/history`、`/api/search`、`/api/fs/*` 等）
- 合并端点：`fs/read` + `fs/raw`、`fs/list` + `fs/stat`、`session/status` 合并三个端点
- 删除端点：`/api/cwd`、`/api/session-subscriptions`、`/api/commands`、`/api/channels`
- Runner 端点合并 token 逻辑

### 不包含

- SSE 端点（子 spec 1）
- 前端代码（子 spec 3）
- 插件市场端点删除（子 spec 4）

## 统一响应格式

所有 REST 端点返回统一 JSON 格式：

成功：
```json
{ "ok": true, "data": { ... }, "error": null }
```

错误：
```json
{ "ok": false, "data": null, "error": { "code": "not_found", "message": "会话不存在" } }
```

HTTP 状态码：200（成功）、400（请求错误）、401（未认证）、403（无权限）、404（不存在）、500（服务器错误）。

## 从 WS 迁移的端点

### POST /api/message

从 WS `readPump` 的 `MsgTypeMessage` 处理逻辑迁移。

请求 body：
```json
{
  "content": "用户消息文本",
  "chat_id": "/home/user",
  "file_ids": [...],
  "upload_keys": [...]
}
```

实现：提取 `readPump` 中 message 处理逻辑，注入 `msgBus.Inbound`，与 WS 路径行为一致。

### POST /api/cancel

从 WS `readPump` 的 `MsgTypeCancel` 处理逻辑迁移。

请求 body：
```json
{
  "chat_id": "/home/user"
}
```

实现：注入 `/cancel` 到 msgBus。

### POST /api/ask_user/respond

从 WS `readPump` 的 `MsgTypeAskUserResponse` 处理逻辑迁移。

请求 body：
```json
{
  "chat_id": "/home/user",
  "question_id": "q1",
  "answer": "用户回答"
}
```

实现：复用现有 ask_user_response 路由逻辑。

### POST /api/rpc

通用 RPC 端点，未被语义化端点覆盖的 RPC 方法走此端点。

请求 body：
```json
{
  "method": "get_settings",
  "params": { ... }
}
```

实现：直接调用 `RPCTable.Dispatch(method, params, userID)`，返回 RPC 结果。

## 现有端点改造

### 方法统一

以下端点从 GET 改为 POST，请求参数从 query string 改为 JSON body：

| 端点 | 原方法 | 新方法 | 说明 |
|------|--------|--------|------|
| `/api/auth/config` | GET | POST | 公开配置 |
| `/api/history` | GET | POST | body 含 chat_id |
| `/api/search` | GET | POST | body 含 query |
| `/api/fs/list` | GET | POST | body 含 path |
| `/api/fs/read` | GET | POST | body 含 path + raw |
| `/api/fs/search` | GET | POST | body 含 query + path |
| `/api/chats/list` | GET | POST | 会话列表 |
| `/api/session-tree` | GET | POST | 会话树 |
| `/api/runners/list` | GET | POST | Runner 列表 |
| `/api/runners/active` | GET/PUT | POST | 获取/设置活跃 Runner |
| `/api/account/link-code` | GET | POST | 生成关联码 |
| `/api/account/identities/list` | GET | POST | 身份列表 |
| `/api/admin/users/list` | GET | POST | 用户列表 |
| `/api/context-info` → `/api/session/status` | GET | POST | 合并 |

`/api/files/upload` 保持 POST（multipart），不改。

### 端点合并

#### fs/read + fs/raw → fs/read

`POST /api/fs/read`：
```json
{ "path": "/some/file", "raw": false }
```
- `raw: false`（默认）：读取文件内容，2MB cap，自动检测二进制并返回 base64（原 `fs/read` 逻辑）
- `raw: true`：返回原始文件内容，原始 Content-Type（原 `fs/raw` 逻辑）

#### fs/list + fs/stat → fs/list

`POST /api/fs/list` 返回的目录列表中包含 stat 信息（大小、模式、是否目录），原 `fs/stat` 逻辑合入返回值。

#### context-info + tasks + background-tasks → session/status

`POST /api/session/status`：
```json
{ "chat_id": "/home/user" }
```

响应合并返回：
```json
{
  "token_usage": { ... },
  "tasks": [...],
  "background_tasks": [...]
}
```

#### runner/token → runners

- `POST /api/runners/list` 响应中包含每个 Runner 的 token
- `POST /api/runners/create` 创建时返回 token
- 删除独立的 `/api/runner/token` 端点

#### subagents → session-tree

`POST /api/session-tree` 响应中包含 SubAgent 信息（原 `/api/subagents` 的数据合入会话树）。

### 端点删除

| 删除端点 | 原因 |
|---------|------|
| `/api/cwd` | CWD 变化通过 SSE `session` 事件推送，前端缓存 |
| `/api/session-subscriptions` | 会话切换通过 SSE `session` 事件推送 |
| `/api/commands` | 命令列表基本不变，前端启动时加载一次后缓存 |
| `/api/channels` | 渠道列表基本不变，前端缓存 |
| `/api/subagents` | 合入 session-tree |
| `/api/fs/raw` | 合入 fs/read |
| `/api/fs/stat` | 合入 fs/list |
| `/api/context-info` | 合入 session/status |
| `/api/tasks` | 合入 session/status |
| `/api/background-tasks` | 合入 session/status |
| `/api/runner/token` | 合入 runners |

## 完整端点表

### 认证（4 个，无 authMiddleware）

| 端点 | 用途 |
|------|------|
| `POST /api/auth/register` | 用户注册 |
| `POST /api/auth/login` | 登录 |
| `POST /api/auth/logout` | 登出 |
| `POST /api/auth/config` | 公开配置 |

### 会话管理（5 个）

| 端点 | 用途 |
|------|------|
| `POST /api/chats/list` | 会话列表 |
| `POST /api/chats/create` | 创建会话 |
| `POST /api/chats/{chatID}/switch` | 切换会话 |
| `POST /api/chats/{chatID}/rename` | 重命名 |
| `POST /api/chats/{chatID}/delete` | 删除会话 |

### 消息与历史（5 个）

| 端点 | 用途 |
|------|------|
| `POST /api/message` | 发送用户消息 |
| `POST /api/cancel` | 取消运行 |
| `POST /api/history` | 历史快照 |
| `POST /api/history/rewind` | 历史回退 |
| `POST /api/search` | 全文搜索 |

### 文件系统（4 个）

| 端点 | 用途 |
|------|------|
| `POST /api/files/upload` | 文件上传（multipart） |
| `POST /api/fs/list` | 目录列表（含 stat） |
| `POST /api/fs/read` | 读取文件（`raw: true` 返回原始） |
| `POST /api/fs/search` | 文件搜索 |

### 设置与 LLM（3 个）

| 端点 | 用途 |
|------|------|
| `POST /api/settings` | 设置读写 |
| `POST /api/llm-config` | LLM 配置 CRUD |
| `POST /api/session/status` | Token + 任务 + 后台任务 |

### Runner（4 个）

| 端点 | 用途 |
|------|------|
| `POST /api/runners/list` | Runner 列表（含 token） |
| `POST /api/runners/create` | 创建 Runner |
| `POST /api/runners/{name}/delete` | 删除 Runner |
| `POST /api/runners/active` | 获取/设置活跃 Runner |

### 账户与管理员（6 个）

| 端点 | 用途 |
|------|------|
| `POST /api/account/link-code` | 生成关联码 |
| `POST /api/account/link` | 消费关联码 |
| `POST /api/account/identities/list` | 身份列表 |
| `POST /api/account/identities/{id}/delete` | 解除关联 |
| `POST /api/admin/users/list` | 用户列表 |
| `POST /api/admin/users/{id}/set-role` | 设置角色 |

### 会话树（1 个）

| 端点 | 用途 |
|------|------|
| `POST /api/session-tree` | 会话树（含 SubAgent） |

### 通用 RPC（1 个）

| 端点 | 用途 |
|------|------|
| `POST /api/rpc` | 通用 RPC 调用 |

### AskUser（1 个）

| 端点 | 用途 |
|------|------|
| `POST /api/ask_user/respond` | AskUser 回答 |

### SSE（1 个，GET）

| 端点 | 用途 |
|------|------|
| `GET /api/sse?chat_id=xxx` | SSE 推送连接 |

**总计**：约 34 个 POST 端点 + 1 个 GET（SSE）

## 路由注册

所有路由在 `web.go` 的 `Start()` 方法中注册。Go 1.22+ `ServeMux` 支持路径参数（`{chatID}`、`{name}`、`{id}`）和方法匹配。

除 `auth` 路由外，所有路由经过 `authMiddleware`。

## 验收标准

1. 所有 REST 端点使用 POST 方法（SSE 除外）
2. 统一响应格式正确
3. 从 WS 迁移的端点（message/cancel/ask_user/rpc）行为与原 WS 路径一致
4. 合并的端点返回完整数据
5. 删除的端点返回 404
6. 所有 handler 复用 RPCTable.Dispatch / WebCallbacks，无业务逻辑重复
7. 编译通过，现有功能不回归（CLI WS 不受影响）
