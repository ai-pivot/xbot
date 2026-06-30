# Plan: 移除"默认订阅"和"当前订阅"概念

## Summary

订阅只是模型来源（凭证+endpoint），用于拉取模型列表。模型标识永远是 `(subscription, model)` 二元组。
移除"默认订阅"（`user_default_model` 表、`GetDefault`、`IsDefault`/`Active` 投射）和"当前订阅"概念（`activeSubID` 作为用户可见字段）。
新会话从全局模型列表选择一个 `(sub, model)` 二元组，而非继承"默认订阅"。

**不变**：模型标识仍是 `(subID, model)`，`tenants` 表仍存 `(subscription_id, model)`，`SelectModel` 仍接收 `(subID, model)`。

## Changes

### 数据层

#### `storage/sqlite/user_llm_subscription.go`
- **移除 `user_default_model` 表的使用**：`GetUserDefaultModel`/`SetUserDefaultModel`/`ClearUserDefaultModel` 改为废弃（保留函数签名 but 内部 no-op + log warning，避免编译断裂）
- **移除 `IsDefault`/`Active` 投射**：`markDefaultsFor`/`markDefaultsAll` 改为 no-op；`GetDefault` 废弃（改为返回 `GetSystemSubscription` — 仅系统订阅兜底）
- **`Add`**：不再 `seed user_default_model`（移除 `If IsDefault, SetUserDefaultModel` 逻辑）

#### `storage/sqlite/tenant.go`
- `SetTenantSubscription(channel, chatID, subID, model)` — 不变，仍写 `(subscription_id, model)`

### 解析链

#### `agent/llm_factory.go`

**`ResolveLLM`** — 解析链简化为 3 步：
1. sessionMemo → return
2. tenants 表 → `(subID, model)` → return
3. 系统订阅兜底（GetLLM 函数 / defaultLLM）
- **移除步骤 3（`GetUserDefaultModel`）** — 不再有用户级默认

**`ResolveActiveSubModel`** — 同步简化：sessionMemo → tenants → 系统订阅兜底
- **移除 `GetDefault` fallback** — 改为系统订阅兜底

**`ResolveSubscriptionForModel`** — tie-break 改为：系统订阅优先 → 第一个 enabled 订阅（按创建顺序）
- 移除 `sub.IsDefault` 优先逻辑

**`SelectModel(senderID, chatID, channel, subID, model)`** — 签名不变，仍写 tenants `(subID, model)`

**`SwitchModel`** — 遗留路径，保留但标注 deprecated（仅 RPC fallback 用）

### 新会话创建

#### `channel/cli/cli_panel_sessions.go` — `showSessionCreateDialog`
- 新建会话时：**继承用户最近使用的 `(subID, model)`**，数据源是 `tenants` 表按 `last_active_at DESC` 的第一条非空记录
- 写入新会话的 tenants 行 `(channel, newChatID, subID, model)`
- 找不到任何历史会话 → 用系统订阅兜底
- **不弹模型选择器**——用户直接开始聊天，模型从最近使用继承

#### `channel/cli/cli_session.go` — `SessionLLMState`
- `SubscriptionID` 字段保留（仍是 `(sub, model)` 二元组的一部分），但不再叫"当前订阅"
- 新建会话时不再 `SaveSessionLLMState` 继承父状态

### 命令

#### `agent/llm_config_handler.go`

**`/set-llm`** (`handleSetLLM`):
- 创建/更新订阅 = 添加一个模型来源
- 不再标记 `IsDefault`、不再 `SetUserDefaultModel`
- 创建后返回"已添加订阅 X，可用 /models 查看模型列表"

**`/unset-llm`** (`handleUnsetLLM`):
- 改为 `/del-llm <名称>` — 按名称删除指定订阅（不再是"清除默认"）
- 删除后检查 tenants 表中是否有会话绑定此订阅 → 自动回退到系统订阅

**`/set-model`** (`handleSetModel`):
- 改为 per-session：写 tenants 表（`SelectModel`），不再是用户级（不再写 `user_default_model`）
- 接受 `订阅名 · 模型名` 或纯模型名（后者用 `ResolveSubscriptionForModel` 解析 owner）

**`/models`** (`handleModels`):
- 不变 — 已显示 `订阅名 · 模型名` 格式

### TUI

#### `channel/cli/cli_panel_quickswitch.go`
- Ctrl+N 面板 — 不变，已显示 `订阅名 · 模型名`，`applyModelSwitch` 已用 `SelectModel(subID, model, chatID)`
- 移除 `activeSubID` 的"当前订阅"语义 — 改名为 `sessionSubID`（纯内部字段，标识会话当前绑定的订阅，不向用户暴露"当前订阅"概念）
- 状态栏：显示 `订阅名 · 模型名`（信息性，不是"当前订阅"概念）

#### `channel/cli/cli_panel_settings.go`
- `subscription_manage` 条目 — 保留（管理订阅凭证）
- 移除订阅行的 "默认" badge（`sub.Active`/`sub.IsDefault` 不再有用户可见含义）

#### `channel/cli/cli_subscription.go`
- `activeSubID` → `sessionSubID`（重命名，纯内部）

### 清理

#### `channel/protocol/events.go`
- `Subscription.Active` 字段 — 废弃标记（保留字段，不再设置）

#### `storage/sqlite/user_llm_subscription.go`
- `LLMSubscription.IsDefault` — 废弃标记（保留字段，永远 false）

## Risks

- **新建会话必选模型**：当前新会话静默继承默认订阅 → 改为弹模型选择器可能影响 `/chat new` 等快速创建流程。缓解：提供"跳过"选项，跳过则用系统订阅兜底
- **`GetDefault` 广泛引用**：全项目搜索 `GetDefault` / `IsDefault` / `Active`，需逐一替换/移除。缓解：分阶段，先改 agent 层，再改 CLI 层
- **DB 迁移**：`user_default_model` 表保留不删（避免迁移风险），代码层面废弃即可
- **`/unset-llm` 改为 `/del-llm`**：是 breaking change（IM 命令）。缓解：保留 `/unset-llm` 作 alias

## Definition of Done

- [ ] `ResolveLLM` 不再读 `user_default_model` 表
- [ ] `GetDefault` 不再被 ResolveLLM/ResolveActiveSubModel 调用（仅系统订阅兜底保留）
- [ ] `IsDefault`/`Active` 不再被任何代码设置为 true
- [ ] 新建 CLI 会话弹模型选择器（或跳过用系统兜底）
- [ ] `/set-llm` 不再设置默认订阅
- [ ] `/unset-llm` 按名称删除指定订阅
- [ ] `/set-model` 改为 per-session（写 tenants 表）
- [ ] Ctrl+N 面板行为不变（已是 `(sub, model)` 选择）
- [ ] 状态栏显示 `订阅名 · 模型名`（信息性）
- [ ] `go build ./...` + `go test ./...` 通过

## Open Questions

- `/set-model` 接受 `订阅名 · 模型名` 格式还是分别传 `订阅名` 和 `模型名` 两个参数？（倾向单参数 `"模型名"`，用 `ResolveSubscriptionForModel` 解析 owner，保持和现状一致）
