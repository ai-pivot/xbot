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

## 配置

```json
{
  "push": {
    "enable": true,
    "include_preview": true,
    "fcm": {
      "enable": true,
      "project_id": "xbot-android-push",
      "credentials_file": "~/.xbot/fcm-service-account.json",
      "mode": "data"
    },
    "webpush": {
      "enable": false,
      "vapid_public": "...",
      "vapid_private": "...",
      "subject": "mailto:you@example.com",
      "endpoint_allowlist": ["fcm.googleapis.com", "push.services.mozilla.com"]
    },
    "apns": { "enable": false },
    "feishu": { "enable": false }
  }
}
```

- `push.enable` 是总开关；某个 provider 配错只会在启动日志里告警并禁用该 provider，
  不会阻塞 server 启动。
- **秘密只从文件/环境变量读**：FCM service account、APNs `.p8`、VAPID private。
  不打印、不落库、不进仓库。
- `webpush.endpoint_allowlist` 是 **SSRF 闸门**（endpoint 由浏览器提供）；为空时
  WebPush provider 直接拒绝初始化。
- FCM `mode`：
  - `data`（默认）：服务端只发 data，app 自己弹通知（渠道/深链/前台抑制可控）；
  - `notification`：系统直接展示（OEM 更稳），app 无法定制/抑制。

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
