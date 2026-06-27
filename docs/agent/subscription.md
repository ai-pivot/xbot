# Subscription & LLM Resolution

## Overview

xbot 的 LLM 配置分为 3 层：全局默认 → 用户级别订阅 → 会话级别覆盖。
每个会话最终使用哪组 LLM 凭据/模型/参数，由 `LLMFactory` 运行时决定。

## Model-First Redesign (v39, authoritative)

> 本节描述 **当前权威路径**。下方 "LLM Resolution" 里的 `GetLLMForChat` 等遗留入口仍存在，但 agent loop 已切换到 `ResolveLLM`，新代码应优先使用本节 API。

设计原则：**model 是一等实体，subscription 是模型的凭据来源**。agent 只关心 model 层；订阅只提供凭据和 per-model 配置。模型可被禁用，订阅也可被整体禁用（v40）。

**UI 后果**：TUI 不再有"切换订阅"动作。模型选择（Ctrl+N / 模型面板）是唯一的切换入口，且**跨订阅**——选中属于别的订阅的模型时，后端 `ResolveSubscriptionForModel` 自动解析 owner 订阅并配对凭据。订阅面板降级为管理面板：**只支持 添加 / 禁用 / 删除**，不支持切换。

### 新增 DB（v39 迁移，rebase 后与 master 的 v38 runner_id 并存）

- `subscription_models.enabled INTEGER NOT NULL DEFAULT 1` — 模型禁用开关。禁用后该模型不出现在轮换/选择器，`SelectModel` 拒绝选中。
- `user_default_model(sender_id PK, subscription_id, model, updated_at)` — 用户级默认 (订阅, 模型)，用于新会话解析，取代旧的 `user_llm_subscriptions.model` "当前模型" 隐式语义。
- v39 迁移幂等：补建 `enabled` 列、`user_default_model` 表，从 tenants 引用回填具体 model 行，从默认订阅 seed `user_default_model`。

### 新增 DB（v40 迁移）

- `user_llm_subscriptions.enabled INTEGER NOT NULL DEFAULT 1` — **订阅级禁用开关**。禁用后该订阅不再向模型选择池贡献任何模型（`ListAllModelsForUser` / `ResolveSubscriptionForModel` 跳过，`SelectModel` 拒绝），但凭据和 per-model 配置保留，重新启用无损。v40 迁移幂等（`ALTER TABLE ... ADD COLUMN enabled ...`，缺失才加）。

### 新增 LLMFactory API

| 方法 | 作用 |
|------|------|
| `ResolveLLM(senderID, chatID, channel)` | **权威解析入口**（agent loop 用）。返回 (client, model, maxContext, thinkingMode, maxOutputTokens) |
| `SelectModel(senderID, chatID, channel, subID, model)` | per-session 选 (订阅, 模型)，校验 enabled，写 tenants 表，失效 sessionMemo |
| `SetUserDefaultModel(senderID, subID, model)` | 用户级默认 (订阅, 模型)，写 `user_default_model`，失效该用户所有 memo |
| `SetModelEnabled(subID, model, enabled)` | 切换模型禁用状态，失效该订阅缓存 |
| `SetSubscriptionEnabled(subID, enabled)` | **v40** 切换订阅级禁用，失效该订阅缓存（禁用订阅不贡献模型） |
| `ListAllModelEntriesForUser(senderID)` | 返回 `[]protocol.ModelEntry{SubID,SubName,Model}`，`ListAllModelsForUser` 的富化版（带 owner 订阅名，供"订阅名·模型名"选择器），共享同一 selectable 逻辑 |
| `RefreshModelEntriesForUser(senderID)` | 并行拉每个启用订阅的 `/models` → 经 `OnModelsLoaded` 落 `CachedModels` → 返回最新 `[]ModelEntry`。失败软降级。供选择器开面板刷新 |
| `makeOnModelsLoaded(subID)` | 构造 `OnModelsLoaded` 回调：`Get(subID)` nil-check 后 `UpdateCachedModels`。在 `createClientFromSub` 及 entry 构造路径注入，修复 model-first 重构丢失的持久化 |
| `ResolveSubscriptionForModel(senderID, model)` | **模型→订阅反向解析**：找提供该模型的订阅（enabled 行优先 → CachedModels/sub.Model；默认订阅优先；跳过 disabled 订阅与 disabled 模型） |
| `PickDefaultModelForSub(sub)` | 订阅无 `Model` 时从 `subscription_models`/`CachedModels` 选一个真实模型，避免 `f.defaultModel` 污染 |

### ResolveLLM 解析链（优先级从高到低）

```
1. sessionMemo[senderID:chatID] 命中且 client 非 nil → 直接返回（memo 缓存）
2. tenantSvc.GetTenantSubscription(channel, chatID) → tenants 表的 (subID, model)
3. subscriptionSvc.GetUserDefaultModel(senderID) → user_default_model 的 (subID, model)
4. fallback → GetLLM(senderID)（遗留用户级，再退到 defaultLLM）
```

命中 (subID, model) 后：`lookupSub(subID)` → `getOrCreateClient(sub, model)`（client 按 `(subID, apiType)` 缓存复用）→ 解析 per-model 配置 → 写 `sessionMemo`。

**关键**：`ResolveLLM` **不读**遗留 `entries` map（`f.entries`），所以 `LLMFactory.SwitchModel` 写的 per-chat entry 对解析而言是死代码。

### 新增 RPC

| RPC | 说明 |
|-----|------|
| `select_model` | per-session (subID, model)，走 `SelectModel`。需 chatID（用户级用 `set_default_model`） |
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

- `ListAllModelsForUser` 聚合 `subscription_models` 行 + `CachedModels` + `sub.Model`，并**排除被禁用的模型**（仅当所有列出它的订阅都禁用时排除），且**整体跳过 `enabled=0` 的订阅**（禁用订阅不贡献任何模型）。Ctrl+N 轮换、tier 选择器、idle placeholder 自动不再出现禁用模型/禁用订阅的模型。
- `protocol.PerModelConfig.Enabled` 是读侧投射字段，`mergeSubscriptionModels` 把 `subscription_models.enabled` 透传到客户端，供 UI 显示。`protocol.Subscription.Enabled`（v40）由 `subToChannel` / `LLMGetSubscription` 从 `user_llm_subscriptions.enabled` 透传。
- 订阅编辑面板（`editQuickSwitchEntry`）每个模型行有 Enabled/Disabled 下拉，保存时按差异调 `SetModelEnabled`。
- 订阅管理面板（`cli_panel_quickswitch.go`，"subscription" 模式）**不再有切换动作**：Enter = 启用/禁用该订阅（调 `SetSubscriptionEnabled`，面板保持打开以便连续管理），E = 编辑，D = 删除，末尾 `➕ Add subscription`。删除当前活跃订阅会被拒绝（提示先切模型）。
- 模型选择面板（"model" 模式）= **可搜索的跨订阅模型选择器**：`ListAllModelEntries()` 返回 `[]{SubID, SubName, Model}`（权威：服务端 `ListAllModelEntriesForUser` 复用 `ListAllModelsForUser` 的 enabled 过滤），列表项显示 **`订阅名 · 模型名`**（系统默认模型无订阅名时只显示模型名）。带过滤输入框（输入即按订阅名/模型名子串过滤），↑↓ 导航，Enter 调 `applyModelSwitch` → `SwitchModel` → 后端 `ResolveSubscriptionForModel` + `SelectModel` 配对 owner 订阅。`applyModelSwitch` 在 `SwitchModel` 之后用 `GetSessionSubscription` 回读 owner 订阅，修正 `activeSubID`/上下文上限/输出上限并持久化（local 模式同样走 RPC，tenants 表是 source of truth）。
  - **入口**：`Ctrl+L`（主入口，匹配历史文档语义）、点状态栏模型名（不再是轮转）、palette "Switch Model" 命令。`Ctrl+N` 保留为快速轮转（无面板，逐个切换）。
  - **状态栏**显示 `订阅名 · 模型名`（窄屏回退为只显示模型名），由 `cachedSubName` 缓存（`refreshCachedSubName` 在 `activeSubID` 变更路径上刷新：`applyModelSwitch` / `refreshCachedModelName`(defer) / `applySessionLLMState`，每次一次 `List("")` RPC，View() 只读缓存，非每帧）。
  - **开面板即时刷新**：`openQuickSwitch("model")` 先用 DB 快照渲染，同时后台调 `RefreshModelEntries()` → RPC `refresh_model_entries` → `LLMFactory.RefreshModelEntriesForUser`：并行（并发上限 8、每订阅 8s 超时、失败软降级保留旧 `CachedModels`）对每个启用订阅拉 `/models`，经 `OnModelsLoaded` 回调落 `CachedModels`，返回最新 entries。CLI 收到 `cliModelEntriesRefreshedMsg` 后替换 `quickSwitchModelEntries` 并重过滤（保留当前过滤文本与光标位置，越界则夹紧），面板顶部显示 `↻ 刷新模型列表…`。这解决了"列表不全"——不再只靠 `sub.Model` + 手动行，而是反映 provider 真实可用模型。
- `cycleModel`（Ctrl+N）已改为跨订阅：用 `ListAllModels()` 而非 `ListModels()`，复用 `applyModelSwitch`。Ctrl+N 是"快速轮转"，Ctrl+L 是"挑一个"，二者互补。

## Key Files

| File | Role |
|------|------|
| `agent/llm_factory.go` | LLM 客户端缓存、订阅解析、模型切换 |
| `serverapp/rpc_table.go` | `setDefaultSubscription` / `setSubscriptionModel` 等 RPC handler |
| `serverapp/callbacks.go` | `LLMSetDefaultSubscription` 等 Backend callback |
| `serverapp/server.go` | 启动时从 DB 同步 defaultLLM |
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
ThinkingMode    string    — "" (auto) | "enabled" | "disabled"
IsDefault       bool      — 是否为该用户的默认订阅
PerModelConfigs map       — per-model overrides (见下)
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

- **启动时**: `server.go` 从 DB default subscription 创建客户端 → `SetDefaults(client, model)`
- **用户全局切换**: `SwitchSubscription` 对 `cli_user` 会同步更新 `defaultLLM`/`defaultModel`
- **作用**: SubAgent fallback（无指定模型时）、`ListModels()`、`GetLLM()` 最终 fallback
- **不覆盖的场景**: Feishu 用户（`senderID != "cli_user"`）不会影响 `defaultLLM`

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

用户在 TUI 用 Ctrl+N 或模型面板选中一个模型，该模型可能属于**另一个订阅**。

**TUI 端** (`cli_subscription.go:applyModelSwitch`):
1. `m.llmSubscriber.SwitchModel(senderID, model, chatID)` → RPC `switch_model`
2. `GetSessionSubscription(senderID, chatID)` 回读后端解析出的 owner 订阅，修正 `activeSubID`
3. `ResolveEffectiveMaxContext/Output` 重算上下文/输出上限
4. `SaveSessionLLMState()` 持久化 (ownerSubID, model) 到 Session JSON

**RPC 端** (`rpc_table.go` `switch_model`，chatID != "" 路径):
1. `ResolveSubscriptionForModel(bizID, model)` → 解析 owner 订阅（跳过 disabled 订阅/模型，默认订阅优先）
2. `SelectModel(owner.ID, model, chatID)` → 校验订阅/模型 enabled，写 tenants 表 (ownerSubID, model)，失效 sessionMemo
3. 解析失败才回退旧默认订阅路径

**订阅面板不再切换**：`cli_panel_quickswitch.go` "subscription" 模式只做 添加/禁用/删除（Enter=启停，E=编辑，D=删除）。旧的 `SwitchLLM` 异步切换 + `cliSwitchLLMDoneMsg` 流程仅保留给启动恢复（`scheduleSessionLLMRestore`）使用。

**对其他会话的影响**: 模型切换是 per-session 的（只写当前 chatID 的 tenants 行 + per-chat entry），不触碰全局默认，不影响其他会话。

### 场景 2: TUI 切换模型（Ctrl+L 选择器 / Ctrl+N 轮转）

两种互补入口：

- **Ctrl+L / 点状态栏模型名 / palette "Switch Model"** → 打开**可搜索模型选择器**：`ListAllModelEntries()` 列出全部可选模型（`订阅名 · 模型名`），输入框按订阅名/模型名子串过滤，↑↓ + Enter 选中，走 `applyModelSwitch`（同场景 1）。模型多时比轮转快得多。
- **Ctrl+N** → 快速轮转下一个模型（不开面板），`cycleModel` 用 `ListAllModels()`，复用 `applyModelSwitch`。

选中属于别的订阅的模型时，后端 `ResolveSubscriptionForModel` + `SelectModel` 自动配对 owner 订阅凭据。

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

**server.go 启动** (`server.go:548-572`):
1. `subSvc.GetDefault("cli_user")` → 查 DB
2. `SetDefaults(newClient, defSub.Model)` — 设置 defaultLLM，清空所有缓存
3. `SetUserMaxOutputTokens("cli_user", ...)` — 恢复 per-user 配置

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
| startup (`server.go`) | server | `SetDefaults`（全清+重设） | `SetUserMaxOutputTokens` + `SetUserThinkingMode` |

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

`scheduleSessionLLMRestore()` 必须使用 `m.cachedModelName`（由 `refreshCachedModelName` 从 tenants 表恢复的 per-session model），而非 `target.Model`（订阅默认 model）。否则用户通过 Ctrl+L 切换的 per-session model 在重启后丢失，回退到订阅默认 model。

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

### 9. ResolveLLM 不读遗留 entries map

agent loop 走 `ResolveLLM`（权威），它读 `sessionMemo` → tenants 表 → `user_default_model` → 遗留 `GetLLM`。`LLMFactory.SwitchModel` 写的 per-chat `f.entries` 对解析是死代码。新增 LLM 相关功能应基于 `ResolveLLM`/`SelectModel`，不要依赖 `entries`。

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
`SetSubscriptionEnabled` 改完必须 `InvalidateSubscription(subID)`，否则 `clientCache`/`sessionMemo` 复用旧 client。

### 14. 订阅面板不再切换；模型切换跨订阅必须回读 owner

`cli_panel_quickswitch.go` "subscription" 模式已移除切换逻辑（旧 `SwitchLLM` 异步 + `cliSwitchLLMDoneMsg` 仅留作启动恢复）。Enter 现在是启停订阅。模型切换（Ctrl+N / "model" 面板）跨订阅时，`applyModelSwitch` 在 `SwitchModel` 之后**必须**用 `GetSessionSubscription` 回读 owner 订阅修正 `activeSubID`——否则 `activeSubID` 停留在旧订阅，上下文上限/输出上限/settings 面板都显示错误订阅的配置。`cycleModel` 必须用 `ListAllModels()`（不是 `ListModels()`）才能跨订阅轮换。

### 15. 模型选择器用 `ListAllModelEntries`（带 owner 名）；过滤/状态栏都靠它

"model" 模式选择器必须用 `ListAllModelEntries()`（`[]{SubID,SubName,Model}`），**不要**用纯 `ListAllModels()` + CLI 本地拼装订阅名——本地拼装会和服务端 `ListAllModelEntriesForUser` 的 enabled 过滤逻辑分叉（禁用订阅/禁用模型口径不一致）。`ListAllModelsForUser` 已重写为 `ListAllModelEntriesForUser` 的薄包装，二者结果按位置一致，新增模型可见性逻辑只需改一处。

- 选择器有**过滤输入框**（`quickSwitchFilterInput` textinput）；`handleQuickSwitchKey` 在 "model" 模式必须先拦截 Esc/Up/Down/Enter（操作 `quickSwitchModelFiltered`），其余键喂给 textinput 再 `applyQuickSwitchFilter` 重建过滤视图。`applyQuickSwitch` 在 "model" 模式从 `quickSwitchModelFiltered[cursor]` 取（不是 `quickSwitchList`，model 模式下 `quickSwitchList` 是 nil）。
- **点状态栏模型名 = 打开选择器**（`cli_mouse.go` "modelName" → `openQuickSwitch("model")`），不再是 `cycleModel`；Ctrl+N 保留轮转。
- 状态栏 `订阅名 · 模型名` 靠 `cachedSubName`，由 `refreshCachedSubName` 在 `activeSubID` 变更路径（`applyModelSwitch` / `refreshCachedModelName` defer / `applySessionLLMState`）刷新——每次一次 `List("")` RPC，**View() 只读缓存**，绝不能在每帧 RPC。订阅重命名后状态栏订阅名可能短暂滞后（下次 activeSubID 变更或面板刷新才更新），可接受。

### 16. `OnModelsLoaded` 必须在 model-first 路径注入；选择器开面板必须实时刷新

model-first 重构曾把 `OnModelsLoaded` 回调丢线：`createClientFromSub`（及 entry 构造路径）构造 `UserLLMConfig` 时不设 `OnModelsLoaded`，导致每个订阅 client 即便异步拉到 `/models` 也不写回 `CachedModels`。症状：模型选择器/Ctrl+N 每个订阅只看到 `sub.Model`（一个）+ 手动 `subscription_models` 行，**provider 真实可用模型全部缺失**（"列表不全"）。修复：`makeOnModelsLoaded(subID)` 在三处 `createClient` 调用点注入（`createClientFromSub` + 两处 entry 构造），回调内 `Get(subID)` nil-check 后 `UpdateCachedModels`（`UpdateCachedModels` 对不存在的 subID 会 nil-deref，必须先 Get 守卫）。

光修回调还不够——`CachedModels` 只在订阅 client 被构造时才更新，"从没用过的订阅" / "provider 新增模型"仍是旧值。所以选择器开面板**必须**触发 `RefreshModelEntriesForUser`：并行（`sem` 容量 8）对每个 `Enabled && BaseURL && APIKey` 的订阅 `createClientFromSub` + `llm.ModelLoader.LoadModelsFromAPI`（8s 超时），失败软降级（保留旧 `CachedModels`），完成后返回最新 entries。CLI 侧异步：`openQuickSwitch("model")` 把 `refreshModelEntriesCmd` 推进 `m.pendingCmds`，三个入口（Ctrl+L / 点状态栏模型名 / palette Enter）都必须 drain `pendingCmds` 把 cmd 发出去；回包 `cliModelEntriesRefreshedMsg` 在 `Update` 顶层处理，替换 `quickSwitchModelEntries` 后 `applyQuickSwitchFilter`（**只夹紧光标，不重置**——否则后台刷新会把用户光标拽回顶部）。`applyQuickSwitchFilter` 因此只做 clamp，光标置位由调用方负责（open → `cursorToActiveModel`，typing → 0）。`backendModelLister.EnsureModelsLoaded` 是 no-op（远程模式服务端管缓存），刷新由 `RefreshModelEntries` 显式触发，别再依赖 `EnsureModelsLoaded`。
