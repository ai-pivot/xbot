# 计划：Per-Model 配置 + Per-Session 模型隔离

> 生成时间：2026-05-12
> 状态：待确认

## 背景与目标

### 现状问题

1. **max_output_tokens 和 context_size 是订阅级的**：同一个 OpenAI 订阅下的 gpt-4o 和 gpt-4o-mini 共享同一个 max_output_tokens（默认 8192），无法单独调整。`model_contexts` 是全局配置，不能按订阅区分。
2. **Session 模型切换影响其他 session**：`SwitchModel()` 清除同一 senderID 下所有 per-chat 缓存，导致 session A 切换模型后，session B 的 LLM client 也被清除。
3. **Session 的订阅/模型选择不持久化**：`activeSubscriptionID` 和 `activeModel` 仅在内存中（`sessionState`），TUI 重启后丢失，回退到全局默认。

### 目标状态

1. **每个订阅下的每个模型可以独立设置 max_output_tokens 和 context_size**，订阅级值作为 fallback。
2. **每个 session 可以独立使用不同的订阅/模型**，互不影响，且跨 TUI 重启持久化。
3. **全局设置作为 fallback**：未单独配置时使用全局默认值。
4. **交互清晰**：用户在 TUI 中能直观地理解和操作这些设置。

---

## 现状分析

### 关键文件

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `storage/sqlite/user_llm_subscription.go` | DB 订阅结构体和 CRUD | 修改：添加 PerModelConfig 字段 |
| `storage/sqlite/migrations.go` | DB 迁移链 | 修改：添加 v31→v32 迁移 |
| `storage/sqlite/schema.go` | 新库建表 DDL | 修改：建表含新列 |
| `storage/sqlite/db.go` | schema 版本号 | 修改：31→32 |
| `config/config.go` | CLI config 结构体 | 修改：SubscriptionConfig 添加字段 |
| `agent/llm_factory.go` | LLM 工厂：缓存、解析、切换 | 修改：核心重构 |
| `agent/engine_wire.go` | RunConfig 构建 | 修改：读取 per-model 配置 |
| `channel/cli_model.go` | TUI Model：session 状态 | 修改：per-session 持久化 |
| `channel/cli_panel.go` | TUI 面板：Quick Switch | 修改：per-session 切换 |
| `channel/cli_helpers.go` | TUI 设置读写辅助 | 修改：设置项支持 |
| `channel/cli_session.go` | Session 管理 | 修改：持久化 session 模型选择 |
| `channel/i18n.go` | 国际化/设置 schema | 修改：添加设置项定义 |
| `channel/cli_types.go` | 类型定义 | 修改：Subscription 结构体 |

### 依赖关系

```
DB Schema (v32 migration)
    ↓
LLMSubscription.PerModelConfig (新字段)
    ↓
config.SubscriptionConfig.PerModelConfig (CLI 配置)
    ↓
LLMFactory.createClientFromSub / resolveModelConfig (读取 per-model 配置)
    ↓
buildBaseRunConfig (注入到 RunConfig)
    ↓
maybeCompress (使用正确的 max_tokens)

Session 持久化
    ↓
sessions.json (存储 per-session subscriptionID + model)
    ↓
restoreSession → 恢复 per-session LLM client
    ↓
SwitchModel/SwitchSubscription (只影响当前 session)
```

### 风险点

1. **SwitchModel 清除所有 per-chat 缓存**：当前行为会影响其他 session。修改为只清除当前 chatID 的缓存。
2. **JSON 列的序列化/反序列化**：需要处理空值、格式错误等边界情况。
3. **向后兼容**：没有 PerModelConfig 的旧订阅应该继续正常工作，使用订阅级默认值。
4. **TUI 交互复杂度**：需要在不过度复杂化 UI 的前提下提供 per-model 配置入口。

---

## 详细计划

### 阶段一：数据层 — Per-Model Config

**目标**：在订阅中存储每个模型的独立配置。

#### 步骤 1.1：定义 PerModelConfig 数据结构

**涉及文件**：
- `storage/sqlite/user_llm_subscription.go` — 添加 PerModelConfig 类型
- `config/config.go` — CLI 配置对应字段

**设计**：
```go
// PerModelConfig 单个模型的覆盖配置
type PerModelConfig struct {
    MaxOutputTokens int `json:"max_output_tokens,omitempty"` // 0 = 使用订阅默认
    MaxContext      int `json:"max_context,omitempty"`       // 0 = 使用订阅默认
}

// LLMSubscription 添加新字段
type LLMSubscription struct {
    // ... 现有字段 ...
    PerModelConfigs map[string]PerModelConfig `json:"-"` // key = model name
}

// CLI config.json 的订阅也添加
type SubscriptionConfig struct {
    // ... 现有字段 ...
    PerModelConfigs map[string]PerModelConfig `json:"per_model_configs,omitempty"`
}
```

#### 步骤 1.2：DB 迁移 v31→v32

**涉及文件**：
- `storage/sqlite/db.go:25` — schemaVersion 31→32
- `storage/sqlite/schema.go` — 建表 DDL 添加新列
- `storage/sqlite/migrations.go` — 迁移函数

**操作**：
1. 添加 `per_model_configs TEXT NOT NULL DEFAULT '{}'` 列到 `user_llm_subscriptions`
2. 更新 `scanSubscription()` 解析 JSON
3. 更新 `Add()` / `Update()` SQL 写入 JSON
4. 新库建表 DDL 包含新列

#### 步骤 1.3：LLMFactory 读取 Per-Model Config

**涉及文件**：
- `agent/llm_factory.go`

**操作**：
1. 修改 `createClientFromSub()` — 查找 per-model max_output_tokens
2. 修改 `resolveModelContext()` — 增加 per-model context 来源
3. 新增辅助方法 `resolvePerModelMaxTokens(sub, model)` 和 `resolvePerModelContext(sub, model)`

**优先级链**：
```
Per-Model Config → Subscription Default → Global Default
```

#### 步骤 1.4：config.json 订阅适配

**涉及文件**：
- `agent/llm_factory.go` — `configSubToLLMSubscription()` 转换时保留 PerModelConfigs
- `cmd/xbot-cli/main.go` — 初始化时传递配置

**验证**：
- 现有测试通过
- 旧订阅（无 PerModelConfig）行为不变
- 新订阅可以设置 per-model config 并正确生效

---

### 阶段二：Per-Session 模型隔离

**目标**：每个 session 独立使用不同的订阅/模型，互不影响。

#### 步骤 2.1：Session 模型选择持久化

**涉及文件**：
- `channel/cli_session.go` — 扩展 session 存储结构
- `channel/cli_model.go` — saveCurrentSession / restoreSession

**设计**：
1. 扩展 `~/.xbot/sessions/<hash>.json` 的 session 结构，添加：
```json
{
  "sessions": [
    {
      "name": "Agent-brave-fox",
      "chat_id": "/home/user/project:Agent-brave-fox",
      "created_at": "...",
      "subscription_id": "sub_xxx",
      "model": "gpt-4o"
    }
  ]
}
```

2. `saveCurrentSession()` 时写入 `activeSubscriptionID` 和 `activeModel` 到文件
3. `restoreSession()` 时从文件恢复，而不是仅从内存 map

#### 步骤 2.2：修改 SwitchModel 为 Per-Session

**涉及文件**：
- `agent/llm_factory.go` — `SwitchModel()` 只更新当前 chatID

**操作**：
1. `SwitchModel` 新增 `chatID` 参数
2. 只更新 `chatKey(senderID, chatID)` 的缓存，不清除其他 session
3. 更新 user-level `models[senderID]`（影响新 session 的默认值）
4. 更新 RPC 签名和所有调用点

#### 步骤 2.3：修改 SwitchSubscription 为 Per-Session

**涉及文件**：
- `agent/llm_factory.go` — `SwitchSubscription()` 已经支持 chatID
- `channel/cli_panel.go` — Quick Switch 传递 chatID
- `channel/cli_update_handlers.go` — 确认 chatID 传递

**操作**：
1. 确认 `SwitchSubscription` 已正确按 chatID 缓存
2. 确保 Quick Switch 完成后更新当前 session 的 `activeSubscriptionID`

#### 步骤 2.4：启动时恢复 Per-Session LLM

**涉及文件**：
- `channel/cli_model.go` — 启动恢复流程
- `channel/cli.go` — Start() 方法
- `cmd/xbot-cli/main.go` — 初始化逻辑

**操作**：
1. 启动时从 sessions.json 读取 session 的 subscriptionID + model
2. 调用 `SetSessionLLM` 恢复 per-session LLM client
3. 首次使用的 session 使用全局默认订阅

**验证**：
- 打开两个 session，各自切换不同模型
- 切换回另一个 session，模型保持各自的选择
- 重启 TUI，两个 session 各自恢复之前的模型
- 切换模型的操作不影响另一个 session 的 LLM 调用

---

### 阶段三：TUI 交互优化

**目标**：用户能清晰理解和操作 per-model 配置和 per-session 模型选择。

#### 步骤 3.1：Settings 面板添加 Per-Model 配置入口

**涉及文件**：
- `channel/i18n.go` — SettingsSchema 添加设置项
- `channel/cli_helpers.go` — mergeCLISettingsValues / persistCLISettingsValues
- `channel/cli_panel.go` — 面板渲染

**设计**：
在 Settings 面板中添加一个新的操作项 `"🎛️ Model Overrides"`（类似 `"📦 订阅管理"`），点击后弹出 per-model 配置面板。

该面板展示当前订阅的所有模型列表，每个模型可设置：
- Max Output Tokens
- Max Context Tokens

交互流程：
```
/settings → 选择 "🎛️ Model Overrides" → 弹出模型列表
  → 选择某个模型 → 弹出配置面板（max_output_tokens, max_context）
  → 保存 → 写入订阅的 PerModelConfigs
```

#### 步骤 3.2：状态栏显示增强

**涉及文件**：
- `channel/cli_view.go` — renderReadyStatus

**操作**：
1. 状态栏模型名旁显示订阅名（如果非默认订阅）
2. 当模型有 per-model override 时，显示小标记（如 `gpt-4o*`）

#### 步骤 3.3：Quick Switch 增强

**涉及文件**：
- `channel/cli_panel.go` — openQuickSwitch / viewQuickSwitch

**操作**：
1. Quick Switch 面板中，每个模型旁显示 per-model 配置摘要（如 `16k/128k`）
2. 选中的模型有明确标记

#### 步骤 3.4：Context Bar 信息增强

**涉及文件**：
- `channel/cli_view.go` — renderContextTopBorder

**操作**：
1. Context bar tooltip 显示当前 max_output_tokens 和 max_context_tokens 的来源
（全局默认 / 订阅默认 / per-model override）

---

### 阶段四：RPC 接口和远程模式适配

**目标**：确保远程模式（CLI → WebSocket → Server）也能支持 per-session 和 per-model 配置。

#### 步骤 4.1：RPC 方法更新

**涉及文件**：
- `agent/backend.go` — AgentBackend 接口
- `agent/backend_impl.go` — 方法实现
- `agent/rpc_table.go` — RPC 路由

**操作**：
1. `switch_model` RPC 添加 `chatID` 参数
2. 新增 `update_per_model_config` RPC（更新订阅的 per-model 配置）
3. 新增 `get_per_model_config` RPC（读取 per-model 配置）

#### 步骤 4.2：远程 CLI 适配

**涉及文件**：
- `cmd/xbot-cli/main.go` — remoteLLMSubscriber 实现

**操作**：
1. `remoteLLMSubscriber.SwitchModel` 传递 chatID
2. 远程模式的 session 持久化通过 WS API 读写

---

### 阶段五：测试和清理

#### 步骤 5.1：单元测试

- `storage/sqlite/schema_test.go` — 验证迁移链完整性
- `agent/llm_factory_test.go` — per-model config 解析
- `agent/llm_factory_test.go` — per-session 切换隔离性

#### 步骤 5.2：集成测试

- 多 session 同时运行，各自模型独立
- 订阅 per-model 配置正确传递到 LLM API 调用
- 重启后 session 模型恢复

#### 步骤 5.3：代码清理

- 移除 `LLMSubscription.MaxContext` 字段的死代码（它已不再使用）
- 清理 `configSubToLLMSubscription` 中的 `MaxContext: 0` 硬编码
- 更新 `docs/agent/llm.md` 和 `docs/agent/architecture.md`

---

## 验证方案

- **Per-Model Config**：在 TUI 中给同一个订阅的不同模型设置不同的 max_output_tokens，确认 API 调用中使用正确的值
- **Per-Session 隔离**：开两个 session，各自切换不同订阅/模型，确认互不影响
- **持久化**：重启 TUI 后，每个 session 恢复之前的订阅/模型选择
- **Fallback**：未设置 per-model config 时，使用订阅级默认值；未设置订阅级值时，使用全局默认
- **迁移**：旧 DB 升级后，现有订阅正常工作，per_model_configs 为空 JSON
- **编译**：`go build ./...` 无错误
- **测试**：`go test ./...` 全部通过
- **Lint**：`golangci-lint run ./...` 无新增 warning

## 回滚策略

1. **DB 迁移**：添加的列是 `DEFAULT '{}'` 的可选列，不影响现有查询。无需回滚。
2. **代码回滚**：如果出现问题，git revert 即可，新列不影响旧代码。
3. **Config.json**：新字段使用 `omitempty`，旧版本忽略不会报错。

## 注意事项

- `SwitchModel` 签名变更需要更新所有调用点（CLI 本地、CLI 远程、Server handler）
- `SwitchSubscription` 已支持 chatID，但需要验证所有路径都正确传递
- Session 持久化文件格式变更需要向后兼容（新字段用 omitempty）
- Per-model config 的 JSON 列需要处理格式错误的情况（降级到订阅默认值）
- `modelMaxOutputTokens()` 硬编码表（`llm/openai.go:548-604`）仍然在 API 调用时夹紧 max_tokens，这是正确行为，不需要修改
