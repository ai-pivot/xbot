# xbot 日志类别与字段标准

> **版本**: v2.0 | **日期**: 2026-07-17  
> **适用范围**: 全项目所有 `log.Xxx()` / `log.Ctx(ctx).Xxx()` 调用

---

## 1. 日志生命周期分类

每条日志**必须**属于以下四种生命周期之一。生命周期决定了**必填字段**和**日志调用方式**。

### 四种生命周期

```
┌──────────────────────────────────────────────────────────────────────────┐
│  Global（全局级）                                                         │
│  进程级事件，不归属于任何用户/会话/请求                                    │
│  例：启动初始化、Channel 注册、DB 迁移、日志轮转                           │
│  ┌────────────────────────────────────────────────────────────────────┐  │
│  │  User（用户级）                                                     │  │
│  │  跨会话但归属特定用户的事件                                          │  │
│  │  例：订阅增删改、设置变更、身份解析、模型列表刷新、权限校验           │  │
│  │  ┌──────────────────────────────────────────────────────────────┐ │  │
│  │  │  Session（会话级）                                           │ │  │
│  │  │  跨多次请求但归属于一个会话                                    │ │  │
│  │  │  例：会话创建/删除、交互会话 spawn/unload、持久化恢复          │ │  │
│  │  │  ┌──────────────────────────────────────────────────────┐   │ │  │
│  │  │  │  Request（请求级）                                     │   │ │  │
│  │  │  │  单次用户消息内的处理事件                               │   │ │  │
│  │  │  │  例：LLM 调用、工具执行、压缩检查、迭代                  │   │ │  │
│  │  │  └──────────────────────────────────────────────────────┘   │ │  │
│  │  └──────────────────────────────────────────────────────────────┘ │  │
│  └────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────┘
```

### 1.1 Request（请求级）

在一次用户消息处理过程中产生的日志。有明确的 `request_id`。

| 规则 | 说明 |
|------|------|
| **必填字段** | `request_id`, `category`, `session_key`, `user_id` |
| **调用方式** | `log.Ctx(ctx).WithField("category", "xxx")...` |
| **典型场景** | LLM 流式调用、工具执行、压缩检查、SubAgent 消息注入、迭代进度 |
| **request_id 来源** | `processMessage` 注入 context → 全链路传递 |

**当前问题**：远程模式下 `[LLM] Starting stream request`(83条) 和 `maybeCompress check`(80条) 丢了 request_id — 这是断链 bug，不是生命周期归类问题。

### 1.2 Session（会话级）

跨多次请求、但归属于一个会话的生命周期事件。有 `session_key` 但不一定有 `request_id`。

| 规则 | 说明 |
|------|------|
| **必填字段** | `category`, `session_key`, `user_id` |
| **可选字段** | `request_id`（如果事件发生在请求处理过程中） |
| **调用方式** | `log.WithFields(log.Fields{"category":"session","session_key":key,"user_id":uid,...})` |
| **典型场景** | 会话创建/切换/删除/恢复、交互会话 spawn/unload、持久化恢复 |

```go
// ✅ 会话级：会话创建（可能在请求处理中，也可能独立操作）
log.WithFields(log.Fields{
    "category":    "session",
    "session_key": sessionKey,
    "user_id":     senderID,
    "action":      "created",
}).Info("session created")
```

### 1.3 User（用户级）

跨会话但归属特定用户的事件。有 `user_id` 但不需要 `session_key`。

| 规则 | 说明 |
|------|------|
| **必填字段** | `category`, `user_id` |
| **不需要** | `session_key`（跨会话事件），`request_id`（通常不在请求链路中） |
| **可选字段** | `request_id`（如果由 RPC 触发，如模型刷新） |
| **调用方式** | `log.WithFields(log.Fields{"category":"subscription","user_id":uid,...})` |
| **典型场景** | 订阅增删改、模型列表刷新、设置变更（thinking_mode 等）、身份解析、权限校验、runner token 管理 |

```go
// ✅ 用户级：订阅添加
log.WithFields(log.Fields{
    "category": "subscription",
    "user_id":  senderID,
    "action":   "added",
    "sub_id":   subID,
    "provider": sub.Provider,
}).Info("subscription added")

// ✅ 用户级：身份解析
log.WithFields(log.Fields{
    "category": "auth",
    "user_id":  senderID,
    "role":     resolvedRole,
}).Info("identity resolved")

// ✅ 用户级：模型列表刷新（RPC 触发，有 request_id）
log.Ctx(ctx).WithFields(log.Fields{
    "category": "subscription",
    "user_id":  senderID,
    "sub_id":   subID,
    "count":    len(models),
}).Info("models refreshed")
```

### 1.4 Global（全局级）

进程级事件，不归属于任何用户或会话。

| 规则 | 说明 |
|------|------|
| **必填字段** | `category` |
| **不需要** | `user_id`, `request_id`, `session_key` |
| **调用方式** | `log.WithField("category", "startup")...` |
| **典型场景** | 启动/关闭、Channel 注册/注销、Cron 调度循环、DB 迁移、配置加载、连接管理 |

```go
// ✅ 全局级：启动
log.WithFields(log.Fields{
    "category":  "startup",
    "component": "agent",
}).Info("Agent loop started")

// ✅ 全局级：Cron 调度（fire 时生成 request_id，但调度循环本身是全局的）
log.WithFields(log.Fields{
    "category": "cron",
    "job_id":   job.ID,
}).Info("cron job fired")
```

### 生命周期判定规则

```
问：这条日志在单次用户请求处理过程中产生？
  → 是 → Request（必须 log.Ctx(ctx)，必须 request_id + session_key + user_id）
  → 否 → 问：归属于某个会话/连接？
    → 是 → Session（必须 session_key + user_id）
    → 否 → 问：归属于某个特定用户？
      → 是 → User（必须 user_id）
      → 否 → Global（仅 category）
```

---

## 2. 日志级别标准

| 级别 | 语义 | 使用准则 | 示例 |
|------|------|----------|------|
| **Error** | 操作失败，需要人工介入 | 不可恢复的错误；影响用户体验的异常 | LLM API 返回 400、DB 写入失败、持久化失败 |
| **Warn** | 潜在风险或降级运行 | 可恢复但不应频繁出现；自动降级/重试 | LLM 重试、sandbox 降级到 None、channel 配置缺失 |
| **Info** | 请求生命周期关键节点 | **仅记录"一条请求发生了什么"的关键节点**，不记录内部状态 | 消息开始处理、LLM 调用发起、工具调用开始/结束、SubAgent 启动/结束 |
| **Debug** | 内部状态和调试细节 | 高频、详细、仅供开发者排查问题 | 进度快照、压缩检查、session 事件、缓存命中 |

### 级别判定规则

```
问：这条日志出了问题是否需要人工看？
  → 是 → Error 或 Warn
  → 否 → 问：用户是否关心这个事件发生了？
    → 是 → Info
    → 否 → Debug
```

---

## 3. 日志类别（`category` 字段）

每条日志**必须**携带 `category` 字段，标识日志所属的业务领域。

### 类别定义

| category | 覆盖范围 | 典型日志 |
|----------|----------|----------|
| `request` | 请求生命周期 | 消息接收、处理开始/结束、取消、响应发送 |
| `llm` | LLM 调用 | 流式请求发起、首 chunk、流式完成、重试、token 统计 |
| `tool` | 工具执行 | 工具调用开始、完成、错误、offload |
| `agent` | Agent 循环 | 迭代开始/结束、压缩检查/执行、context 管理 |
| `subagent` | SubAgent 生命周期 | spawn、unload、interrupt、消息注入 |
| `rpc` | RPC 调用 | RPC 请求分发、响应、panic 恢复 |
| `channel` | Channel 适配器 | 注册/注销、消息接收、消息发送 |
| `session` | 会话管理 | 创建、切换、删除、恢复、持久化 |
| `subscription` | 订阅管理 | 添加、删除、更新、切换、模型刷新 |
| `cron` | 定时任务 | job 触发、调度错误、job 过期清理 |
| `hook` | Hook 系统 | 事件分发、决策结果、handler panic |
| `plugin` | 插件系统 | 激活、停用、工具注册、widget 渲染 |
| `transport` | 传输层 | 连接建立/断开、重连、WS 读写 |
| `config` | 配置管理 | 配置加载、设置变更、runner 切换 |
| `db` | 数据存储 | 迁移、写入失败、查询异常 |
| `auth` | 认证授权 | 身份解析、权限校验 |
| `startup` | 启动关闭 | 组件初始化、服务启停 |
| `tui` | TUI 渲染 | （仅非 CLI channel 的 TUI 控制） |

### 使用方式

```go
// ✅ 正确：带 category
log.Ctx(ctx).WithField("category", "llm").Info("stream request started")

// ✅ 正确：带 category + 额外字段
log.Ctx(ctx).WithFields(log.Fields{
    "category": "tool",
    "tool":     "Shell",
    "elapsed_ms": 234,
}).Info("tool completed")

// ❌ 错误：无 category
log.Ctx(ctx).Info("something happened")
```

---

## 4. 字段标准

### 4.1 生命周期 × 必填字段矩阵

| 生命周期 | `category` | `request_id` | `session_key` | `user_id` | 调用方式 |
|----------|-----------|--------------|---------------|-----------|----------|
| **Request** | ✅ 必填 | ✅ 必填 | ✅ 必填 | ✅ 必填 | `log.Ctx(ctx)` |
| **Session** | ✅ 必填 | ⬜ 可选 | ✅ 必填 | ✅ 必填 | `log.WithFields(...)` 或 `log.Ctx(ctx)` |
| **User** | ✅ 必填 | ⬜ 可选 | ❌ 不需要 | ✅ 必填 | `log.WithFields(...)` 或 `log.Ctx(ctx)` |
| **Global** | ✅ 必填 | ❌ 不需要 | ❌ 不需要 | ❌ 不需要 | `log.WithField("category", ...)` |

### 4.2 日志基础字段（全部自动携带）

| 字段 | 来源 | 说明 |
|------|------|------|
| `time` | logrus 自动 | ISO 8601 + 时区 |
| `level` | logrus 自动 | info/warning/error/debug |
| `msg` | 调用参数 | 简短的事件描述（见 §5 命名规范） |

### 4.3 领域专用字段

#### `llm` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `model` | string | 模型名（如 `deepseek-chat`） |
| `provider` | string | 提供商（`openai` / `anthropic`） |
| `msg_count` | int | 发送给 LLM 的消息条数 |
| `prompt_tokens` | int | 输入 token 数 |
| `completion_tokens` | int | 输出 token 数 |
| `thinking_mode` | string | 思维链模式（`""` / `enabled` / `disabled`） |
| `retry` | int | 当前重试次数 |
| `elapsed_ms` | int | 耗时（毫秒） |

#### `tool` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `tool` | string | 工具名（如 `Shell`、`Read`） |
| `elapsed_ms` | int | 耗时（毫秒） |
| `is_error` | bool | 工具执行是否出错 |
| `offloaded` | bool | 结果是否被 offload |

#### `agent` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `msg_count` | int | 当前消息条数 |
| `prompt_tokens` | int | 当前 prompt token 数 |
| `max_context` | int | 最大上下文 token 数 |
| `need_compress` | bool | 是否需要压缩 |
| `source` | string | token 数据来源（`api` / `restored` / `no_data`） |

#### `subagent` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `role` | string | SubAgent 角色名 |
| `instance` | string | SubAgent 实例 ID |
| `background` | bool | 是否后台运行 |
| `parent_request_id` | string | 父 Agent 的 requestID |

#### `rpc` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `method` | string | RPC 方法名（如 `send_inbound`） |
| `rpc_id` | string | RPC 调用 ID |
| `elapsed_ms` | int | 耗时 |

#### `session` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `session_key` | string | `channel:chatID` |
| `action` | string | 操作类型（`created` / `switched` / `deleted` / `restored`） |

#### `cron` 类别

| 字段 | 类型 | 说明 |
|------|------|------|
| `job_id` | string | Cron job ID |
| `channel` | string | 目标 channel |
| `chat_id` | string | 目标 chatID |

---

## 5. msg 命名规范

### 5.1 格式

```
<动作> <对象> [结果]
```

- **动作**: 动词（`started` / `completed` / `failed` / `fired` / `injected`）
- **对象**: 名词（`LLM stream` / `tool execution` / `cron job`）
- **结果**: 可选（`succeeded` / `retried` / `degraded`）

### 5.2 示例

| ✅ 正确 | ❌ 错误 | 原因 |
|---------|---------|------|
| `LLM stream started` | `[LLM] Starting stream request` | 方括号冗余，用过去式 |
| `Tool Shell completed (234ms)` | `Tool done` | 缺工具名和耗时 |
| `Cron job fired` | `Cron job fired` ✅ | — |
| `Compression check: not needed` | `maybeCompress check` | 函数名泄漏到日志 |
| `SubAgent spawned: dev/fix-logs` | `Interactive session spawned in background` | 缺角色/实例 |

### 5.3 禁止事项

- ❌ **禁止在 msg 中放参数 JSON**：`Tool call: Shell({"command":"..."})` → 参数应放字段
- ❌ **禁止函数名作为 msg**：`maybeCompress check` → 应描述业务语义
- ❌ **禁止方括号前缀**：`[LLM] xxx` → category 字段已表达
- ❌ **禁止拼接长字符串**：用结构化字段替代

---

## 6. 当前日志改造对照表

### Info 级别需降级为 Debug

| 当前 msg | 原因 | 目标 |
|----------|------|------|
| `remote CLI: stored progress snapshot` | 高频内部状态 | ✅ 已降级 |
| `maybeCompress check` (need=false) | 每次迭代打，无价值 | 仅 `need=true` 时 Info |
| `[LLM] Starting stream request` | 与 `First chunk received` 重复 | Debug |
| `Tool call: <完整参数JSON>` | 参数泄露到 msg | Debug，参数移到字段 |
| `Tool result offloaded` | 低频内部细节 | Debug |
| `buildCLIProgressEventHandler` | 内部构建 | Debug |
| `Observation masking*` (4条) | 内部 context 管理 | Debug |
| `Hub: client subscribed` | 内部路由 | Debug |
| `RPC get_history` / `RPC refresh_model_entries` | RPC 内部 | Debug |

### Info 级别需补字段

| 当前 msg | 缺失字段 | 补充 |
|----------|----------|------|
| `Tool done` | tool, elapsed_ms | `Tool Shell completed (234ms)` |
| `[LLM] Models loaded` | request_id, model | 改用 `log.Ctx(ctx)` |
| `Processing: <内容>` | category | 加 `category: "request"` |
| `Channel registered` | channel | 加 channel 字段 |
| `Interactive session spawned` | role, instance | 加角色和实例 |

### Warning 级别需加 category

| 当前 msg | category |
|----------|----------|
| `[LLM] Retrying stream request` | `llm` |
| `Failed to load user agent roles` | `startup` |
| `Shell command failed` | `tool` |
| `[LLM] RefreshModelEntries: /models fetch failed` | `llm` |

---

## 7. 使用模式

### 7.1 Request 级（有 ctx，请求处理链路内）

```go
// ✅ 标准模式
log.Ctx(ctx).WithFields(log.Fields{
    "category": "llm",
    "model":    model,
    "msg_count": len(messages),
}).Info("LLM stream started")

log.Ctx(ctx).WithFields(log.Fields{
    "category":    "tool",
    "tool":        "Shell",
    "elapsed_ms":  elapsed,
    "is_error":    result.IsError,
}).Info("tool completed")
```

### 7.2 Session 级（会话事件，无 ctx 或 ctx 可选）

```go
// ✅ 会话创建（可能在请求中，也可能独立操作）
log.WithFields(log.Fields{
    "category":    "session",
    "session_key": sessionKey,
    "user_id":     senderID,
    "action":      "created",
}).Info("session created")

// ✅ 交互会话 spawn（会话级，但有 request_id）
log.Ctx(ctx).WithFields(log.Fields{
    "category":    "subagent",
    "session_key": sessionKey,
    "user_id":     senderID,
    "role":        "dev",
    "instance":    "fix-logs",
    "background":  true,
}).Info("SubAgent spawned")
```

### 7.3 User 级（跨会话、归属用户）

```go
// ✅ 订阅添加（RPC 触发，有 request_id）
log.Ctx(ctx).WithFields(log.Fields{
    "category": "subscription",
    "user_id":  senderID,
    "action":   "added",
    "sub_id":   subID,
    "provider": sub.Provider,
}).Info("subscription added")

// ✅ 身份解析（无 ctx，用户级）
log.WithFields(log.Fields{
    "category": "auth",
    "user_id":  senderID,
    "role":     resolvedRole,
}).Info("identity resolved")

// ✅ 模型列表刷新
log.Ctx(ctx).WithFields(log.Fields{
    "category": "subscription",
    "user_id":  senderID,
    "sub_id":   subID,
    "count":    len(models),
}).Info("models refreshed")
```

### 7.4 Global 级（启动、调度、配置）

```go
// ✅ 启动
log.WithFields(log.Fields{
    "category":  "startup",
    "component": "agent",
}).Info("Agent loop started")

// ✅ Channel 注册
log.WithFields(log.Fields{
    "category": "channel",
    "channel":  "feishu",
}).Info("Channel registered")

// ✅ Cron 触发（fire 时生成 request_id，但调度循环本身是全局的）
log.WithFields(log.Fields{
    "category":   "cron",
    "job_id":     job.ID,
    "request_id": reqID,
}).Info("cron job fired")
```

### 7.5 错误日志模式

```go
// ✅ Request 级错误
log.Ctx(ctx).WithFields(log.Fields{
    "category": "llm",
    "model":    model,
    "retry":    attempt,
}).WithError(err).Error("LLM stream failed")

// ✅ User 级错误
log.WithFields(log.Fields{
    "category": "subscription",
    "user_id":  senderID,
}).WithError(err).Error("subscription update failed")

// ✅ Session 级错误
log.WithFields(log.Fields{
    "category":    "session",
    "session_key": key,
    "user_id":     uid,
}).WithError(err).Error("session restore failed")

// ✅ Global 级错误
log.WithFields(log.Fields{
    "category":  "db",
    "component": "migrator",
}).WithError(err).Error("DB migration failed")

// ❌ 错误：丢失上下文
log.WithError(err).Error("LLM stream failed")
```

---

## 8. 当前日志生命周期归类

### 当前 Info 日志的生命周期分布（今日 2,346 条）

| 生命周期 | 日志消息 | 数量 | 问题 |
|----------|----------|------|------|
| **Request** | `Processing: <内容>` | 7 | ✅ 有 request_id，❌ 缺 user_id |
| | `Tool done` | 190 | ✅ 有 request_id，❌ 缺 tool/elapsed |
| | `[LLM] Starting stream request` | 172+83 | ⚠️ 83 条缺 request_id（远程断链）|
| | `maybeCompress check` | 168+80 | ⚠️ 80 条缺 request_id |
| | `Tool call: <参数JSON>` | 13 | ✅ 有 request_id，❌ 参数泄入 msg |
| | `Tool result offloaded` | 15+6 | ⚠️ 6 条缺 request_id |
| | `Injected bg subagent notification` | 3 | ✅ 有 request_id |
| | `remote CLI: stored progress snapshot` | 1,375 | ❌ 应降为 Debug |
| **Session** | `Interactive session spawned` | 3 | ❌ 缺 session_key + user_id |
| | `Interactive session unloaded` | 3 | ❌ 缺 session_key + user_id |
| | `Interactive session interrupted` | 1 | ❌ 缺 session_key + user_id |
| | `Background interactive session interrupted` | 1 | ❌ 缺 session_key + user_id |
| | `Session messages purged` | 2 | ❌ 缺 session_key + user_id |
| | `Tenant deleted` | 3 | ❌ 缺 session_key + user_id |
| | `Tenant session destroyed` | 3 | ❌ 缺 session_key + user_id |
| | `sendMessage directSend dispatch` | 7 | ❌ 缺 session_key + user_id，应降为 Debug |
| | `RPC get_history` | 3 | ❌ 缺 session_key + user_id，应降为 Debug |
| **User** | `[LLM] GetLLMForModel: exact match` | 3 | ❌ 缺 user_id，应降为 Debug |
| | `[LLM] Models loaded from API` | 4 | ❌ 缺 user_id，应降为 Debug |
| | `RPC refresh_model_entries` | 3 | ❌ 缺 user_id，应降为 Debug |
| **Global** | `Channel registered` | 3 | ✅ 正确 |
| | `Channel unregistered` | 3 | ✅ 正确 |
| | `Hub: client subscribed` | 2 | ❌ 应降为 Debug |
| | `buildCLIProgressEventHandler` | 8 | ❌ 应降为 Debug |
| | `ObservationMaskStore: loaded` | 1 | ❌ 应降为 Debug |
| | `Stale offloads detected` | 1 | ❌ 应降为 Debug |

---

## 9. 实施路径

| 阶段 | 内容 | 优先级 |
|------|------|--------|
| **Phase 1** | `category` 字段加入 `log.Ctx(ctx)` 和 `log.WithFields` 调用 | P0 |
| **Phase 2** | Info→Debug 降级（9 类日志） | P0 |
| **Phase 3** | 补齐缺失的 `request_id`（M1/M5） | P1 |
| **Phase 4** | msg 命名规范化（去 `[LLM]` 前缀等） | P2 |
| **Phase 5** | `Tool done` 等补字段 | P2 |
