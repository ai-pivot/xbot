# 统一推送层（push/）——provider 无关的系统通知

后台通知不再由客户端维持 SSE 长连接。服务端把**语义事件**（`ask_user` / `turn_done`）
交给**投递通道**（FCM / WebPush / APNs / 飞书…），每条通道实现一个 `Provider`。

## 核心模型

```go
type Event struct {   // 语义，与通道无关
    Kind Kind          // ask_user | turn_done | test
    Action string      // 仅 session_state：busy | idle | ...
    Channel, ChatID, SenderID string
    Title, Body string
    RequestID string   // ask_user 去重键
    HighPriority bool
    CollapseKey string
    TS int64
}

type Provider interface {
    Name() string      // "fcm" | "webpush" | "apns" | "feishu"
    Platform() string  // "android" | "web" | "ios" | "im"
    Validate(t storage.PushTarget) error
    Send(ctx context.Context, t storage.PushTarget, ev Event) error
}
```

`storage.PushTarget` 是统一的目标：`provider + platform + address + metadata`。
`address` 的含义由 provider 决定（FCM token / PushSubscription endpoint / APNs token /
飞书 chat_id）；`metadata` 存 provider 专属字段（WebPush 的 `p256dh`/`auth`、设备名等）。

## 数据流

```
channel/web 事件出口（SendProgress 的 WaitingUser、SendSessionState 的 busy/idle）
        │  web.PushEvent（不 import push 包）
        ▼
serverapp 转成 push.Event → push.Service.Notify（只入队）
        ▼
push.Notifier worker
  · owner 解析（TenantStore.GetSenderForChat）
  · 去重（ask: request_id 10min；done: chat:busyAt 2min）
  · 限流（每 target 10 条/分钟）
  · presence（provider:address 90s 租约，前台客户端续租）
  · 按 target.Provider 调 Registry.Get(...).Send(...)
        ▼
FCM / WebPush / APNs / Feishu Provider
```

- **busy→idle 才算完成**：`Notifier` 在内存里记 `busySince[channel:chatID]`；单独收到
  `idle`（启动/取消）不会发通知。
- **owner 解析不到就丢弃**，绝不群发。
- **permanent 错误删 target**（FCM 404/UNREGISTERED、WebPush 404/410）；
  **transient 错误**指数退避重试 3 次；**auth 错误**由 provider 刷新凭据后重试一次。
- 队列满/限流/去重都只计数 + 日志，**绝不阻塞 agent**。

## 配置（Storage 为准）

推送配置**保存在数据库**（`push_settings`，单个加密 JSON blob）；`config.json` 的
`push` 段只在**首次启动**时作为种子写入数据库，之后以数据库为准。Web 设置 →
**通知 → 推送配置（服务端）** 可以直接修改并保存，服务端会**热切换** runtime（不重启）。

```json
// config.json 的 push 段仅首次启动生效（种子）
{
  "push": {
    "enable": true,
    "include_preview": true,
    "fcm": { "enable": true, "project_id": "...", "credentials_file": "/home/you/.xbot/fcm-service-account.json", "mode": "data" },
    "webpush": { "enable": true, "vapid_public": "...", "vapid_private": "...", "subject": "mailto:...", "endpoint_allowlist": ["fcm.googleapis.com"] }
  }
}
```

- **读取**：`GET /api/push/settings`（VAPID private 返回空串 + `has_vapid_private`）；
  **保存**：`POST /api/push/settings`（完整配置；private 留空 = 保持原值），
  校验通过后加密落库并重建 registry/notifier。
- **落库加密**：`crypto.Encrypt`（AES-256-GCM，`XBOT_ENCRYPTION_KEY`；未设置时
  passthrough 并告警，与 LLM API key 同策略）。
- **秘密仍在文件里**：FCM service account / APNs `.p8` 的实际内容留在磁盘，
  数据库只存**路径**；VAPID private 是唯一进库的秘密。
- `push.enable` 是总开关；关闭时保留配置但不发送。
- `webpush.endpoint_allowlist` 是 **SSRF 闸门**；WebPush 启用时必填。
- FCM `mode`：`data`（app 自己弹通知）/ `notification`（系统展示）；保存后立即生效。
- 数据库 v75 新增 `push_settings`；`push_targets`（订阅目标）本来就是高频数据，
  一直在 Storage。

## HTTP API（都在 web channel 的 auth 之后）

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/push/register` | `{provider, platform, address, metadata, device_name, app_version}`；绑定当前登录用户 |
| POST | `/api/push/unregister` | `{provider, address}`；只能删自己的 |
| POST | `/api/push/presence` | `{provider, address, active}`；前台 30s 续租，后台置 false |
| POST | `/api/push/test` | 给当前用户的所有 target 同步发一条测试消息 |

**新增平台不需要新的端点**：iOS 用 `provider=apns`，Web 用 `provider=webpush`，
飞书用 `provider=feishu`。

## 新增一个 provider

1. 在 `push/` 实现 `Provider`（`Name/Platform/Validate/Send`），错误用
   `PermanentError` / `TransientError` / `AuthError` 分类；
2. 在 `config/config.go` 的 `PushConfig` 加一个 `*XConfig` + 环境变量；
3. 在 `push/build.go` 的 `BuildRegistry` 里注册；
4. 不需要改 `Notifier`、`channel/web` 或 HTTP handler。

## 关键实现位置

| 文件 | 内容 |
|---|---|
| `config/config.go` | `PushConfig` / `FCMConfig` / `WebPushConfig` / `APNsConfig` |
| `storage/interfaces.go` | `PushTarget` / `PushStore`，嵌入 `ConfigStore` |
| `storage/sqlite/schema.go` + `migrations.go`（v74） | `push_targets` 表（SQLite，时间存 unix 秒） |
| `storage/pg_schema.go` + `pg_push.go` | PG 表（`metadata JSONB`、`TIMESTAMPTZ`） |
| `push/notifier.go` | 去重/限流/presence/busy→idle/重试 |
| `push/fcm.go` | service account → JWT → OAuth token 缓存 → FCM v1 |
| `push/webpush.go` | VAPID + aes128gcm（`webpush-go`） |
| `push/service.go` | Registry + Notifier + 注册/注销/presence/test 的业务封装 |
| `channel/web/web_push.go` | HTTP handler（不 import push 包，provider 无关） |
| `serverapp/callbacks.go` | `web.WebCallbacks.PushNotify` 等注入 |

## 坑

- **完成预览必须读 `iteration_history`，不能读 `session_messages`**：assistant 的
  `session_messages` 行在 tool-only 迭代里是**空 content 占位**（实测生产 PG 多条
  `content_len=0`），用它做 `GetLastAssistantPreview` 会永远返回空，通知就退化成
  “本轮回复已完成”。正解：取**最新 turn 的最高 iteration 的非空 `content`**
  （SQLite + PG 同一规则；守护：`storage/sqlite/preview_test.go`）。
- **`writeJSON` 已经包了一层 `{ok,data,error}`**：handler 里只传业务值
  （`writeJSON(w, 200, cfg)`），**不要**再传 `{"ok":true,"data":cfg}` —— 否则
  前端会收到 `data.data`，`cfg.webpush` 变成 `undefined`，界面显示“服务端未启用
  Web Push”（真实事故，2026-10-22；守护：`channel/web/web_push_test.go`）。
- **FCM service account 是秘密**：`~/.xbot/fcm-service-account.json`，`chmod 600`。
  启动时解析失败只会禁用 FCM（`push: ...` 告警），不会让 server 起不来。
- **FCM data-only 在“强行停止”后收不到**（Android 系统行为）；OEM 也可能延迟
  data-only，必要时切 `mode=notification`。
- **`push_targets` 必须两后端都实现**：SQLite 的 `created_at/last_seen_at` 是
  unix 秒（`INTEGER`），PG 是 `TIMESTAMPTZ`；契约测试 `storage/contract_push_test.go`
  两个后端跑同一套。
- **`GetLastAssistantPreview`** 走每会话独立 DB（`Storage.Session(tenantID)`），
  在 Notifier 的 worker 里异步查询，不要放进 `SendSessionState` 的广播路径。
- **WebPush endpoint 必须 https + allowlist**，否则服务端会被当成 SSRF 跳板。
- 飞书/APNs 目前只有配置位与接口，未实现；启用会在启动日志里告警并跳过。
