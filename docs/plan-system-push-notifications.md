# 计划：统一推送层（provider 无关）+ FCM 首发

> 生成时间：2026-10-22
> 状态：服务端 Phase 1 完成；service account 已就位（真实凭据 smoke test 通过）；进行 Phase 2 Android
> 范围：xbot 服务端（`~/xbot`）+ xbot-android（`~/xbot-android`）+ Web 前端
> 已确认（2026-10-22）：service account 用 `~/.xbot/fcm-service-account.json`；
> 首发 data-only；飞书/APNs 只留接口；WebPush 本轮一起做。
> 前情：2026-10-22 Firebase Console 测试消息在目标机实测成功；UnifiedPush / 厂商通道作废（见附录）。

## 摘要

后台通知不再依赖 xbot app 自建的 SSE 前台服务。服务端引入一个**统一的推送层**：

- 语义事件（`ask_user` / `turn_done` / `test`）与**投递通道**解耦；
- 每个通道实现一个 `Provider`（FCM / APNs / WebPush / 飞书 / 将来的其他 IM）；
- 设备或会话注册为 `Target{provider, platform, address, metadata}`；
- `Notifier` 负责 owner 解析、去重、限流、presence，然后按 provider 分发；
- 新增通道 = 新增一个 `Provider` + 一段配置，**不改 Notifier、不改 HTTP handler**。

首发只实现 **FCM（Android，data-only）**；APNs / WebPush / 飞书先留接口与配置位，
后续按需接入。

## 一、现状与约束

1. **FCM 可用**：目标机（OPPO/vivo 系）实测能及时收到 Console 消息。
2. **平台不止 Android**：服务端必须能同时面对 Android、iOS、Web，以及飞书这类 IM 通道。
3. **前台 SSE 保留**：app 可见时的实时流是正常 HTTP；删除的只是后台常驻连接。
4. **FCM 的边界**：用户“强行停止”app 后收不到；OEM 可能延迟 data-only ⇒ 保留
   `mode=notification` 作为回退。
5. **已有可复用资产**：
   - 飞书：`channel/feishu/feishu.go` 已有 tenant token、`Send`、卡片渲染；
   - Web：`web/src/components/SWUpdateButton.tsx` 已注册 `sw2.js` service worker；
   - 存储：SQLite + PG 双后端，新增表必须两边实现 + 契约测试。

## 二、目标架构

```
                   语义事件（与通道无关）
        ask_user / turn_done / test
                     │
                     ▼
              push.Notifier
    owner 解析 · 去重 · 限流 · presence · Target 选择
                     │
        ┌────────────┼─────────────┬──────────────┐
        ▼            ▼             ▼              ▼
   FCMProvider  APNsProvider  WebPushProvider  FeishuProvider
   (Android)     (iOS)          (Web)          (IM)
        │            │             │              │
        ▼            ▼             ▼              ▼
    FCM HTTP v1   APNs HTTP/2   WebPush(VAPID)  飞书 im/v1/messages

   Target 存储：push_targets(provider, platform, address, metadata_json)
   配置：push.providers.<name>.enable + 各自参数
```

### 核心接口

```go
// 语义事件：Provider 只负责“怎么发”，不关心业务含义。
type Event struct {
    Kind         string // ask_user | turn_done | test
    Channel      string // "web"
    ChatID       string
    SenderID     string
    Title        string
    Body         string
    RequestID    string // ask_user 去重键
    HighPriority bool
    TS           int64
}

// 投递目标：设备、浏览器订阅、IM 会话都统一成 Target。
type Target struct {
    ID         int64
    UserID     int64
    SenderID   string
    Provider   string // fcm | apns | webpush | feishu
    Platform   string // android | ios | web | im
    Address    string // FCM token / APNs token / WebPush endpoint / 飞书 chat_id
    Metadata   map[string]string // WebPush keys、设备名、环境等
    DeviceName string
    AppVersion string
    CreatedAt  time.Time
    LastSeenAt time.Time
}

type Provider interface {
    Name() string
    Platform() string
    // Validate 校验注册参数（地址格式、必填 metadata）。
    Validate(t Target) error
    // Send 投递一条事件；返回 *DeliveryError 让 Notifier 决定删目标/重试/丢弃。
    Send(ctx context.Context, t Target, ev Event) error
}

type ErrorKind int // Permanent（删目标）| Transient（重试）| Auth（刷新后重试）

type DeliveryError struct {
    Kind ErrorKind
    Err  error
}
```

`push.Registry` 按配置构造已启用的 provider；`Notifier` 只依赖 `Registry` +
`storage.PushStore`，对具体通道零感知。

## 三、服务端改动（`~/xbot`）

### 1. `config/config.go`

```go
type PushConfig struct {
    Enable         bool                    `json:"enable"`
    IncludePreview bool                    `json:"include_preview"`
    Providers      map[string]ProviderConf `json:"providers"`
}

type ProviderConf struct {
    Enable bool           `json:"enable"`
    FCM    *FCMConfig     `json:"fcm,omitempty"`
    APNs   *APNsConfig    `json:"apns,omitempty"`
    WebPush *WebPushConfig `json:"webpush,omitempty"`
    Feishu *FeishuPushConfig `json:"feishu,omitempty"`
}

type FCMConfig struct {
    ProjectID       string `json:"project_id"`
    CredentialsFile string `json:"credentials_file"`
    Mode            string `json:"mode"` // data（默认）| notification
}

type APNsConfig struct { // Phase 4
    KeyFile string `json:"key_file"` // .p8
    KeyID   string `json:"key_id"`
    TeamID  string `json:"team_id"`
    Topic   string `json:"topic"`    // bundle id
    Env     string `json:"env"`      // production | sandbox
}

type WebPushConfig struct { // Phase 4
    VAPIDPublic  string `json:"vapid_public"`
    VAPIDPrivate string `json:"vapid_private"`
    Subject      string `json:"subject"` // mailto:
}

type FeishuPushConfig struct { // Phase 3（复用 feishu.app_id/app_secret）
    Enable bool `json:"enable"`
}
```

- 环境变量：`PUSH_ENABLE`、`PUSH_FCM_*`、`PUSH_APNS_*`、`PUSH_WEBPUSH_*`。
- **秘密只从文件/环境变量读**：FCM service account、APNs .p8、VAPID private。
- 启动时逐 provider 校验配置；某个 provider 配错了只禁用它，不阻塞 server。
- 配置结构用 map，新增 provider 不改 `PushConfig` 的字段（加一个 `*XConfig` 即可）。

### 2. `storage/interfaces.go`

```go
type PushTarget struct {
    ID         int64
    UserID     int64
    SenderID   string // web-<userID>
    Provider   string
    Platform   string
    Address    string
    Metadata   map[string]string // WebPush keys、设备信息等（JSON 落库）
    DeviceName string
    AppVersion string
    CreatedAt  time.Time
    LastSeenAt time.Time
}

type PushStore interface {
    UpsertPushTarget(t *PushTarget) error
    DeletePushTarget(userID int64, provider, address string) error
    DeletePushTargetByID(id int64) error
    ListPushTargetsBySender(senderID string) ([]PushTarget, error)
    TouchPushTargets(ids []int64, at time.Time) error
}
```

- 嵌入 `ConfigStore`。
- `SessionStore` 增加 `GetLastAssistantPreview(tenantID int64, maxRunes int) (string, error)`。

### 3. 数据库（两后端 + 契约测试）

```sql
CREATE TABLE push_targets (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id      INTEGER NOT NULL,
    sender_id    TEXT    NOT NULL,
    provider     TEXT    NOT NULL,
    platform     TEXT    NOT NULL,
    address      TEXT    NOT NULL,
    metadata     TEXT    NOT NULL DEFAULT '{}',
    device_name  TEXT    NOT NULL DEFAULT '',
    app_version  TEXT    NOT NULL DEFAULT '',
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_seen_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(provider, address)
);
CREATE INDEX IF NOT EXISTS idx_push_targets_sender ON push_targets(sender_id);
```

- SQLite：`schema.go` + `db.go` `schemaVersion` 73 → 74 + `migrations.go` 的 `migrateV73ToV74`。
- PG：`pg_schema.go`（`BIGINT GENERATED ALWAYS AS IDENTITY`、`TIMESTAMPTZ`、`metadata JSONB`）。
- 适配器：`storage/sqlite_adapter.go`、`storage/pg_push.go`。
- 契约测试：`storage/contract_push_test.go`（两后端同套断言，含 metadata 往返）。

### 4. 新包 `push/`

| 文件 | 内容 |
|---|---|
| `push/event.go` | `Event`、`Target`、`ErrorKind`、`DeliveryError` |
| `push/provider.go` | `Provider` 接口、`Registry` |
| `push/notifier.go` | 队列、worker、owner 解析、去重、限流、presence、Target 选择 |
| `push/fcm.go` | FCM HTTP v1：service account → JWT → token 缓存 → 发送 |
| `push/feishu.go` | 复用 `FeishuSender`（由 serverapp 注入），发送文本/卡片 |
| `push/apns.go` | Phase 4 占位：APNs HTTP/2 + p8 JWT |
| `push/webpush.go` | Phase 4 占位：VAPID + aes128gcm |
| `push/*_test.go` | `httptest` 假端点：FCM/APNs/WebPush |

FCM provider 要点：

- **鉴权**：标准库手写 JWT（RS256），scope `https://www.googleapis.com/auth/firebase.messaging`，
  POST `token_uri` 换 token；缓存到过期前 5 分钟；401 刷新重试一次。
- **发送**：`POST https://fcm.googleapis.com/v1/projects/{project_id}/messages:send`
  ```json
  { "message": {
      "token": "<address>",
      "data": { "kind": "ask_user", "chat_id": "...", "title": "...",
                "body": "...", "request_id": "...", "ts": "..." },
      "android": { "priority": "HIGH", "ttl": "3600s",
                   "collapse_key": "ask:<request_id>" }
  } }
  ```
  `mode=notification` 时加 `notification` + `android.notification.channel_id=xbot_messages`。
- **错误**：404/`UNREGISTERED`/`INVALID_ARGUMENT` → `Permanent`；401 → `Auth`；
  429/5xx → `Transient`；其余 4xx → `Permanent`（记录原因）。
- **超时**：5s；默认 data-only。

Feishu provider 要点：

- 复用 `channel/feishu` 的 client 与 tenant token，不重复实现鉴权；
- 通过注入的窄接口 `FeishuSender interface { SendText(ctx, chatID, title, body string) error }`
  调用（serverapp 用现有 `Dispatcher` 或 Feishu client 实现），避免 `push → channel` 反向依赖；
- Target：`provider=feishu`、`platform=im`、`address=<chat_id/open_id>`；
- 可复用 `feishu_bind.go` 的绑定关系自动注册目标（Phase 3）。

Notifier 要点：

- `Notify(ev)` 只入有界队列（256），worker 异步发送；队列满丢弃并计数。
- owner：`ev.SenderID` 为空时用 `TenantStore.GetSenderForChat(channel, chatID)`；
  解析不到就丢弃（不群发）。
- Target 选择：按 `sender_id` 列出全部目标；按 provider 分组；并发发送（每 provider 一个信号量）。
- 去重：`ask_user` 按 `request_id`（10 分钟）；`turn_done` 按 `chatID + turnID`（2 分钟）。
- 限流：每 Target 10 条/分钟。
- presence：`map["provider:address"]expiry`，app/客户端每 30s 续租 90s；命中跳过。
- Provider 返回 `Permanent` → 删除 Target；`Auth` → 让 provider 刷新后重试一次；
  `Transient` → 退避重试（1s/2s/4s）。

### 5. `channel/web/web.go`

1. `WebCallbacks` 增加 `PushNotify func(push.Event)`（非阻塞入队；nil = 未启用）。
2. `SendProgress` 的 `WaitingUser` 分支：pending 匹配时调用 `PushNotify`（`Kind=ask_user`）。
3. `SendSessionState`：仅 `ev.Channel == "web"` 时调用 `PushNotify`（`Kind=turn_done`
   在 Notifier 里由 `busy → idle` 触发）。
4. `newServeMux` 增加（全部 `authenticatedPOST`）：
   - `POST /api/push/register` — `{provider, platform, address, metadata, device_name, app_version}`
   - `POST /api/push/unregister` — `{provider, address}`
   - `POST /api/push/presence` — `{provider, address, chat_id, active}`
   - `POST /api/push/test` — `{provider?}`，只发调用者自己的 targets
5. handler 只依赖 `Registry` + `PushStore`，**不出现任何 provider 名字的分支**；
   校验全部走 `provider.Validate`。

### 6. `serverapp/server.go` / `serverapp/callbacks.go`

- 构造 `push.Registry`（按配置启用 FCM/Feishu/…），FCM 传 service account 路径；
- 构造 `push.Notifier`，注入 `PushStore` + `Registry` + `FeishuSender`；
- `WebCallbacks.PushNotify = notifier.Notify`；
- 关闭时 `notifier.Close()`（停 worker，等在途请求最多 3s）；
- 启动日志打印已启用的 provider（不打印任何 token/私钥）。

### 7. 文档

- `docs/agent/channel.md`：`PushNotify` 契约；
- `docs/agent/push.md`（新）：Provider 接口、Target 模型、如何新增一个通道；
- `docs-site` 配置参考：`push.providers.*` + 各 provider 的密钥部署说明（中英双语）。

## 四、客户端改动（按平台）

### Android（本仓库，Phase 2）

- `FcmSpikeService` → 正式 `push/XbotMessagingService.kt`：
  - `onNewToken` / `onMessageReceived`；data-only 解析；
  - 通知渠道 `xbot_messages`、图标、深链、`ask_user` 高优先级、同会话覆盖。
- `push/PushRegistrar.kt`：登录后注册、`onNewToken` 重注册、退出注销。
- `XbotApi`：`pushRegister/pushUnregister/pushPresence/pushTest`
  （统一走 generic 的 provider/platform/address 参数）。
- presence：`ProcessLifecycleOwner`，前台 30s 心跳、后台立即置 false。
- Phase 3 删除 `ChatNotificationService`、`BootReceiver`、常驻通知与相关权限。
- 前台 SSE 保留（`ChatViewModel`）。

### iOS（将来）

- APNs token 注册（同一 `/api/push/register`，`provider=apns, platform=ios`）；
- 服务端 `APNsProvider` 已留位；客户端不在本仓库。

### Web（将来，Phase 4）

- `sw2.js` 增加 `push` 事件处理；
- 订阅 WebPush（`PushManager.subscribe`，VAPID public key 从服务端拿）；
- 注册 `{provider:"webpush", platform:"web", address:endpoint, metadata:{keys}}`；
- 服务端 `WebPushProvider`：VAPID + aes128gcm（用 `webpush-go`，或自实现）。

### 飞书（Phase 3）

- 不需要新 app；服务端把已有的飞书绑定（`feishu_bind.go`）注册成
  `provider=feishu, platform=im, address=<chat_id>`；
- 通知以文本/卡片形式发到飞书会话。

## 五、事件与消息格式（按 provider 渲染）

| 事件 | 语义 | FCM | APNs | WebPush | 飞书 |
|---|---|---|---|---|---|
| `ask_user` | 需要用户回答 | data-only，HIGH | alert + `thread-id` | JSON + `Topic` | 文本/卡片 |
| `turn_done` | 回合完成 | data-only，NORMAL | alert | JSON | 文本 |
| `test` | 手动测试 | data-only，HIGH | alert | JSON | 文本 |

- 所有 provider 的正文上限按最小公倍数控制：**标题 ≤ 32 字、正文 ≤ 120 字**。
- 折叠：FCM `collapse_key`、APNs `apns-collapse-id`、WebPush `Topic`。
- 隐私：`include_preview=false` 时所有通道只发“回复完成”。

## 六、安全

- **秘密**：FCM service account、APNs .p8、VAPID private 只从文件/环境变量读；
  `chmod 600`；不打印、不落库、不进仓库。
- **身份**：注册/注销/测试都要求登录态；`sender_id` 由 cookie 推导；
  注销只能删自己的 target。
- **输入校验**：`provider.Validate` 校验地址格式；不向客户端提供的 URL 发请求
  （FCM/APNs/VAPID 都是固定端点；WebPush 的 endpoint 由浏览器给出，
  需要域名 allowlist 或仅允许 https + 已知推送服务域名——Phase 4 再定）。
- **预览**：可关；锁屏可见性由用户控制。

## 七、可观测性

- 日志：`provider`、`platform`、`kind`、`chat_id`、`target_id`、`status`、`latency_ms`；
- 计数：`push_sent_total{provider}`、`push_failed_total{provider}`、
  `push_dropped_total{reason}`；
- `POST /api/push/test` 返回每个 target 的结果（provider/状态），设置页可一键触发；
- 推送失败只记日志，绝不冒泡到 agent 循环。

## 八、测试

### Go

1. `push/fcm_test.go`：JWT 断言、token 缓存、发送体、401 刷新、404 删除、429 重试。
2. `push/notifier_test.go`：去重、presence、owner 解析失败不发送、按 provider 分组、
   Permanent 删 target、Transient 重试。
3. `push/feishu_test.go`：注入 fake `FeishuSender`，断言文本/卡片与地址。
4. `channel/web` handler：注册绑定 `web-<userID>`；注销隔离；`/api/push/test` 只发自己的。
5. `storage/contract_push_test.go`：SQLite + PG 的 `PushStore` 契约 + metadata 往返。

### Android

- payload 解析、深链、通知 id、通知渠道；
- 注册/注销/onNewToken；
- 真机：前台不重复、后台 5s 内、划掉进程仍收到、强行停止预期收不到、锁屏点击进会话。

## 九、分阶段

| 阶段 | 内容 | 验证 |
|---|---|---|
| Phase 0 | service account 放到服务器（**当前缺失**）；确认 `~/.xbot/fcm-service-account.json` 600 | 能解析 |
| Phase 1 | 服务端：config + storage + `push` 包（FCM + WebPush）+ 通用端点 + `test` | `curl` 测试消息到手机 |
| Phase 2 | Android：正式 receiver + 注册 + presence + 设置页；旧前台服务可切换 | 后台/杀进程收到 ask_user 与完成通知 |
| Phase 3 | Web：`sw2.js` push 处理 + WebPush 订阅 + 注册；Web 端收通知 | 浏览器关页面后仍能收到 |
| Phase 4 | 飞书 / APNs 只留接口与配置位（不实现）；删除 Android 前台服务 | 通知栏无常驻项 |

## 十、完成标准（DoD）

- [ ] `push.Provider` / `Registry` / `Target` 落地；Notifier 与 handler 对具体通道零感知；
- [ ] FCM：登录后自动注册；`onNewToken`/换账号/退出正确更新；`/api/push/test` 可达；
- [ ] 真实回合：`ask_user` 在后台/被杀时 5s 内到达，点击直达会话；
- [ ] 完成通知带 120 字预览，同一回合不重复；
- [ ] 前台 presence 抑制生效；
- [ ] `mode=data` 与 `mode=notification` 在目标机各验证一次并记录；
- [ ] 删除前台服务后通知栏无常驻“正在保持连接”；
- [ ] service account / APNs key / VAPID private 均不在仓库；
- [ ] SQLite + PG 契约测试通过；`go build ./...` / `go test ./...` / Android
      `assembleRelease` + 单测全绿；
- [ ] `docs/agent/push.md` 与 `docs-site` 配置说明同步（中英双语）。

## 十一、待确认

1. ~~service account 路径~~ **已确认**：`~/.xbot/fcm-service-account.json`（600）。
   **当前文件还未放**，Phase 1 代码可以先写，真实发送测试等文件就位。
2. ~~飞书 provider~~ **已确认**：只留接口与配置位，本轮不实现。
3. ~~Web / iOS~~ **已确认**：WebPush 本轮实现；APNs 只留接口。
4. **过渡期**：Phase 2 是否保留「实时模式（前台服务）」开关一个版本？
5. **预览默认值**：完成通知默认带回复预览（可能出现在锁屏），还是默认只显示“回复完成”？

---

## 附录：决策记录

| 通道 | 结论 | 原因 |
|---|---|---|
| **FCM（Android）** | **首发** | 2026-10-22 实测可用；真系统级；无需企业资质 |
| APNs / WebPush | 留接口，Phase 4 | 当前没有 iOS/Web 客户端需求，但架构必须支持 |
| 飞书 | 留接口，Phase 3 | 已有 Feishu channel/绑定，可复用；作为 IM 通道 |
| UnifiedPush / ntfy | 放弃 | 本质仍是长连接，只是换到 ntfy；多一个服务端 + 分发器 |
| OPPO/vivo/一加/realme 厂商通道 | 放弃 | 个人开发者不支持（需营业执照）+ 必须上架商店；非商店安装关闭公信/运营消息 |
| 自建常驻前台服务 | 放弃（保留前台 SSE） | 24/7 常驻通知、耗电、被 OEM 杀 |

厂商通道的详细步骤与限制（2026-10 核对）：

- **OPPO PUSH（一加/realme 同用）**：ColorOS 3.1+、一加 5/5T+、realme 全系；
  需企业开发者、上架商店；2024-07-29 起非商店安装不再提供公信消息（只能私信/测试）；
  公信消息非新闻类 2 条/天，标题 ≤32 字、正文 ≤200 字。
- **vivo PUSH**：推送审核以应用已上架为前提；2025-02-17 起非官方渠道安装默认关闭
  运营消息；运营消息 7:00–23:00；非新闻类 2 条/天、新闻类 5 条/天。
- 将来有企业主体并上架商店，可按 `push.Provider` 新增 provider，不改 Notifier。
