# 计划：彻底删除 system 订阅 + 模型订阅一体化

> 生成时间：2026-08-30
> 状态：✅ 已完成并落地（v62）——全部 6 阶段执行完毕，Go 全量测试 + web vitest 999 测试全绿，golangci-lint 0 issues
> 用户决策（2026-08-30 确认）：Q1 直接删除行（非转正）；Q2 保留 cfg.LLM 内存兜底 defaultLLM；Q3 删除 is_system 列（迁移自动备份 DB）；Q4 全部执行（阶段 1-6）
> 探索依据：3 个并发 explore agent 审计（system 订阅机制 13 个 bug、模型裸名 15 个点、删除依赖 12 场景）

## 背景与目标

system 订阅（v44 引入：`user_llm_subscriptions` 行 `id="system"`, `sender_id="__system__"`, `is_system=1`，每次启动从 `cfg.LLM`/env reconcile）被用户判定"全是 bug"。审计证实：**13 个具体问题（B1-B13）中 11 个直接源于该机制本身**——守卫矩阵不全、双版本顺序相反、system 优先级打架、读写不对称、明文泄露、垃圾行、ID 注入、MergeUsers 过户、reconcile 清零 max_context、defaultLLM 被个人订阅覆写（多用户串号）。逐个修 = 打地鼠；机制本身是 bug 温床。

**目标**：
1. 彻底删除 system 订阅（DB 行 + is_system 列 + 全部代码点 + UI 🔒），凭据数据无损转正
2. 模型订阅一体化：**持久层与协议层中模型永远携带所属订阅 `(subID, model)`，裸模型名永不单独落库/落协议**；人机输入（模型名/tier 名）由解析层一次性配对

## 现状分析

### system 订阅的 bug 清单（审计结论，按严重度）

| ID | 问题 | 位置 |
|----|------|------|
| 🔴 B1 | storage 守卫矩阵不全：`SetModel`/`UpdatePerModelConfigs`/`UpsertModel`/`SetModelEnabled`/`SetModelMaxContext`/`SetModelMaxOutput`/`RemoveModel`/`Add` 均无 system 守卫。"给 system 模型改参数"三路径三结果：RPC `update_per_model_config` 拒绝 ✓ / agent `SetUserMaxContext` **静默写入** ✗ / RPC `updateSubscription` **先写后拒半提交** ✗ | `storage/sqlite/user_llm_subscription.go:645,669,783,868,889,907,925,470`；`serverapp/rpc_table.go:2734-2739` |
| 🔴 B2 | `listModelEntriesCoreByUserID` user-first vs `listModelEntriesCore`（senderID 版）system-first——同修复只做了一半，旧路径 maxModels 截断时用户模型被 system 挤掉 | `agent/llm_factory.go:1182-1191 vs 1280-1291` |
| 🔴 B3 | `ResolveSubscriptionForModel` find 闭包 `if sub.IsSystem { return sub }` system 优先——与 B2 方向打架：列表显示 user 优先、归属判定 system 优先 → legacy 只传模型名的 UI 解析到 system 凭证而非用户自己的 | `agent/llm_factory.go:934` |
| 🟠 B4 | `updateActiveSubFn` 读写不对称：settings 面板正常**显示** system 字段，保存被守卫拒绝报错 | `agent/agent.go:725 vs 748-759` |
| 🟠 B5 | reconcile 每次 boot 清零 system 订阅级 max_context（`ON CONFLICT SET max_context` 而构造侧恒 0） | `serverapp/server.go:1462-1469` + `storage:434` |
| 🟠 B6 | `getDefaultSubscription` uid==0 fallback 用 session key 查 sender → 永远 systemFallback，v45 遗留死路径 | `serverapp/rpc_table.go:2654` |
| 🟠 B7 | Feishu `LLMGetDefaultSubscription` 对 system 返回**明文 APIKey**（注释自认"用于编辑"但编辑必被拒）+ `/llms` 显示明文 BaseURL | `serverapp/callbacks.go:1970-1975` |
| 🟡 B8 | `SetDefault("system")` 产生 `sender_id='__system__'` 的 user_default_model 垃圾行 | `storage:628` |
| 🟡 B9 | `SetSystemLLM` 与 `SetDefaults` 实现完全相同（注释声称的差异已不存在）；启动序列 `GetDefault(cli_user)` 可能返回**个人订阅** → 覆写 cfg.LLM → defaultLLM 持有 cli_user 私人订阅作为全局兜底（多用户串号）；`SwitchSubscription` 的 cli_user 分支同样污染 defaultLLM | `agent/llm_factory.go:423-449, 365-380`；`serverapp/server.go:622-644` |
| 🟡 B10 | `UpsertSystemSubscription` 接受任意非空 ID（可注入）；`GetSystemSubscription` LIMIT 1 无 ORDER BY | `storage:411-413, 385` |
| 🟡 B11 | `MergeUsers` 不过滤 is_system → system 订阅可被"过户" | `agent/identity.go:487` |
| ⚪ B12/B13 | `GetDefault` 给 system 标 `IsDefault=true` 但 `Get` 不标（UI Active 投射不一致）；masked-key 只 log 不拒绝 | `storage:338`；`llm_factory.go:255-261` |

**结构性问题**（非编号 bug 但同样驱动力）：同一模型 `system · deepseek` 与 `deepseek · deepseek` 双行重复显示；每个用户的订阅列表被注入同一行；"系统默认"概念在 DB/内存/UI 三层语义漂移。

### 模型裸名点清单（一体化目标对象）

**存储/协议层（必须修）**：
- tier 配置 legacy 裸名格式：`SetUserTierModel` 允许 `subID==""` 只存 model（`agent/llm_config_handler.go:731-734`）
- `get_default_model` RPC 响应是裸字符串（`serverapp/rpc_table.go:790-806`）
- web `createSession` 只传裸 model（`web/src/stores/useSessionStore.ts:86`）
- `iteration_history.model` 无 subscription_id 列（`storage/sqlite/session.go:473-475`，v59 加列时漏）
- `GetLLMForModel` 三层裸名 fallback：`buildModelSubscriptionMap` 反查 / config-exact / **tier-fallback-config（任意订阅凭据 + 请求模型名）**——最激进的裸名兜底（`agent/llm_factory.go:1539-1550`）
- `Agent.SetUserModel` subID 为空的补偿路径（`agent/agent.go:999-1005`）

**已是配对范式（不动）**：`user_default_model`/`tenants`（含 model_id 外键）/`SessionLLMState`/`ModelEntry`/`ContextUsage`/`select_model` RPC/`RunConfig.SubID+Model`/`SelectModel` 签名。

**合法保留**：`user_llm_subscriptions.model`（订阅自身代表模型，订阅属性）；`defaultLLM` 内存兜底（部署级应急，不持久化不显示——见决策 D2）。

## 方案总览

两大板块，六阶段。**核心设计决策（用户已确认 2026-08-30）**：

- **D1 system 行直接删除**（用户选择，非转正）：v62 迁移直接 `DELETE FROM user_llm_subscriptions WHERE id='system' AND is_system=1`。凭据实际不丢——system 行每次启动被 reconcile 从 cfg.LLM 覆盖（config.json llm 段是原始来源），defaultLLM 重建即恢复。丢弃的只有 system 的 per-model 配置（subscription_models system 行）与 cached_models（可重拉）。引用清理：`user_default_model` 指向 system 的行 DELETE；`tenants.subscription_id='system'` 置空；`subscription_models` system 行 DELETE；tier 值 `system|model` 降级为裸名再反查补齐（用户订阅有同模型则补 `subID|model`，否则清空）。
- **D2 全局兜底回归内存**：`defaultLLM` 恒由 `NewLLMFactory(cfg.LLM client)` 初始值提供（v44 前形态）——config.json `llm` 段（+env 覆盖）是部署级兜底 LLM 的唯一定义处，**不进 DB、不进订阅列表、不进 picker、模型不可被选中**。`SwitchSubscription` 的 cli_user 同步分支与启动序列的 `GetDefault→cfg.LLM 覆写→SetSystemLLM` 段全部删除（消灭 B9 串号）。
- **D3 is_system 列删除**（用户确认）：v62 迁移 `ALTER TABLE DROP COLUMN` + schema.go 同步 + 全部代码点移除。迁移前自动备份 DB 文件。
- **D4 一体化边界**：持久层（DB 表/快照）与协议层（RPC 请求/响应）模型永远 `(subID, model)` 配对；人机输入（SubAgent 工具的 model 参数、tier 名）允许友好格式，但解析在入口一次完成（`ResolveSubscriptionForModel` 保留为唯一"模型名→订阅"解析器，从"存储补偿"降级为"输入解析"），解析结果永配对传递。

## 详细计划

### 阶段 1：DB 迁移 v62（storage/sqlite/migrations.go + schema.go + db.go）

> 用户决策：system 行**直接删除**（凭据不丢——system 行每次启动被 reconcile 从 cfg.LLM 覆盖，config.json llm 段是原始来源，defaultLLM 重建即恢复）

- [ ] 1.1 迁移前自动备份 DB 文件（`xbot.db` → `xbot.db.pre-v62.bak`，一次性 cp）
- [ ] 1.2 **引用清理 + 删除 system 行**（顺序：先清引用再删行）：
  - `DELETE FROM user_default_model WHERE subscription_id='system'`（指向 system 的默认删除，用户回退 defaultLLM 兜底）
  - `UPDATE tenants SET subscription_id='' WHERE subscription_id='system'`（保留会话，绑定为空走 ResolveLLM 重解析）
  - `DELETE FROM subscription_models WHERE subscription_id='system'`（孤儿清理）
  - `DELETE FROM user_identities WHERE channel='system' AND channel_user_id='__system__'`（只为 system backfill 服务）；`schema.go:318` 新库路径同步删该 INSERT
  - `DELETE FROM user_llm_subscriptions WHERE id='system' AND is_system=1`（最后删行）
- [ ] 1.3 **tier 引用清理（Go 侧遍历）**：`tier_vanguard/tier_balance/tier_swift` 值 `system|model` → 降级为裸名 `model` → `ResolveSubscriptionForModel` 反查用户订阅（有同模型 → 回写 `subID|model`；无 → 清空该 tier，fallback 链处理）；纯裸名 `model`（legacy）同样反查补齐或清空。**迁移后 tier 值要么是合法 `subID|model` 要么为空**
- [ ] 1.4 **is_system 列删除**：`ALTER TABLE user_llm_subscriptions DROP COLUMN is_system`（`columnExists` 幂等检查）；schema.go CREATE TABLE 同步移除；`userLLMSubscriptionSelectCols`/`InsertCols` 同步
- [ ] 1.5 **iteration_history 加 subscription_id 列**：`ALTER TABLE ... ADD COLUMN subscription_id TEXT DEFAULT ''`；schema.go 同步 + `INSERT INTO schema_version VALUES (62)`。⚠️ 按 AGENTS.md v59 教训：SELECT 加列必须同步**全部** Scan 路径（`AppendIterationHistory` INSERT、`scanIterationRecords`、`GetIterationHistoryByTurns` 内联 Scan）
- [ ] 1.6 `LLMSubscription` struct 删 `IsSystem` 字段、`scanSubscription`/`Add` 等同步
- [ ] 迁移测试：幂等（跑两次）、无残留 'system' 引用、新库路径（v62 直建无 is_system 列）

### 阶段 2：storage 层删除（storage/sqlite/user_llm_subscription.go）

- [ ] 2.1 删 `SystemSenderID`/`SystemSubscriptionName` 常量、`GetSystemSubscription`、`UpsertSystemSubscription`、`systemFallback`、`isSystemSubscription`、4 个守卫（Update/Remove/Rename/SetSubscriptionEnabled 的 system 检查——列已删，检查自然失效）
- [ ] 2.2 `List`/`ListAll`/`ListByUserID`：删 `OR is_system = 1` 注入 + `ORDER BY is_system DESC` → `ORDER BY created_at ASC`
- [ ] 2.3 `GetDefault`/`GetDefaultByUserID`：删 systemFallback（udm 未设置/悬空 → 返回 `nil, nil`，语义简化为"last-used"，无默认即无）
- [ ] 2.4 `SetDefault` 的 `SELECT sender_id WHERE id=?` 垃圾行路径（B8）随 SetDefault 语义收尾检查
- [ ] 2.5 测试更新：`subscription_test.go` TestSystemSubscription 系列、`subscription_default_model_test.go` 的 seedSystemSubscription fixture

### 阶段 3：serverapp/agent 层删除

- [ ] 3.1 `serverapp/server.go`：删 `reconcileSystemSubscription`（1451-1477）、启动序列的 system 段（608-617 migrate/seed + 622-644 GetDefault→cfg.LLM 覆写→createAdminLLM→SetSystemLLM）。`defaultLLM` 由 `NewLLMFactory` 初始值提供（已验证 `agent/llm_factory.go:71` 构造时接收）。thinking_mode seed 保留，来源改直读 `cfg.LLM.ThinkingMode`（不再经 defSub）
- [ ] 3.2 `migrateConfigSubscriptions` 简化：`userOwned := len(existing) > 0`（List 不再注入 system 行）
- [ ] 3.3 `agent/llm_factory.go`：
  - 删 `SetSystemLLM`（439-449）+ `SetDefaults`（423-433，无生产调用点，B9 双胞胎一起删）
  - `GetLLM`（246-286）：`GetDefault` 返回 nil → 自然落 `f.defaultLLM`（285 行已有兜底，改动近零）
  - `ResolveContextConfig` 第三级 `GetSystemSubscription`（218-227）→ 删（落 cfg 默认）
  - `HasCustomLLM` 的 `!sub.IsSystem` 过滤（348）删
  - `ResolveSubscriptionForModel` find 闭包 `if sub.IsSystem { return sub }`（934）删 → first-enabled by created order
  - `listModelEntriesCore`×2 的 system 两轮循环/顺序分支删（单轮 created_at）
  - `SwitchSubscription` 的 cli_user 同步 defaultLLM 分支（365-380）删——**defaultLLM 恒 = cfg.LLM**（D2，消灭 B9）
  - `resolveTierModel`/`parseTierValue` 裸名分支删（1699-1705 `("", s)` 返回；迁移 1.5 已补齐存量，写入 5a 已收口）
- [ ] 3.4 `agent/agent.go`：`updateActiveSubFn`/`getActiveSubFieldFn`（725/748-759）GetDefault nil 处理（B4 随 system 删除自然消失，验证 nil 路径不炸）
- [ ] 3.5 `agent/llm_config_handler.go`：`/set-llm` skip system（228）、`/llms` system label（415-417）、`/unset-llm` skip（795/816）删
- [ ] 3.6 `agent/identity.go` MergeUsers（487）：B11 随列删除自然消失，验证
- [ ] 3.7 **CLI `hasNoSubscription` 行为变化修正**（`channel/cli/cli_subscription.go:130-145`）：List 不再注入 system 行后，"无个人订阅但 config.json llm 段有效"的本地用户会被 `SetupNoLLM` 误拦（当前靠 system 行放行）。修正：本地模式（`sendInboundFn == nil`）下 `hasNoSubscription = 订阅列表为空 && cfg.LLM 未配置`（defaultLLM 有效即放行）；远程模式不检查（既有规则）

### 阶段 4：RPC/协议/UI 清理

- [ ] 4.1 `protocol/events.go:271` 删 `Subscription.IsSystem`；`subToChannel`（rpc_table:3111）同步
- [ ] 4.2 `serverapp/rpc_table.go`：`subOwnedByUser` 注释更新；`getDefaultSubscription` uid==0 死路径（2637-2664）删；`setSubscriptionModel` 的 `GetDefault("__system__")` 分支（2853）删
- [ ] 4.3 CLI `cli_panel_quickswitch.go`：`subIsSystem`/🔒 badge/toggle·edit·disable·delete 拒绝分支/userOwned 排除计数（352/364/407-417/429-439/484/657-668/991/1090）
- [ ] 4.4 Feishu `feishu_settings.go`：🔒 "系统内置" 渲染 + 明文 BaseURL（1639-1680）删（B7 消失）
- [ ] 4.5 测试 fixture：`llm_factory_modelfirst_test.go` 两处 system fixture、`agent`/`serverapp` 相关测试

### 阶段 5：模型订阅一体化

- [ ] 5.1 `SetUserTierModel` 写入收口（llm_config_handler.go:724-742）：subID 必填，空 → 报错（不再接受裸名）
- [ ] 5.2 `get_default_model` RPC 响应 `{sub_id, model}` 对象（rpc_table.go:790-806）+ `agent/client.go` 方法签名 + CLI/web 消费方
- [ ] 5.3 web `createSession` 带 `subscription_id`：前端 `useSessionStore.ts` 继承路径从 `get_context_usage`（协议已返回 subscription_id）同时取 subID+model 传给后端；`ChatCreate` 回调（callbacks.go:766-799）优先用传入 subID，`ResolveSubscriptionForModel` 从补偿降级为校验
- [ ] 5.4 `Agent.SetUserModel`（agent.go:999-1005）subID 必填，删补偿路径
- [ ] 5.5 `GetLLMForModel` 删三层裸名 fallback（llm_factory.go:1539-1550 tier-fallback-config / 1484-1494 config-exact / 1467 buildModelSubscriptionMap）：tier 解析 `(subID, model)` 直达 `Get(subID)`；裸模型名输入（SubAgent 工具参数）→ `ResolveSubscriptionForModel` 唯一解析点 → 找不到即报错（不再"任意订阅凭据+模型名"硬试）
- [ ] 5.6 `resolveSubIDForModel` 倒序消除（user_context.go:154）：`GetLLMForModel` 签名扩展返回 `(client, subID, model, ...)` 配对，SubAgent RunConfig 直接拿对
- [ ] 5.7 `iteration_history.subscription_id` 写入：`snapshotCompletedIteration` 从 `cfg.SubID` 取（engine_run.go）；`GetTenantUsageStats` by_model 改 `(subscription_id, model)` 复合分组；历史行 `''` 显示原样不回填
- [ ] 5.8 CreateChat/SubAgent 工具的 `model` 参数保持人机友好（模型名/tier 名，D4 边界），schema 描述补充说明"内部解析为 (订阅, 模型) 对"
- [ ] 5.9 `buildModelSubscriptionMap`（1580-1612）随 5.5 删除；`ResolveSubscriptionForModel` 保留（唯一输入解析器）

### 阶段 6：文档同步

- [ ] 6.1 AGENTS.md「Subscription & Settings」节：删 system 订阅段落（v44 描述、GetDefault fallback、saveServerConfig 注意事项改写），新增 defaultLLM 内存兜底语义 + 模型订阅一体化不变量
- [ ] 6.2 `docs/agent/subscription.md`：删「系统订阅：单源 LLM（v44）」节、GetDefault 语义（"last-used，无 system fallback"）、defaultLLM 描述、tier 格式（强制 `subID|model`）
- [ ] 6.3 `docs/plans/remove-default-subscription.md` 旧相邻计划标注 superseded
- [ ] 6.4 docs-site 中英同步（若有订阅/模型配置文档页）

## 验证方案

- **构建与测试**：`go build ./...`；`go test ./...`（重点 storage/agent/serverapp/channel）；web `vitest`（createSession 改动）
- **新库首跑**：无 system 行、无 is_system 列；无订阅时首消息走 defaultLLM（cfg.LLM）；CLI `hasNoSubscription` 引导
- **存量库迁移**：转正幂等（跑两次不重复）；`grep` 断言无残留 `subscription_id='system'` 引用；tier 值全部带 `|`
- **CLI**：`/llms` 无 system 行、Ctrl+N 面板无 🔒、无 "system · model" 双行、settings 面板改 LLM 不再报 "read-only"
- **Web/Feishu**：picker 无 system 订阅行；Feishu /models 卡片无 🔒"系统内置"；无订阅新用户消息走兜底或清晰报错
- **一体化回归**：tier 配置读写全带 subID；`get_default_model` 返回对象；iteration_history 新行带 subscription_id；by_model 聚合复合分组
- **SubAgent**：tier 指定（vanguard/balance/swift）解析到 (subID, model) 对；裸模型名 SubAgent 正常（经 ResolveSubscriptionForModel 解析）

## 回滚策略

- 迁移前 1.1 自动备份 `xbot.db.pre-v62.bak`——回滚 = 还原备份 + `git revert`
- `DROP COLUMN is_system` 不可逆但备份覆盖；旧二进制不兼容新 schema（SELECT is_static 报错）——**回滚必须同时还原 DB**
- 转正是 UPDATE（非 DELETE），凭据无损失

## 风险点

- **R1 现部署 web/飞书无个人订阅用户**：依赖 system 订阅作为共享凭据源。删除后走 defaultLLM（cfg.LLM，同一 config.json 来源，凭据相同）→ **行为等价**；但若这些用户需要"选中 system 名下的具体模型"（per-model 切换），转正后订阅归 admin，他们**看不到也选不了**。缓解：转正订阅归 admin 是数据归属正确化；共享需求应显式配订阅或依赖 cfg.LLM 兜底。**若现部署存在多用户共享 system 模型选择的场景，需提前确认**（见决策点 Q1）
- **R2 `GetDefault` 语义变化**：返回 nil 的调用方（`GetLLM`/`ResolveActiveSubModel`/serverapp 启动）需 nil-safe——阶段 3 逐一验证
- **R3 iteration_history 加列**：v59 教训（Scan 路径不同步 → 批量查询恒空）——1.7 列出全部三处 Scan 同步清单
- **R4 迁移时序**：tier 裸名补齐（1.5）依赖转正先完成（1.2-1.3），顺序不可换
- **R5 `tier-fallback-config` 删除**（5.5）：依赖"任意 OpenAI 兼容订阅 + 任意模型名"的 SubAgent 用法将报错。这是有意的（该 fallback 是裸名硬试，一体化后模型必须有明确归属），报错信息引导配 tier 或用带订阅的模型名

## 注意事项

- **不要用 storage 层方法做迁移**（Remove 守卫拒绝 is_system 行）——迁移直连 SQL
- **`GetLLM` 的 masked-key 只 log 不拒绝**（B13）本计划不动——独立问题
- **`user_llm_subscriptions.model` 列（订阅代表模型）保留**——它是订阅属性不是独立模型
- **cfg.LLM 仍是启动种子**：`migrateConfigSubscriptions`（config.json subscriptions[] → DB）保留；config.json `llm` 段只喂 defaultLLM 内存兜底，**不再落 DB**（saveServerConfig 不写回的既有规则不变）
- **defaultLLM 是唯一合法裸名例外**（D2）：部署级应急，不持久化、不显示、不参与模型选择——一体化不变量作用于持久层与协议层

## 待确认决策点

| # | 决策 | 推荐 | 备选 |
|---|------|------|------|
| Q1 | system 行处置 | **转正为 admin 个人订阅**（数据无损） | 直接删行（凭据丢弃，不推荐） |
| Q2 | 无订阅用户兜底 | **保留 cfg.LLM 内存 defaultLLM**（现部署无感） | 严格拒绝（无订阅报错引导配置） |
| Q3 | is_system 列 | **删除**（迁移自动备份 DB） | 保留恒 0（省 DROP COLUMN 风险） |
| Q4 | 一体化范围 | 存储层+协议层+解析收口（5.1-5.7） | 追加 CLI 内存字段合并（cachedModelName+activeSubID → 单结构体，可选低优） |

---

## 自审记录

- [x] 目标一致性：阶段 1-4 服务于删除 system 订阅，阶段 5 服务于模型订阅一体化（D4 边界：持久层/协议层严格配对，人机输入由解析层配对），阶段 6 文档同步。无偏离步骤
- [x] 遗漏检查（对照三份探索报告逐项核对）：12 场景依赖清单全部有对应步骤；15 个裸名点 A-O 全部处置（A→1.7/5.7、B→5.2、C→1.5/5.1、D→5.8、E→5.5、F→5.3、G→5.4、H→Q4 可选、I→5.7、J→5.7、K→5.9、L→5.6、M→合法保留、N→5.3、O→D2 例外）；自审补充 2 个遗漏：①`schema.go:318` 的 system identity INSERT（已补进 1.2）②CLI `hasNoSubscription` 行为变化（已补进 3.7）
- [x] 依赖检查：1.5 tier 补齐先于 5.1 写入收口（存量迁移→收口顺序）；1.7 加列与 5.7 写入配套；migration（SQL 直连）与 Go struct 删字段不冲突（v62 转正 SQL 先跑）
- [x] 风险评估：R1（现部署共享用户）已列决策点 Q1；hasNoSubscription 行为变化已修（3.7）
- [x] 计划自洽性：D2（defaultLLM 恒 cfg.LLM）与 3.1（删启动同步）/3.3（删 SwitchSubscription 分支）一致；D3（删列）与 1.6/2.x/4.1（struct/protocol 字段删除）一致

✅ 自审通过（2026-08-30，含 2 处补充修正）
