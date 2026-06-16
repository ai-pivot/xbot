# Subscription & LLM Resolution

## Overview

xbot 的 LLM 配置分为 3 层：全局默认 → 用户级别订阅 → 会话级别覆盖。
每个会话最终使用哪组 LLM 凭据/模型/参数，由 `LLMFactory` 运行时决定。

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
MaxContext       int — max_context_tokens（上下文窗口）
MaxOutputTokens  int — max_output_tokens（单次输出上限）
```

查找键：`PerModelConfigs[modelName]`

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

### 场景 1: TUI 订阅面板切换（per-session）

用户在 TUI 订阅面板选择一个新订阅。

**TUI 端** (`cli_panel.go:2814-2853`):
1. 立即更新 `activeSubID` / `cachedModelName` / `cachedMaxContextTokens`
2. `SaveSessionLLMState()` 持久化到 Session JSON
3. 异步调用 `mgr.SetDefault(subID, chatID)` → RPC with chatID
4. 异步调用 `switchFn(provider, url, key, model)` → 创建新 LLM 客户端

**RPC 端** (`rpc_table.go:1334-1346`，chatID != "" 路径):
1. `SetSessionLLM(bizID, chatID, sub)` → 设置 `entries["cli_user:chatID"]`
2. `SetTenantSubscription()` → 持久化到 tenants 表

**回调** (`cli_update_handlers.go:1617-1644`):
1. `mgr.SetDefault(subID, m.chatID)` — 再次确认 per-session（幂等）
2. **`mgr.SetDefault(subID, "")`** — 更新全局默认（让新会话继承）
3. `SaveSessionLLMState()` — 持久化
4. `RefreshValuesCache(subID)` — 更新 TUI settings 缓存

**关键**: 步骤 2 的全局 `SetDefault("")` 调用 RPC `setDefaultSubscription` 的全局路径：
- `svc.SetDefault(id)` — 更新 DB is_default 标记
- `InvalidateSender(bizID)` — **只**清除 user-level entry，**保留**所有 per-chat entries
- `SwitchSubscription(bizID, sub, "")` — 更新 user-level entry + defaultLLM/defaultModel

**对其他会话的影响**:
- ✅ 有 per-session 订阅的会话：不受影响（per-chat entry 保留）
- ✅ 没有 per-session 订阅的会话：使用新的全局默认（合理行为）
- ✅ SubAgent fallback：跟随新的 defaultLLM（用户级别偏好已更新）
- ✅ 新会话：继承全局默认订阅

### 场景 2: TUI 快速切换模型（Ctrl+L）

在当前订阅内切换模型（不换订阅）。

**路径** (`cli_model.go:210-237`):
1. `llmSubscriber.SwitchModel(senderID, nextModel, chatID)` → RPC
2. `SaveSessionLLMState()` 持久化

**RPC 端** (`SwitchModel` with chatID):
1. 更新 `entries["cli_user:chatID"]` 的 model 字段
2. 清除 client（懒重建）
3. 持久化到 DB 默认订阅的 Model 字段

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
