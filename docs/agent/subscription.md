# Subscription & LLM Resolution

## Overview

xbot 的 LLM 配置分为 3 层：全局默认 → 用户级别订阅 → 会话级别覆盖。
每个会话最终使用哪组 LLM 凭据/模型/参数，由 `LLMFactory` 运行时决定。

## Model-First Redesign (v39, authoritative)

> 本节描述 **当前权威路径**。下方 "LLM Resolution" 里的 `GetLLMForChat` 等遗留入口仍存在，但 agent loop 已切换到 `ResolveLLM`，新代码应优先使用本节 API。

设计原则：**model 是一等实体，subscription 是模型的凭据来源**。agent 只关心 model 层；订阅只提供凭据和 per-model 配置。模型可被禁用，订阅也可被整体禁用（v40）。

**UI 后果**：TUI 不再有"切换订阅"动作，也不再有独立的订阅面板/Ctrl+P/Ctrl+N。**单一 LLM 面板**（Ctrl+N）是唯一入口，模型切换**跨订阅**——选中属于别的订阅的模型时，后端 `ResolveSubscriptionForModel` 自动解析 owner 订阅并配对凭据。面板内订阅行只支持 **添加 / 禁用 / 删除 / 编辑凭证**，不支持切换。

### 新增 DB（v39 迁移，rebase 后与 master 的 v38 runner_id 并存）

- `subscription_models.enabled INTEGER NOT NULL DEFAULT 1` — 模型禁用开关。禁用后该模型不出现在轮换/选择器，`SelectModel` 拒绝选中。
- `user_default_model(sender_id PK, subscription_id, model, updated_at)` — 用户级默认 (订阅, 模型)，用于新会话解析，取代旧的 `user_llm_subscriptions.model` "当前模型" 隐式语义。
- v39 迁移幂等：补建 `enabled` 列、`user_default_model` 表，从 tenants 引用回填具体 model 行，从默认订阅 seed `user_default_model`。

### 新增 DB（v40 迁移）

- `user_llm_subscriptions.enabled INTEGER NOT NULL DEFAULT 1` — **订阅级禁用开关**。禁用后该订阅不再向模型选择池贡献任何模型（`ListAllModelsForUser` / `ResolveSubscriptionForModel` 跳过，`SelectModel` 拒绝），但凭据和 per-model 配置保留，重新启用无损。v40 迁移幂等（`ALTER TABLE ... ADD COLUMN enabled ...`，缺失才加）。

### 表瘦身（v41–v43 迁移）

移除冗余/死表，单一数据源收敛：

- **v41**：`DROP TABLE user_llm_configs`。该表自 v24 起即为死表（数据早已迁入 `user_llm_subscriptions`），无任何活跃代码读写。
- **v42**：`DROP COLUMN user_llm_subscriptions.per_model_configs`。逐模型配置统一由 `subscription_models` 表承载（v35+ 权威源）。`LLMSubscription.PerModelConfigs` Go 字段保留为**读侧投射**，由 storage 层 `loadPerModelConfigs` 在 `List`/`Get`/`GetDefault` 时从 `subscription_models` 表填充；`Add` 时把传入 map upsert 进表。`UpdatePerModelConfigs` 改为 delete+reinsert 表行。`mergeSubscriptionModels`（rpc_table）已删除——填充下沉到 storage。
- **v43**：`DROP COLUMN user_llm_subscriptions.is_default`。默认订阅由 `user_default_model` 派生（v39 seed）。`LLMSubscription.IsDefault` / `channel.Subscription.Active` 保留为**读侧投射**：`GetDefault` 标记命中订阅；`List`/`ListAll` 通过 `markDefaultsFor`/`markDefaultsAll` 标记。`SetDefault(id)` 改为 upsert `user_default_model`。`Add`/`Update` 在 `IsDefault=true` 时同步 `user_default_model`。
- **代码层**：删除 `UserLLMConfigService`（旧 shim，平行写路径）。`NewLLMFactory` 不再接收 `configSvc`。agent 的 `GetUserMaxContext`/`SetUserMaxContext`/`GetUserMaxOutputTokens`/`SetUserMaxOutputTokens`/`SetUserModel` 改走 `subscriptionSvc`（默认订阅 + `subscription_models`）。`GetUserThinkingMode`/`SetUserThinkingMode` **例外**：改走 `settingsSvc`（全局用户设置，见「ThinkingMode 全局开关」），不再写订阅行。`HasCustomLLM` 移除 configSvc 读路径。`GetUserLLMConfig`/`SetUserLLM`/`DeleteUserLLM` 死方法删除。`user_llm_config.go` 仅保留 `UserLLMConfig` 结构体（client 构造/`/set-llm` 解析用的字段袋）。

### 系统订阅：单源 LLM（v44 迁移）

把"config 种子 + DB 覆盖 + 运行时 SwitchSubscription 同步 defaultLLM"三段舞收敛为**DB 单源**：全局默认 LLM 作为一条共享**系统订阅** DB 记录，启动时从 `config.json llm`/env reconcile 一次。

- **v44**：`ALTER TABLE user_llm_subscriptions ADD COLUMN is_system INTEGER NOT NULL DEFAULT 0`。系统订阅 `sender_id="__system__"`、`is_system=1`、`name="system"`、`id="system"`。
- **启动 reconcile**（`serverapp.reconcileSystemSubscription`）：每次启动用 `cfg.LLM`（含 env 覆盖）upsert 系统订阅的凭证/配置字段（`provider/base_url/api_key/model/max_output_tokens/thinking_mode`），`cached_models` 与 `subscription_models` 保留。`cli_user` 无默认时把系统订阅设为其默认（首跑）；已有用户默认不覆盖。`xbot-cli serve` 与 `xbot` 共用 `serverapp.Run`，同步逻辑一处覆盖。
- **LLMFactory**：`SetSystemLLM(client, model)` 用系统订阅行构建 `defaultLLM` 兜底（不清空 per-user 缓存，区别于 `SetDefaults`）。picker 里系统模型不再走 `defaultLLM.ListModels()` 伪条目，而是经系统订阅行的标准订阅-owned 路径，`SubName="system"`。`GetDefault` 用户无默认时回退系统订阅。
- **可见性与只读**：`List(senderID)`/`ListAll` 注入系统订阅行（`is_system=1`），所有用户可见。`Remove`/`SetSubscriptionEnabled`/`Rename`/`Update` 对 `is_system=1` 行拒绝（storage 层守卫 + UI `subIsSystem` 守卫 + 🔒 标记）。系统订阅下的模型正常可选（跨订阅切模型不变）。
- **config 写回收敛**：`saveServerConfig`/`saveCLIConfig` 不再把 `cfg.LLM` 凭证写回 config.json（DB 单源，且 `cfg.LLM` 启动后可能含从 DB 解密的明文 key，回写会泄漏）。config.json 仅保留 tier 模型（vanguard/balance/swift）和原有凭证（SaveToFile 深合并保留）作为系统订阅的启动种子。
- **协议**：`protocol.Subscription` / `channel.Subscription` 新增 `IsSystem bool`，`subToChannel` 填充，UI 据此渲染只读标记。

### 新增 LLMFactory API

| 方法 | 作用 |
|------|------|
| `ResolveLLM(senderID, chatID, channel)` | **权威解析入口**（agent loop 用）。返回 (client, model, maxContext, thinkingMode, maxOutputTokens) |
| `SelectModel(senderID, chatID, channel, subID, model)` | per-session 选 (订阅, 模型)，校验 enabled，写 tenants 表 |
| `SetUserDefaultModel(senderID, subID, model)` | 用户级默认 (订阅, 模型)，写 `user_default_model` |
| `SetModelEnabled(subID, model, enabled)` | 切换模型禁用状态，失效该订阅缓存 |
| `SetSubscriptionEnabled(subID, enabled)` | **v40** 切换订阅级禁用，失效该订阅缓存（禁用订阅不贡献模型） |
| `ListAllModelEntriesForUser(senderID)` | 返回 `[]protocol.ModelEntry{SubID,SubName,Model,Status}`，**DB 驱动**（列 `sub.CachedModels` ∪ `sub.Model` ∪ `subscription_models` 行的全部记录）。`Status`：`normal`（已拉取或是 sub.Model 且启用）/`offline`（有记录但未拉取，启用，可手动添加）/`disabled`（`subscription_models.enabled=0`）。包含全部三种供选择器渲染；`ListAllModelsForUser` 是其 `Status!="disabled"`（normal+offline，即可选）子集的薄包装 |
| `RefreshModelEntriesForUser(senderID)` | 并行拉每个启用订阅的 `/models` → 经 `OnModelsLoaded` 落 `CachedModels` → 返回最新 `[]ModelEntry`。失败软降级。供选择器开面板刷新 |
| `makeOnModelsLoaded(subID)` | 构造 `OnModelsLoaded` 回调：`Get(subID)` nil-check 后 `UpdateCachedModels`。在 `createClientFromSub` 及 entry 构造路径注入，修复 model-first 重构丢失的持久化 |
| `ResolveSubscriptionForModel(senderID, model)` | **模型→订阅反向解析**：找提供该模型的订阅（enabled 行优先 → CachedModels/sub.Model；默认订阅优先；跳过 disabled 订阅与 disabled 模型） |
| `PickDefaultModelForSub(sub)` | 订阅无 `Model` 时从 `subscription_models`/`CachedModels` 选一个真实模型，避免 `f.defaultModel` 污染 |

### ResolveLLM 解析链（优先级从高到低）

```
1. ProxyLLM（runner local LLM）命中 → 直接返回（sync.Map，运行时注入）
2. tenantSvc.GetTenantSubscription(channel, chatID) → tenants 表的 (subID, model)
3. subscriptionSvc.GetUserDefaultModel(senderID) → user_default_model 的 (subID, model)
4. fallback → GetLLM(senderID)（用户默认订阅，再退到 defaultLLM 系统默认）
```

**DB-direct，无内存缓存**：`sessionMemo`/`entries`/`hasCustomLLMCache`/`perChatMaxCtx` 已全部移除。`ResolveLLM` 每次调用都从 DB 读取，设置变更即时生效，无需任何失效逻辑。`Invalidate`/`InvalidateSender`/`InvalidateSession` 是 no-op，`InvalidateAll` 只清 `clientCache`（HTTP 连接池复用）。`GetLLMForChat` 从 tenants 表读 per-session (subID, model)，fallback 到 `GetLLM`；`SetSessionLLM` 通过 `tenantSvc.SetTenantSubscription` 写 DB。`HasCustomLLM` 直接查 `subscriptionSvc.List(senderID)` + 检查 `proxyLLMs`。

### 新增 RPC

| RPC | 说明 |
|-----|------|
| `select_model` | per-session (subID, model)，走 `SelectModel`。需 chatID（用户级用 `set_default_model`），并且必须传目标 `channel`；`/su` 到 Feishu/Web/Agent 等非 CLI 会话时，模型绑定写入 `(channel, chatID)`，不能默认写 `cli:<chatID>` |
| `set_default_model` | 用户级默认 (subID, model)，走 `SetUserDefaultModel` |
| `set_model_enabled` | 切换模型 enabled，走 `SetModelEnabled` |
| `set_subscription_enabled` | **v40** 切换订阅 enabled，走 `SetSubscriptionEnabled` |
| `list_all_model_entries` | 返回 `[]{SubID,SubName,Model}`，模型选择器权威数据源（带 owner 订阅名） |
| `refresh_model_entries` | 并行拉每个启用订阅 `/models` 落 `CachedModels`，返回最新 entries（选择器开面板触发） |

`client.SelectModel` / `client.SetDefaultModel` / `client.SetModelEnabled` / `client.SetSubscriptionEnabled` 对应客户端方法。`SubscriptionManager.SetModelEnabled` / `SetSubscriptionEnabled` 已加入 CLI 接口。

### 跨订阅切模型 404（已修复）

**根因**：旧 `switch_model` per-session 分支用 `GetDefault()` 拿默认订阅，把 `(默认订阅ID, 任意模型名)` 写进 tenants 表。当用户 Ctrl+N 切到属于**别的订阅**的模型时，`ResolveLLM` 读到 `(默认订阅, 别的模型名)` → 用默认订阅的 baseURL/apiKey 构造 client，却请求别的模型名 → 404 "model not supported by any configured account"。

**修复**：`switch_model` per-session 分支先 `ResolveSubscriptionForModel` 解析拥有该模型的订阅，再走 `SelectModel(owner.ID, model, chatID)`（含 enabled 校验 + memo 失效）。解析失败才回退旧默认订阅路径。

### 禁用 UX（模型级 + 订阅级）

- `ListAllModelsForUser` 聚合 `subscription_models` 行 + `CachedModels` + `sub.Model`，并**排除被禁用的模型**（仅当所有列出它的订阅都禁用时排除），且**整体跳过 `enabled=0` 的订阅**（禁用订阅不贡献任何模型）。tier 选择器、idle placeholder 自动不再出现禁用模型/禁用订阅的模型。
- `protocol.PerModelConfig.Enabled` 是读侧投射字段，`mergeSubscriptionModels` 把 `subscription_models.enabled` 透传到客户端，供 UI 显示。`protocol.Subscription.Enabled`（v40）由 `subToChannel` / `LLMGetSubscription` 从 `user_llm_subscriptions.enabled` 透传。
- 订阅编辑面板（`editQuickSwitchEntry`）每个模型行有 Enabled/Disabled 下拉，保存时按差异调 `SetModelEnabled`。
- 订阅管理面板（`cli_panel_quickswitch.go`，"subscription" 模式）**不再有切换动作**：Enter = 启用/禁用该订阅（调 `SetSubscriptionEnabled`，面板保持打开以便连续管理），E = 编辑，D = 删除，末尾 `➕ Add subscription`。删除当前活跃订阅会被拒绝（提示先切模型）。
- **统一 LLM 面板**（`Ctrl+N` / 点状态栏模型名 / palette "Models & Subscriptions"）：单一面板，把订阅 + 模型 + 添加动作合并为一个扁平 `[]qsRow` 列表（`qsSection`/`qsSub`/`qsModel`/`qsAddSub`/`qsAddModel`）。**不再有独立的订阅面板，也不再有 `Ctrl+P`/`Ctrl+N`**。模型行用 `ListAllModelEntries()` 返回 `[]{SubID, SubName, Model, Status}`（权威：服务端 `ListAllModelEntriesForUser`，**DB 驱动**——列 `sub.CachedModels` ∪ `sub.Model` ∪ `subscription_models` 行的全部记录），列表项显示 **`订阅名 · 模型名`** + 状态标签（`normal` 无标 / `offline` 置灰 `(offline)` / `disabled` 置灰 `(disabled)`）。订阅行只做管理（添加/禁用/删除），模型行跨订阅切换。
  - **键位**（命令模式，无过滤时）：`↑↓` 导航（跳过 section 行），`Enter` = 行动作（订阅→启停，模型→切换，添加行→开添加面板），`E` 编辑当前行（订阅凭证 / 模型参数），`D` 禁用或删除（模型→启停，订阅→删除），`N` 添加模型（按光标行预填 owner 订阅），`S` 切换 `quickSwitchShowAll`，`/` 进入过滤模式，`Esc` 关闭。**`/` 切换过滤模式**使命令字母 `e`/`d`/`n`/`s` 不与打字冲突——取代旧的 Ctrl 组合命令（`Ctrl+E`/`Ctrl+A`/`Ctrl+T`，它们与全局 `Ctrl+E` 折叠、`Ctrl+T` 会话冲突）。
  - **`E` 编辑模型参数**：在模型行按 `E` → `openEditModelPanel` 打开 mini 面板编辑 `max_context`/`max_output`/`api_type`/`enabled`，提交时 `UpdatePerModelConfig`（走 `UpsertModel`，**只增不减**——以 (订阅,模型) 为键 INSERT OR REPLACE，禁用即 enabled=0，永不 DELETE）+ `SetModelEnabled`（仅当 enabled 状态变更）。保存后 `reopenLLMPanelOn(model)` 用 DB 快照重开面板，状态标签即时刷新。
  - **`N` 添加模型**：按 `N` → `openAddModelPanel` 打开 mini 面板：选择启用订阅 + 输入模型名 + 可选 `max_context`/`max_output`/`api_type`，提交 `UpdatePerModelConfig`/`UpsertModel`（只增）。新模型以 `offline` 出现（直到 provider `/models` 列出它），立即可选。
  - **`S` 显示全部**：切换 `quickSwitchShowAll`，默认用 `isNoiseModel` 过滤掉噪声模型（image/realtime/whisper/tts/audio/embed/moderation/dated-snapshot 如 `gpt-5.2-2025-12-11`），`S` 显示全部。
  - **`applyQuickSwitch`** 从 `quickSwitchRows[cursor]` 取当前行：模型行调 `applyModelSwitch(model, subID)`（**拒绝 `disabled`**，保持面板开放提示按 `E` 复启；`normal`/`offline` 都可选）。`subID` 非空（picker 行携带 owner 订阅）时走 `SelectModel(senderID, subID, model, chatID)` **直接钉住该订阅**——同一模型名被多个订阅提供时（如 `system · deepseek-v4-pro` 与 `deepseek · deepseek-v4-pro`），用户点哪行就切到哪个订阅；`SelectModel` 失败（订阅在渲染与点击间被禁用/删除）则回退 `SwitchModel`（按模型名服务端解析 owner）。`subID` 为空（`subscriptionSvc==nil` 的裸系统默认条目）时直接 `SwitchModel`。`applyModelSwitch` 在切换之后用 `GetSessionSubscription` 回读 owner 订阅，修正 `activeSubID`/上下文上限/输出上限并持久化（local 模式同样走 RPC，tenants 表是 source of truth）。
  - **状态栏**显示 `订阅名 · 模型名`（窄屏回退为只显示模型名），由 `cachedSubName` 缓存（`refreshCachedSubName` 在 `activeSubID` 变更路径上刷新：`applyModelSwitch` / `refreshCachedModelName`(defer) / `applySessionLLMState`，每次一次 `List("")` RPC，View() 只读缓存，非每帧）。
  - **开面板即时刷新**：`openLLMPanel` 先用 DB 快照渲染，同时后台调 `RefreshModelEntries()` → RPC `refresh_model_entries` → `LLMFactory.RefreshModelEntriesForUser`：并行（并发上限 8、每订阅 8s 超时、失败软降级保留旧 `CachedModels`）对每个启用订阅拉 `/models`，经 `OnModelsLoaded` 回调落 `CachedModels`，返回最新 entries（拉取到的模型 `offline`→`normal`）。CLI 收到 `cliModelEntriesRefreshedMsg` 后 `rebuildLLMRows`（保留当前过滤文本与光标位置，越界则夹紧），面板顶部显示 `↻ 刷新模型列表…`。不同 provider 的 `/models` 列表本就不同（按订阅类型各异），未列出的模型可用 `N` 手动添加（显示为 `offline`）。
- **订阅是订阅，模型是模型**（订阅面板只改凭证）：`openEditSubscriptionPanel` / `openAddSubscriptionPanel` / `addSubscriptionSchema` 只收集 `name/provider/base_url/api_key`——`sub_model`/`sub_max_output_tokens`/`sub_thinking_mode` 已从面板移除。订阅行不再显示 `sub.Model` 列，过滤只按订阅名匹配。`/set-llm` 提示（`setLLMCmdForSub`）也只展示凭证参数（`provider/base_url/api_key`），不再带 model/max_context/max_output/thinking_mode。订阅内部仍保留 `Model`/`MaxOutputTokens`/`ThinkingMode` 字段作为兜底（`Update` 时原样回写），但 UI 不再编辑它们。模型来自 `/models` 拉取 + 模型行 `N` 手动添加；`max_output` 在模型行 `E` 编辑；`thinking_mode` 是全局开关（见下）。

## Key Files

| File | Role |
|------|------|
| `agent/llm_factory.go` | LLM 客户端缓存、订阅解析、模型切换 |
| `serverapp/rpc_table.go` | `setDefaultSubscription` / `setSubscriptionModel` 等 RPC handler |
| `serverapp/callbacks.go` | `LLMSetDefaultSubscription` 等 Backend callback |
| `serverapp/server.go` | 启动 reconcile 系统订阅 + 从 DB 同步 defaultLLM |
| `channel/cli_session.go` | `SessionLLMState` — TUI 端 per-session LLM 状态持久化 |
| `channel/cli_settings.go` | TUI settings 面板读取/写入订阅配置 |
| `channel/cli_update_handlers.go` | `handleSwitchLLMDoneMsg` — TUI 订阅切换完成回调 |
| `channel/cli_panel.go` | 订阅选择面板 UI |
| `agent/engine_wire.go` | `buildMainRunConfig` / `buildSubAgentRunConfig` — 把 LLM 注入到 agent 运行配置 |
| `storage/sqlite/user_llm_subscription.go` | DB 订阅模型（`LLMSubscription` / `PerModelConfig`） |

## Data Model

### LLMSubscription（DB 表 `user_llm_subscriptions`）

```
ID              string    — 唯一标识
SenderID        string    — 所属用户 ("cli_user" / feishu open_id)
Name            string    — 显示名
Provider        string    — "openai" | "anthropic" 等
BaseURL         string    — API endpoint
APIKey          string    — API 密钥
Model           string    — 默认模型名
MaxOutputTokens int       — 默认 max_output_tokens
ThinkingMode    string    — 兜底字段（UI 不再编辑）；生效 thinking 由全局用户设置决定（见「ThinkingMode 全局开关」）
IsDefault       bool      — 读侧投射：由 user_default_model 派生（GetDefault/List 标记，无 DB 列，v43）
IsSystem        bool      — 共享系统订阅标记（v44，DB `is_system` 列）：sender_id="__system__"，只读兜底，启动从 config/env reconcile
PerModelConfigs map       — 读侧投射：由 subscription_models 表填充（loadPerModelConfigs，无 DB JSON 列，v42）
```

### PerModelConfig

每个订阅可以为不同模型设置不同参数：

```
MaxContext       int  — max_context_tokens（上下文窗口）
MaxOutputTokens  int  — max_output_tokens（单次输出上限）
APIType          string — "" (用订阅默认) | "responses"
Enabled          bool — 读侧投射自 subscription_models.enabled（仅 UI 用，写入走 set_model_enabled RPC）
```

查找键：`PerModelConfigs[modelName]`。`mergeSubscriptionModels` 把 `subscription_models` 表行（v35+ 权威源）合并进此 map。

### ThinkingMode 全局开关（订阅是订阅，模型是模型）

`thinking_mode` 不再是订阅级字段，而是**全局用户设置**（`ScopeUser`，存 `user_settings` 表）。一处值对所有订阅/模型生效，UI 入口：

- **主聊天状态栏指示器**：`renderReadyStatus` 追加 `🧠 auto|on|off [Ctrl+M]`，带独立点击区（`thinkingZoneXStart/End`，`cli_mouse.go` 跟踪 `"thinkingMode"` zone）。
- **`Ctrl+M` 快捷键**（主聊天模式，`cli_update_turn.go`）：`toggleThinkingMode` 循环 `""(auto) → enabled → disabled → ""`。
- **`/settings` Select**：`cli_settings.go` 读写走 `settingsSvc`（用户作用域），不再读 `sub.ThinkingMode`。`saveSettings` 对 `thinking_mode` 始终持久化（即使空值 = auto），以便从 `enabled` 切回 `auto` 能清掉旧行。
- **Feishu `/set-llm thinking_mode=X`**：`Agent.SetUserThinkingMode` / `GetUserThinkingMode` 改走 `settingsSvc`（写/读全局用户设置），不再写订阅行。

**存储 channel**：`channel.ThinkingModeChannel = "cli"`（规范 channel，定义在 `channel` 包避免 `channel/cli ↔ agent` 循环引用）。CLI 设置面板、`Ctrl+M`、Feishu get/set、`ResolveLLM` 全部用此 channel，保证跨渠道同一用户只有一个 thinking 值。

**解析优先级**（`ResolveLLM`）：逐模型 override（`pmc.thinkingMode`，程序化、非用户编辑）→ 全局用户设置（`f.userThinkingMode(senderID)` 读 `user_settings["cli"][senderID]["thinking_mode"]`）→ `""`(auto)。`sub.ThinkingMode` **不再被读取**（仅作内部兜底字段保留）。

**失效**：`thinking_mode` 在 `SettingHandlerRegistry` 注册了 handler，`ApplyAgent` 调 `f.InvalidateSender(senderID)`（现已 no-op——无内存缓存需要清理，DB 每次读取即时生效）。`Agent.SetUserThinkingMode` 同样调 `InvalidateSender`。

**启动 seeding**（`serverapp`）：首跑若 `user_settings` 无 `thinking_mode` 行，从 `cfg.LLM.ThinkingMode` → 默认订阅 `ThinkingMode` → `""` 顺序 seed 一次。`thinking_mode` 已从订阅级 stale-key 清理列表移除（它是合法的用户设置行）。

**CLI `ApplySettings` 解耦**：`cmd/xbot-cli/main.go` 的 `ApplySettings` 把 `thinking_mode` 从 `llmFieldChanged` 移除，`updateActiveSubscription` 不再写 `sub.ThinkingMode`——切换 thinking 不会触发订阅 Update，只走 `ApplyRuntimeSettings`（→ handler → `InvalidateSender`）。

### subscription_models 表（v35+，per-model 权威源）

每行一个 (订阅, 模型)，字段：`max_context`、`max_output_tokens`、`thinking_mode`、`api_type`、`enabled`（v39，默认 1）。`enabled=0` 的模型被禁用。

### user_default_model 表（v39）

用户级默认 (订阅, 模型)，主键 `sender_id`。用于新会话解析（`ResolveLLM` 的第 3 优先级）。

### SessionLLMState（TUI 端，存在 dirSession JSON）

```
SubscriptionID   string — 当前会话的订阅 ID
Model            string — 当前模型名
MaxContextTokens int    — 用户手动设置的 max_context 覆盖（0=从订阅继承）
MaxOutputTokens  int    — 用户手动设置的 max_output_tokens 覆盖
```

## LLMFactory Cache Architecture

`LLMFactory` 用单一 `entries map[string]*llmEntry` 缓存所有 LLM 状态。
key 的格式决定作用域：

| Key 格式 | 作用域 | 示例 |
|---------|--------|------|
| `senderID` | 用户级别 | `"cli_user"` |
| `senderID:chatID` | 会话级别 | `"cli_user:/home/proj:Agent-001"` |

### llmEntry 结构

```go
type llmEntry struct {
    client          llm.LLM              // LLM 客户端实例
    model           string               // 当前模型名
    sub             *LLMSubscription     // 来源订阅（用于 max_context 解析）
    maxOutputTokens int
    thinkingMode    string
}
```

**设计原则**: 每个 entry 包含完整的客户端+订阅信息，保证 `model`、`maxContext`、`thinkingMode` 来自同一个订阅，不会出现"模型来自 A 订阅，max_context 来自 B 订阅"的不一致。

### defaultLLM / defaultModel

- **启动时**: `server.go` reconcile 系统订阅 → `GetDefault`（用户默认，否则回退系统订阅）→ 创建客户端 → `SetSystemLLM(client, model)`（不清空 per-user 缓存）
- **用户全局切换**: `SwitchSubscription` 对 `cli_user` 会同步更新 `defaultLLM`/`defaultModel`
- **作用**: SubAgent fallback（无指定模型时）、`ListModels()`、`GetLLM()` 最终 fallback
- **不覆盖的场景**: Feishu 用户（`senderID != "cli_user"`）不会影响 `defaultLLM`
- **单源**: v44 起 `defaultLLM` 的凭证源自 DB 系统订阅行（启动从 config/env reconcile），不再由 `cfg.LLM` 直接构建，也不再 `SetDefaults` 重建。`cfg.LLM` 仅作启动种子与 tier 模型载体。

## LLM Resolution（LLM 获取逻辑）

### GetLLM(senderID) — 用户级别

```
1. entries[senderID] 命中 → 返回缓存的 client + model + maxContext
2. subscriptionSvc.GetDefault(senderID) → 从 DB 查默认订阅 → 创建客户端缓存到 entries
3. fallback → defaultLLM + defaultModel + maxContext=0
```

### GetLLMForChat(senderID, chatID) — 会话级别

这是 agent 运行时的主要入口（`engine_wire.go:86`）。

```
1. entries["senderID:chatID"] 命中（per-session 订阅）→ 返回
2. perChatMaxCtx[chatID] 命中（只改 max_context，不改订阅）→ 用 GetLLM(senderID) 的 client + 覆盖 maxCtx
3. fallback → GetLLM(senderID)（用户级别）
```

### GetLLMForModel(senderID, targetModel) — SubAgent 专用

SubAgent 角色可以指定模型或 tier（vanguard/balance/swift）。

```
1. 解析 tier → 具体模型名
2. buildModelSubscriptionMap → 按 model→sub 映射精确匹配
3. configSubsFn（config.json 订阅）精确匹配
4. 订阅 API 动态加载模型列表
5. tier-fallback: 任意订阅 + 目标模型名（OpenAI 兼容）
6. 最终 fallback: GetLLM(senderID) 的 client + 解析后的模型名
```

### Max Context Resolution 优先级

**后端（agent maybeCompress 使用的）**:
```
engine_wire.go → GetLLMForChat → resolveEffectiveContext(model, sub)
  1. sub.PerModelConfigs[model].MaxContext（per-model 订阅配置）
  2. modelContexts[model]（全局 config model_contexts）
  3. 0（schema 默认值）
→ 然后 applyUserMaxContext(base, userMaxCtx) 覆盖到 ContextManagerConfig
```

**TUI（context bar 显示的）**:
```
ResolveEffectiveMaxContext(state, subMgr)
  1. state.MaxContextTokens（Session JSON 手动设置）
  2. sub.PerModelConfigs[model].MaxContext（订阅 per-model 配置）
  3. config.DefaultMaxContextTokens（schema 默认值 200000）
```

**两者必须一致**。如果不一致就会出现"TUI 显示 200k 但后端用 1M 做压缩判断"的 bug。

## Subscription Switch Scenarios（订阅切换场景）

### 场景 1: TUI 切换模型（跨订阅，per-session）

用户在 TUI 用 Ctrl+N 统一面板选中一个模型，该模型可能属于**另一个订阅**。

**TUI 端** (`cli_subscription.go:applyModelSwitch`):
1. `m.llmSubscriber.SwitchModel(senderID, model, chatID)` → RPC `switch_model`
2. `GetSessionSubscription(senderID, chatID)` 回读后端解析出的 owner 订阅，修正 `activeSubID`
3. `ResolveEffectiveMaxContext/Output` 重算上下文/输出上限
4. `SaveSessionLLMState()` 持久化 (ownerSubID, model) 到 Session JSON

**RPC 端** (`rpc_table.go` `switch_model`，chatID != "" 路径):
1. `ResolveSubscriptionForModel(bizID, model)` → 解析 owner 订阅（跳过 disabled 订阅/模型，默认订阅优先）
2. `SelectModel(owner.ID, model, chatID)` → 校验订阅/模型 enabled，写 tenants 表 (ownerSubID, model)
3. 解析失败才回退旧默认订阅路径

**订阅面板不再切换**：`cli_panel_quickswitch.go` "subscription" 模式只做 添加/禁用/删除（Enter=启停，E=编辑，D=删除）。旧的 `SwitchLLM` 异步切换 + `cliSwitchLLMDoneMsg` 流程仅保留给启动恢复（`scheduleSessionLLMRestore`）使用。

**对其他会话的影响**: 模型切换是 per-session 的（只写当前 chatID 的 tenants 行 + per-chat entry），不触碰全局默认，不影响其他会话。

### 场景 2: TUI 切换模型（Ctrl+N 统一面板）

- **Ctrl+N / 点状态栏模型名 / palette "Models & Subscriptions"** → 打开**统一 LLM 面板**：模型行 `ListAllModelEntries()` 按 `(订阅, 模型)` 配对列出（`订阅名 · 模型名` + 状态标签）——**同一模型名被多个订阅提供时每个订阅各列一行**（如 `system · deepseek-v4-pro` 与 `deepseek · deepseek-v4-pro` 同时出现），`/` 按订阅名/模型名子串过滤，↑↓ + Enter 选中，走 `applyModelSwitch(model, subID)`（同场景 1）。同一面板内订阅行做管理（添加/禁用/删除），模型行 `E` 编辑参数、`N` 添加未拉取模型。模型多时用过滤比轮转快得多。

选中模型时，picker 行携带的 `SubID` 直接走 `SelectModel(senderID, subID, model, chatID)` 钉住该订阅凭据（不再仅按模型名服务端解析 owner）；`SubID` 为空时回退 `ResolveSubscriptionForModel` + `SwitchModel`。

### 场景 3: Settings 面板修改 max_context

**TUI 端** (`cli_settings.go:saveSettings`):
1. 提取 `max_context_tokens` 值
2. `subscriptionMgr.UpdatePerModelConfig(subID, model, pmc)` → RPC
3. 刷新 `cachedMaxContextTokens`

**RPC 端** (`update_per_model_config`):
1. 只修改 `PerModelConfigs[model].MaxContext`，不触碰凭据
2. `InvalidateSender(senderID)` — 清除 user-level 缓存
3. `SwitchSubscription` — 重建 user-level entry（读取新 PerModelConfigs）

**对 agent 的影响**: 下次 `GetLLMForChat` 返回更新后的 `maxContext` → `maybeCompress` 使用新阈值。

### 场景 4: 全局订阅切换（Settings 面板切换订阅）

用户在 Settings 面板更改 `llm_provider`/`llm_api_key`/`llm_base_url`/`llm_model`。

**路径** (`main.go:428-510` `updateActiveSubscription`):
1. 如果只改 `llm_model`：尝试找到匹配订阅 → `SetDefaultSubscription(subID, "")` 全局切换
2. 否则：创建/更新订阅 → `SetDefaultSubscription(newSubID, "")` 全局切换

**RPC 全局路径** (`rpc_table.go:1348-1353`，chatID == "" 路径):
1. `svc.SetDefault(id)` — 更新 DB is_default
2. `InvalidateSender(bizID)` — 只清 user-level
3. `SwitchSubscription(bizID, sub, "")` — 更新 user-level + defaultLLM/defaultModel

### 场景 5: 启动时订阅恢复

**server.go 启动** (`server.go` reconcile 流程):
1. `migrateConfigSubscriptions` — config.json `subscriptions[]` → DB 用户订阅（仅首跑）
2. `reconcileSystemSubscription` — 从 `cfg.LLM`/env upsert 系统订阅（每次启动覆盖凭证字段）
3. `cli_user` 无默认时把系统订阅设为其默认（首跑）
4. `subSvc.GetDefault("cli_user")` → 用户默认，否则回退系统订阅
5. `SetSystemLLM(newClient, defSub.Model)` — 设置 defaultLLM 兜底（不清空 per-user 缓存）
6. `SetUserMaxOutputTokens("cli_user", ...)` — 恢复 per-user 配置

**TUI 启动** (`cli.go:191-208`):
1. `refreshCachedModelName()` → 从后端 `GetSessionSubscription` RPC（remote mode）或 Session JSON（local mode）恢复 `activeSubID`/`cachedModelName`。**per-session model 优先**：如果 tenants 表有该会话的 model 记录，就用它，不用订阅默认 model。
2. `RefreshValuesCache(activeSubID)` — 同步 settings 缓存
3. `scheduleSessionLLMRestore()` — 异步触发后端 `SetSessionLLM` + `SwitchLLM`。**关键修复**：使用步骤 1 恢复的 per-session model（`m.cachedModelName`），而非订阅默认 model。如果 per-session model 与订阅默认不同，`handleSwitchLLMDoneMsg` 会在 `SetDefault` 之后额外调用 `SwitchModel` RPC 来纠正后端的 per-chat entry model 字段，确保重启后不会回退到订阅默认 model。

## Session Isolation（会话隔离保证）

### 核心规则

**`Invalidate()` vs `InvalidateSender()` vs `InvalidateSession()`**

| 方法 | 清除范围 | 适用场景 |
|------|---------|---------|
| `Invalidate(senderID)` | user-level + 所有 per-chat entries | 删除订阅、更新订阅 PerModelConfigs（需要强制刷新所有缓存） |
| `InvalidateSender(senderID)` | 只清 user-level entry | 全局订阅切换（保留其他会话的 per-session 订阅） |
| `InvalidateSession(senderID, chatID)` | 只清一个 per-chat entry | 单个会话重置 |
| `InvalidateAll()` | 清空所有 | 测试/重置 |

### 为什么全局切换用 InvalidateSender 而非 Invalidate

CLI 模式下所有会话共享 `senderID = "cli_user"`。如果全局切换用 `Invalidate`：

```
会话 A 有 per-session GLM（entries["cli_user:chatA"] = GLM entry）
会话 B 无 per-session（fallback to entries["cli_user"] = DeepSeek entry）

用户在会话 C 做全局切换 → Invalidate("cli_user")
→ entries["cli_user"] 被清除 ✓
→ entries["cli_user:chatA"] 也被清除 ✗ ← 会话 A 的 GLM 被丢弃

之后 SwitchSubscription(newSub) → entries["cli_user"] = newSub
会话 A 调用 GetLLMForChat → 无 per-chat entry → fallback to user-level → 得到 newSub
```

用 `InvalidateSender`：
```
InvalidateSender("cli_user") → 只清 entries["cli_user"]
→ entries["cli_user:chatA"] 保留 ✓

会话 A 调用 GetLLMForChat → per-chat entry 命中 → 仍然得到 GLM
```

### 什么情况下用 Invalidate（全清）

1. **删除订阅** (`remove_subscription`): 被删的订阅可能被多个会话缓存，必须全清
2. **更新订阅凭据** (`update_subscription`): per-chat 缓存的 `*LLMSubscription` 指针指向旧数据，必须全清

## Invalidate / SwitchSubscription 速查表

| 调用者 | 方法 | Invalidate 类型 | SwitchSubscription |
|--------|------|----------------|-------------------|
| `setDefaultSubscription` (per-session, chatID != "") | RPC | 无 | `SetSessionLLM` |
| `setDefaultSubscription` (全局, chatID == "") | RPC | `InvalidateSender` | `SwitchSubscription(bizID, sub, "")` |
| `setSubscriptionModel` | RPC | `InvalidateSender` | `SwitchSubscription(senderID, sub, "")` |
| `update_subscription` | RPC | `Invalidate`（全清） | `SwitchSubscription(bizID, dbSub, "")` |
| `remove_subscription` | RPC | `Invalidate`（全清） | 无 |
| `LLMSetDefaultSubscription` (callback) | callbacks | `InvalidateSender` | `SwitchSubscription(senderID, sub, "")` |
| `LLMUpdateSubscription` (callback) | callbacks | `Invalidate`（全清） | 无 |
| `handleSwitchLLMDoneMsg` (TUI 回调) | channel | 不直接调用 | 通过 `mgr.SetDefault` 间接触发上面的 RPC |
| startup (`server.go`) | server | `SetDefaults`（全清+重设） | `SetUserMaxOutputTokens`（thinking_mode 不再走这里，改为 seed 全局用户设置，见「ThinkingMode 全局开关」） |

## TUI ↔ Backend 数据同步

### TUI 状态字段

| 字段 | 来源 | 用途 |
|------|------|------|
| `activeSubID` | Session JSON / `refreshCachedModelName()` | 标识当前会话的订阅 |
| `cachedModelName` | Session JSON / RPC / 自动发现 | status bar 显示模型名 |
| `cachedMaxContextTokens` | `resolveMaxContext()` | context bar 进度条上限 |
| `cachedMaxOutputTokens` | `resolveMaxOutputTokens()` | context bar 压缩阈值计算 |
| `lastTokenUsage` | progress event `TokenUsage` | context bar 当前 token 数 |

### Backend 状态字段

| 字段 | 来源 | 用途 |
|------|------|------|
| `entries[senderID]` | `SwitchSubscription` / `GetLLM` 懒加载 | user-level LLM 客户端 |
| `entries[senderID:chatID]` | `SetSessionLLM` | per-session LLM 客户端 |
| `defaultLLM` | `SetDefaults` / `SwitchSubscription`(cli_user) | SubAgent fallback / ListModels |
| `perChatMaxCtx[chatID]` | `SetPerChatMaxContext` | per-session max_context 覆盖 |

### 数据一致性保证

1. **TUI 端** `applySessionLLMState()` 是**唯一**更新 `activeSubID`/`cachedModelName`/`cachedMaxContextTokens` 的方法
2. **TUI 端** `SaveSessionLLMState()` 原子写入所有 per-session LLM 字段
3. **后端** `llmEntry` 保证 client/model/sub 来自同一个订阅
4. **RPC** `setDefaultSubscription` 全局路径用 `InvalidateSender`（非 `Invalidate`），保留 per-session 隔离

## Gotchas

### 1. CLI 所有会话共享 senderID

所有 CLI 会话的 `senderID` 都是 `"cli_user"`。`entries` map 靠 `chatKey(senderID, chatID)` 区分会话。如果 chatID 不匹配（typo、格式变化），会话会 fallback 到 user-level entry。

### 2. TUI 和后端 max_context 来源不同

TUI 的 `resolveMaxContext()` 从 `activeSubscription()` → `PerModelConfigs` 读取。
后端的 `maybeCompress` 从 `GetLLMForChat()` → `resolveEffectiveContext()` 读取。
两者必须引用同一个订阅的同一条 PerModelConfig，否则会出现"context bar 显示 200k 但压缩用 1M 判断"。

### 3. SwitchModel 清除 client 但保留 sub

`SwitchModel` 设置 `entries[key] = &llmEntry{sub: ..., model: newModel, client: nil}`。下次 `GetLLMForChat` 命中时懒重建客户端。如果此时 sub 的凭据已被其他操作修改，懒重建会用旧 sub 的凭据。

### 4. scheduleSessionLLMRestore 必须恢复 per-session model

`scheduleSessionLLMRestore()` 必须使用 `m.cachedModelName`（由 `refreshCachedModelName` 从 tenants 表恢复的 per-session model），而非 `target.Model`（订阅默认 model）。否则用户通过 Ctrl+N 切换的 per-session model 在重启后丢失，回退到订阅默认 model。

`handleSwitchLLMDoneMsg` 在 `SetDefault(subID, chatID)` 之后必须调用 `SwitchModel(senderID, perSessionModel, chatID)`，因为 `SetDefault` 的 RPC handler 调用 `SetSessionLLM` 用的是 `sub.Model`（订阅默认），会覆盖 per-chat entry 的 model 字段。`SwitchModel` 纠正 model 字段并重新持久化到 tenants 表。当 perSessionModel 等于订阅默认 model 时（如订阅面板切换场景），此调用幂等。

### 5. scheduleSessionLLMRestore 的二次 SetDefault

TUI 启动恢复 per-session 订阅时，`handleSwitchLLMDoneMsg` 会额外调用全局 `SetDefault(subID, "")`。这个全局调用走 RPC 的全局路径（`InvalidateSender` + `SwitchSubscription`），会影响 user-level entry 和 defaultLLM。这是有意为之——新会话应该继承最后使用的订阅。

### 6. remote mode 下 Session JSON 不是订阅的 source of truth

remote mode 下，`SaveSessionLLMState(..., true)` 不写 SubscriptionID/Model 到本地 JSON。
后端 DB（tenants 表）是 source of truth。`refreshCachedModelName()` 优先查询后端。

### 7. PerModelConfig 写入必须用 UpdatePerModelConfig

`UpdatePerModelConfig(id, model, pmc)` 只修改 `PerModelConfigs` 字段，不触碰凭据。
`Update(id, sub)` 会读取完整的订阅数据再写回，如果传入 masked key 会破坏真实凭据。

### 8. 跨订阅切模型必须解析 owner 订阅（model-first）

`switch_model` per-session 分支绝不能用 `GetDefault()` 配对模型名写 tenants 表——切到别的订阅的模型时会用错凭据 404。必须 `ResolveSubscriptionForModel` 找到提供该模型的订阅，再 `SelectModel(owner.ID, model, chatID)`。详见上方"跨订阅切模型 404"。

### 9. LLMFactory 无内存缓存（DB-direct）

agent loop 走 `ResolveLLM`（权威），每次调用从 DB 直接读取：ProxyLLM → tenants 表 → `user_default_model` → `GetLLM`。`sessionMemo`/`entries`/`hasCustomLLMCache`/`perChatMaxCtx` 已全部移除。`Invalidate`/`InvalidateSender`/`InvalidateSession` 是 no-op，`InvalidateAll` 只清 `clientCache`。新增 LLM 相关功能应基于 `ResolveLLM`/`SelectModel`，不要依赖已移除的缓存。

### 10. 订阅 CRUD 必须 InvalidateSubscription 同步 clientCache

model-first 引入 `clientCache`（按 `(subID, apiType)` 缓存 client）。`add/remove/update/set_default/set_subscription_model/update_per_model_config` 等 RPC 和 `LLMAdd/Remove/SetDefault/Update` callback 都必须调 `LLMFactory().InvalidateSubscription(subID)`，否则 `ResolveLLM` 会复用旧凭据的 client。

### 11. ResolveSubscriptionForModel 跳过 disabled 模型

`ResolveSubscriptionForModel` 第一遍只匹配 `subscription_models.enabled=1` 的行；`CachedModels`/`sub.Model` 作为回退（覆盖尚未建行的模型）。被禁用的模型不会被选为 owner，`SelectModel` 也会拒绝。

### 12. v39 迁移与 master v38 runner_id 的 rebase 关系

master #179 用了 v38（`tenants.runner_id`）；model-first 重设计也用了 v38。rebase 后 model-first 迁移抬到 **v39**，保留 master 的 v38。`schemaVersion=40`（v40 加入订阅级 enabled）。注意：曾用旧 model-pool 二进制升到 v38 的 live DB 缺 `runner_id`（旧 v38 没加），需手动 `ALTER TABLE tenants ADD COLUMN runner_id TEXT DEFAULT ''` 后再跑新二进制（v39/v40 幂等，会把 version 升到 40）。这是该 dev 机器的特例，不应写进迁移代码。

### 13. 订阅级 enabled（v40）必须三层同步跳过

`user_llm_subscriptions.enabled=0` 的订阅必须被三处同时跳过，否则禁用形同虚设：
- `ListAllModelsForUser`：两个循环都加 `if !sub.Enabled { continue }`，否则禁用订阅的模型仍进 `states`/结果列表。
- `ResolveSubscriptionForModel`：`find` 闭包内 `if !sub.Enabled { continue }`，否则禁用订阅会被选为 owner。
- `SelectModel`：`if !sub.Enabled { return err }`，否则显式 SelectModel 仍能选中禁用订阅。
`SetSubscriptionEnabled` 改完必须 `InvalidateSubscription(subID)`，否则 `clientCache` 复用旧 client。

### 14. 统一面板不切换订阅；模型切换跨订阅必须回读 owner

`cli_panel_quickswitch.go` 现在是**单一面板**（`Ctrl+N` / 点状态栏模型名 / palette "Models & Subscriptions"），合并订阅+模型+添加动作为一个扁平 `[]qsRow` 列表。订阅行只管理（添加/禁用/删除，`Enter` 启停、`E` 编辑、`D` 删除），不再有"切换订阅"动作（旧 `SwitchLLM` 异步 + `cliSwitchLLMDoneMsg` 仅留作启动恢复）。**不再有 `Ctrl+P`/`Ctrl+N`**。模型切换（模型行 `Enter`）跨订阅时，`applyModelSwitch` 在 `SwitchModel` 之后**必须**用 `GetSessionSubscription` 回读 owner 订阅修正 `activeSubID`——否则 `activeSubID` 停留在旧订阅，上下文上限/输出上限/settings 面板都显示错误订阅的配置。

### 15. 面板用 `ListAllModelEntries`（带 owner 名 + Status）；列表 DB 驱动

面板的模型行必须用 `ListAllModelEntries()`（`[]{SubID,SubName,Model,Status}`），**不要**用纯 `ListAllModels()` + CLI 本地拼装订阅名——本地拼装会和服务端 `ListAllModelEntriesForUser` 的 status/owner 逻辑分叉。**列表源是 DB 驱动**：`listModelEntriesCore` 列 `sub.CachedModels` ∪ `sub.Model` ∪ `subscription_models` 行的**全部记录**（即数据库里所有模型项，含刚拉取到的和手动添加的）。`Status`：`disabled`（`subscription_models.enabled=0`）/ `normal`（在 CachedModels 或是 sub.Model，且未禁用）/ `offline`（有记录但未拉取，且未禁用——手动添加的，可选）。`ListAllModelEntriesForUser`（includeDisabled=true，面板用）**包含全部三种**；`ListAllModelsForUser`（includeDisabled=false，tier 用）是 `Status!="disabled"`（normal+offline）子集——**禁用模型只在 entries 里、不在 names 里**，新增可见性逻辑改 `listModelEntriesCore` 一处即可。owner 归属按 status rank（normal>offline>disabled）选最佳订阅。

- 面板用 **`/` 切换过滤模式**（`quickSwitchFiltering` + `quickSwitchFilterInput` textinput），命令模式与过滤模式分离。`handleQuickSwitchKey` 命令模式先拦截 Esc/Up/Down/Enter（操作 `quickSwitchRows`），再分派命令字母 `e`/`d`/`n`/`s`/`/`；过滤模式把字母喂给 textinput 并 `rebuildLLMRows` 重过滤。**这取代了旧的 Ctrl 组合命令**（`ctrl+e`/`ctrl+a`/`ctrl+t`，与全局 `Ctrl+E` 折叠、`Ctrl+T` 会话冲突）。`isNoiseModel` 默认隐藏 image/realtime/whisper/tts/audio/embed/moderation/dated-snapshot，`S`(`quickSwitchShowAll`) 显示全部。`applyQuickSwitch` 从 `quickSwitchRows[cursor]` 取当前行，模型行**`disabled` 拒绝切换**（保持面板开放，提示按 `E` 复启），`normal`/`offline` 都可选。
- **`E` 编辑模型参数**：`openEditModelPanel` 打开 mini 面板编辑 `max_context`/`max_output`/`api_type`/`enabled`，提交时 `UpdatePerModelConfig`（→ `UpsertModel`，**只增不减**，以 (订阅,模型) 为键 INSERT OR REPLACE，禁用=enabled=0 永不 DELETE）+ `SetModelEnabled`（仅 enabled 变更时）；保存后 `reopenLLMPanelOn(model)` 用 DB 快照重开面板，状态标签即时刷新（不再触发 /models 异步刷新）。
- **`N` 添加模型**：`openAddModelPanel` 打开 mini 面板：选启用订阅 + 模型名 + 可选 `max_context`/`max_output`/`api_type`，提交 `UpdatePerModelConfig`/`UpsertModel`（只增）。新模型以 `offline` 出现（直到 `/models` 列出它），立即可选。用于注册 provider 没列出但实际可用的模型。
- **点状态栏模型名 = 打开面板**（`cli_mouse.go` "modelName" → `openQuickSwitch("")`）。
- **订阅编辑面板只改凭证/默认值**：`openEditSubscriptionPanel` 不再带逐模型 toggles（逐模型参数只在模型行 `E` 编辑，避免两处入口冗余）；`Update` 时保留 `target.PerModelConfigs` 不动。
- 状态栏 `订阅名 · 模型名` 靠 `cachedSubName`，由 `refreshCachedSubName` 在 `activeSubID` 变更路径（`applyModelSwitch` / `refreshCachedModelName` defer / `applySessionLLMState`）刷新——每次一次 `List("")` RPC，**View() 只读缓存**，绝不能在每帧 RPC。订阅重命名后状态栏订阅名可能短暂滞后（下次 activeSubID 变更或面板刷新才更新），可接受。

- **面板缓存规则**：`llmCache` 只服务打开面板时的快速渲染和过滤/展开等纯本地重排。任何订阅/模型元数据变更（新增/编辑/删除订阅、启停订阅、添加模型、编辑模型参数、启停模型）后都必须走 `rebuildLLMRowsFresh()` 或 `reopenLLMPanelOn()`，从 `llmSource()` 重新读取完整 `subs+entries` 快照后再重建 rows；不能只调用 `rebuildLLMRows()` 读取旧 cache。后台 `/models` 刷新完成 (`cliModelEntriesRefreshedMsg`) 时也必须重新读取订阅列表，再合并刷新返回的 entries，避免刷新飞行期间新增/编辑的订阅被旧 cache 覆盖。`add_subscription` RPC 必须保留客户端传入的 `id` 和 `per_model_configs`，否则 TUI 刷新后拿到的订阅 ID 会与刚创建的本地 ID 不一致，导致定位/后续模型操作错位。

### 16. `OnModelsLoaded` 必须在 model-first 路径注入；选择器开面板必须实时刷新

model-first 重构曾把 `OnModelsLoaded` 回调丢线：`createClientFromSub`（及 entry 构造路径）构造 `UserLLMConfig` 时不设 `OnModelsLoaded`，导致每个订阅 client 即便异步拉到 `/models` 也不写回 `CachedModels`。症状：模型选择器/Ctrl+N 每个订阅只看到 `sub.Model`（一个），**provider 真实可用模型全部缺失**（"列表不全"，deepseek/glm 只显示 1-2 个）。修复：`makeOnModelsLoaded(subID)` 在三处 `createClient` 调用点注入（`createClientFromSub` + 两处 entry 构造），回调内 `Get(subID)` nil-check 后 `UpdateCachedModels`（`UpdateCachedModels` 对不存在的 subID 会 nil-deref，必须先 Get 守卫）。

光修回调还不够——`CachedModels` 只在订阅 client 被构造时才更新，"从没用过的订阅" / "provider 新增模型"仍是旧值。所以开面板**必须**触发 `RefreshModelEntriesForUser`：并行（`sem` 容量 8）对每个 `Enabled && BaseURL && APIKey` 的订阅 `createClientFromSub` + `llm.ModelLoader.LoadModelsFromAPI`（8s 超时），失败软降级（保留旧 `CachedModels`），完成后返回最新 entries。CLI 侧异步：`openLLMPanel` 把 `refreshModelEntriesCmd` 推进 `m.pendingCmds`，三个入口（Ctrl+N / 点状态栏模型名 / palette Enter）都必须 drain `pendingCmds` 把 cmd 发出去；回包 `cliModelEntriesRefreshedMsg` 在 `Update` 顶层处理，`rebuildLLMRows` 重建 `quickSwitchRows`（**只夹紧光标，不重置**——否则后台刷新会把用户光标拽回顶部）。`rebuildLLMRows` 因此只做 clamp，光标置位由调用方负责（open → `cursorToActiveLLMRow`，typing → 0）。`backendModelLister.EnsureModelsLoaded` 是 no-op（远程模式服务端管缓存），刷新由 `RefreshModelEntries` 显式触发，别再依赖 `EnsureModelsLoaded`。

**`/models` 跨 provider 不可靠是固有的**：Anthropic 根本没有 `/models` 端点；代理/网关常 404、或只返回 curated 子集、或返回非 OpenAI 格式导致 SDK 解析失败 → 软降级保留旧缓存（看起来"不全"）。这不是 bug，是 API-pull 的代价。缓解：`isNoiseModel` 默认过滤掉 chat 不可用的噪声模型（image/realtime/whisper/tts/audio/embed/moderation/dated-snapshot），`E` 让用户手动给任何模型配参数/启停（写入只增不减的 `subscription_models`），`S` 显示全部噪声，`N` 手动注册 provider 没列出的模型（显示为 `offline`，立即可选）。**不保证列表完整**——provider 没列出的模型需要用户用 `N` 手动添加。
