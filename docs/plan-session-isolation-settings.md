# 计划：Session 隔离优化 + 设置系统重构

> 生成时间：2026-05-08
> 状态：待确认

## 背景与目标

### 核心目标
1. **Session 隔离**：不同 session 有完全独立的状态（输入框内容、Todo、AskUser），切换不丢失，切回恢复
2. **设置系统优化**：max_tokens/context_length/thinking 等参数和模型绑定，订阅添加时要求设置默认值；每个 session 独立记录激活的订阅和模型；新建 session 继承当前 session 配置
3. **配置安全**：杜绝 config.json 被零值覆盖的可能性

### 当前状态
- Session 已有基础的 DB 层隔离（消息、记忆按 tenant 隔离）
- 但 TUI 运行时状态（输入框内容、Todo、AskUser panel）是全局共享的
- 设置系统缺少 per-session 的模型配置存储
- 添加订阅时表单缺少 max_output_tokens/thinking_mode/max_context 输入

## 现状分析

### 关键文件
| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `channel/cli_model.go` | TUI 核心 Model 定义、sessionState save/restore | 修改 |
| `channel/cli_session.go` | Session 列表 JSON 持久化 | 修改 |
| `channel/cli_panel.go` | Settings/Sessions/AskUser 面板 UI | 修改 |
| `channel/cli_message.go` | 消息渲染、AskUser 打开入口 | 修改 |
| `channel/cli_update_handlers.go` | 按键处理、slash 命令 | 修改 |
| `channel/cli.go` | CLI TUI 入口、bgTask 回调注册 | 修改 |
| `channel/cli_types.go` | CLI 数据类型定义、bgSessionKey | 修改 |
| `tools/todo.go` | TodoManager 实现 | 修改 |
| `tools/task_manager.go` | BackgroundTaskManager 实现 | 不改 |
| `tools/ask_user.go` | AskUser 工具定义 | 不改 |
| `agent/llm_factory.go` | LLM 客户端工厂、model resolution | 修改 |
| `agent/engine_wire.go` | RunConfig 构建 | 修改 |
| `config/config.go` | 配置数据结构、加载/保存 | 修改 |
| `storage/sqlite/` | DB schema、user_llm_subscription 表 | 修改 |
| `main.go` | CLI 入口、saveCLIConfig、subscriptionManager | 修改 |

### 依赖关系
```
cliModel (TUI 状态中心)
  ├── chatID/channelName → session 标识
  ├── textarea → 输入框（需隔离）
  ├── panelMode/panelItems → AskUser panel（需隔离）
  ├── savedSessions → 运行时状态快照（需扩展）
  ├── todo → (间接通过 agent，需隔离)
  ├── progress/SubAgents → SubAgent 进度树（已有隔离，保持）
  └── bgTaskCountFn/bgTaskListFn → 后台任务（需动态 sessionKey）

LLMFactory
  ├── GetLLMForChat(senderID, chatID) → 需支持 session override
  └── createClient → max_tokens 创建时固定（需支持动态）

CLIChannel
  ├── bgSessionKey → 后台任务 session 键（需动态化）
  └── bgTaskMgr → BackgroundTaskManager 引用（已有）

Config
  ├── config.json → 全局配置（需防覆盖）
  └── SQLite user_llm_subscriptions → per-user 订阅（需加字段）
```

### 风险点
- **输入框内容丢失**：切换 session 时 `textarea` 状态不保存/恢复，`inputHistory` 也是全局共享
- **AskUser 跨 session 泄露**：panelMode="askuser" 是全局的，切 session 后仍显示旧 session 的问题
- **Todo 无持久化**：进程重启后丢失；且 TUI channel 层无法直接访问 `TodoManager`（在 agent/ 层）
- **配置覆盖**：`local_transport.go:816` 直接 SaveToFile 无 merge；`configSubscriptionManager.Add` 丢失 MaxOutputTokens/ThinkingMode
- **max_tokens 绑定时机**：client 创建时固定，per-session 不同 max_tokens 需要重建 client
- **TodoManager 访问路径缺失**：TUI `switchToSession` 需要触发 Todo save/load，但 CLIChannel 当前没有 TodoManager 引用
- **后台任务 bgSessionKey 固定**：`bgSessionKey` 在 TUI 启动时设置一次，切换 session 不会更新，导致 Tasks 面板显示旧 session 的任务
- **Interactive Agent 列表全局共享**：`agentListFn` 回调不带 session 参数，Tasks 面板显示所有 session 的 agents

## 详细计划

> **推荐执行顺序**：阶段四（配置安全，独立且关键）→ 阶段二（订阅参数扩展）→ 阶段三（per-session 模型，依赖阶段二的新字段）→ 阶段一（UI 隔离，相对独立但可以在最后整合）

### 阶段一：Session 运行时状态隔离

- [ ] **1.1 输入框内容隔离** — `channel/cli_model.go`
  - 在 `sessionState` struct 中增加 `textareaValue string` 字段
  - `saveCurrentSession()` 保存当前 `m.textarea.Value()` 到 `sessionState`
  - `restoreSession()` 恢复时调用 `m.textarea.SetValue(state.textareaValue)`
  - 同时保存 `inputHistory` 和 `inputHistoryIdx`（当前是全局的，也需隔离）

- [ ] **1.2 Todo 按 session 持久化** — `tools/todo.go` + `channel/cli_types.go` + `channel/cli_model.go`
  - `TodoManager` 增加 `SaveToFile(sessionKey)` / `LoadFromFile(sessionKey)` 方法
  - 临时状态存 JSON：`~/.xbot/todos/<sessionKey_hash>.json`
  - `CLIChannelConfig` 增加 `TodoManager *tools.TodoManager` 字段（注入路径）
  - `switchToSession` 中：保存旧 session todo → 加载新 session todo
  - `saveCurrentSession` 触发自动保存（不仅是切换时，进程正常退出时也保存）
  - 加载是幂等的（如果文件不存在，从空开始）

- [ ] **1.3 AskUser 与 session 强绑定** — `channel/cli_model.go` + `channel/cli_panel.go` + `channel/cli_message.go`
  - 在 `sessionState` 中增加 `pendingAskUser` 字段（含 questions、callbacks 序列化）
  - 打开 AskUser panel 时，记录当前 sessionKey 到 `m.activeAskUserSession`
  - 切换到其他 session 时：隐藏 AskUser panel（保存状态到 savedSessions），切换完成后再检查新 session 是否有待处理的 AskUser
  - 切回原 session 时：恢复 AskUser panel 显示
  - **关键**：`onAnswer` 回调必须校验回答的 session 匹配当前活跃 session

- [ ] **1.4 后台任务按 session 隔离** — `channel/cli.go` + `channel/cli_types.go` + `channel/cli_panel.go`
  - `CLIChannel.bgSessionKey` 改为动态计算：每次使用时从当前 `m.chatID` 派生，而非启动时固定
  - `updateBgTaskCountFn()`（`cli.go:680-695`）的闭包改为引用 `c.bgSessionKey` 而非捕获局部变量
  - `switchToSession` 中：切换后更新 `bgSessionKey` 为新的 `"cli:" + chatID`，触发 bgTask 列表刷新
  - `listBgTasks()` 使用动态 key：`"cli:" + m.chatID`
  - Tasks 面板打开时，如果 `bgSessionKey` 已过期，重新查询

- [ ] **1.5 Interactive Agent 列表按 session 隔离** — `channel/cli.go` + `channel/cli_panel.go`
  - `agentListFn`/`agentCountFn` 回调增加 session 过滤参数（需要 engine 侧支持或 TUI 侧后过滤）
  - **优先方案**：TUI 侧后过滤 — 从 `agentListFn()` 返回的全量列表中，按 `ParentChatID`（即父 session 的 chatID）过滤
  - `panelAgentEntry` 已有 `ParentID` 字段（即父 session chatID），可直接用于过滤
  - Tasks 面板只显示属于当前 session 的 agents（`entry.ParentID == m.chatID`）
  - SubAgent progress 树（`CLIProgressPayload.SubAgents`）已有隔离，无需改动

### 阶段二：设置系统优化 — 模型绑定参数

- [ ] **2.1 扩展 SubscriptionConfig 数据结构** — `config/config.go`
  - `SubscriptionConfig` 增加 `DefaultMaxContext int` 和 `DefaultMaxOutputTokens int` 字段
  - 当模型没有自己的 `max_context`/`max_output_tokens` 时使用默认值
  - 注意 `json:"...,omitempty"` tag 行为，0 值不会被写入，需用指针或单独处理

- [ ] **2.2 扩展 LLMSubscription DB 结构** — `storage/sqlite/user_llm_subscription.go`
  - `LLMSubscription` 增加 `DefaultMaxContext int` 和 `DefaultMaxOutputTokens int` 字段
  - 同时增加 `DefaultThinkingMode string` 字段
  - 更新 DB schema migration

- [ ] **2.3 订阅添加表单增加设置项** — `channel/cli_panel.go` + `main.go`
  - `addSchema` 增加 `max_output_tokens`（number 型）、`max_context`（number 型）、`thinking_mode`（select 型，选项：auto/enabled/disabled）
  - `configSubscriptionManager.Add()` 补充 `MaxOutputTokens`/`ThinkingMode`/`MaxContext` 字段复制（修复已知 bug）
  - localSubscriptionManager 的 `Add` 也同步补充

- [ ] **2.4 编辑订阅时也支持修改这些参数** — `channel/cli_panel.go`
  - `editSchema` / `editQuickSwitchEntry` 增加相同的三个字段

### 阶段三：Per-Session 模型配置

- [ ] **3.1 Session 记录当前订阅和模型** — 数据层
  - 方案选择：不修改 SQLite schema（避免 migration 复杂性），改用 TUI 层的 JSON 持久化
  - 在 `~/.xbot/sessions/<hash>.json` 的 `dirSession` 中增加 `ActiveSubscriptionID string` 和 `ActiveModel string` 字段
  - 或者，在 `sessionState` 中增加这两个字段（运行时内存 + JSON 文件临时持久化）
  - **决定**：用 `sessionState`（TUI 层），与输入框内容一起保存/恢复

- [ ] **3.2 LLMFactory 支持 per-chatID model override** — `agent/llm_factory.go`
  - 已有 `SetSessionOverride(sessionKey, subID, model)` 方法（需新增）
  - `GetLLMForChat` 中先查 session override → 再走现有 user/cli 缓存链
  - Session override 存在时，强制 bypass per-chat 缓存，创建新 client（因为 max_tokens 可能不同）
  - 新增 `InvalidateSessionOverride(sessionKey)` 用于 session 切换时清理

- [ ] **3.3 TUI 切换 session 时同步 LLM 配置** — `channel/cli_model.go` + `channel/cli_panel.go`
  - `switchToSession` 中，从 `sessionState` 读取 `ActiveSubscriptionID`/`ActiveModel`
  - 调用 `LLMFactory.SetSessionOverride(newSessionKey, subID, model)` 设置新 session 的 LLM 配置
  - 同时清理旧 session 的 override（如果需要）
  - 新建 session 时，从当前 session 的 `sessionState` 继承 `ActiveSubscriptionID`/`ActiveModel`

- [ ] **3.4 Ctrl+P/Ctrl+N 切换时更新 sessionState** — `channel/cli_panel.go`
  - `openQuickSwitch` 的 onSelect 回调中，更新当前 sessionState 的 `ActiveSubscriptionID`/`ActiveModel`
  - `cycleModel` 中同理

### 阶段四：配置覆盖防护

- [ ] **4.1 修复 `configSubscriptionManager.Add` 字段丢失** — `main.go`
  - 补充 `MaxOutputTokens`、`ThinkingMode`、新增 `MaxContext` 字段的复制

- [ ] **4.2 `SaveToFile` 增加零值 slice 保护** — `config/config.go`
  - `mergeJSONPreserveUnknown` 的 `deepMergeJSON` 中：当 structVal 是 JSON `null` 时，保留 existing 值
  - 同时处理空 slice `[]` 的情况（当 Go struct 的 nil slice 序列化为 `null` 但 omitempty 不生效的边界情况）

- [ ] **4.3 `local_transport.go` 增加 merge 保护** — `local_transport.go`
  - 改为先 `LoadFromFile` 再 merge 的模式（与 `saveCLIConfig` 一致）
  - 或在调用处确保传入的 config 是从磁盘加载的非零值副本

- [ ] **4.4 `saveCLIConfig` 增加 subscriptions 零值守护** — `main.go`
  - 当 `merged.Subscriptions` 为空且 `cfg.Subscriptions` 也为空时，不要写入空的 subscriptions

### 阶段五：验证

- [ ] **5.1 编译验证** — `go build ./...`
- [ ] **5.2 现有测试通过** — `go test ./...`（重点：channel/ session/ agent/ config/）
- [ ] **5.3 TUI 模拟测试** — 编写回归场景验证 session 隔离
  - 场景：创建 session A，输入内容，切换到 session B，输入内容，切回 A，验证输入内容恢复
  - 场景：session A 触发 AskUser，切换到 session B，验证 AskUser 不显示，切回 A 验证恢复
  - 场景：session A 创建后台任务，切换到 session B，验证 Tasks 面板不显示 A 的任务
- [ ] **5.4 配置覆盖回归测试** — 验证多次保存不会丢失 subscription 字段

## 验证方案

1. **Session 输入隔离**：手动测试 — 在 session A 输入 "hello A"，切到 B 输入 "hello B"，切回 A 确认输入框显示 "hello A"
2. **Todo 隔离**：手动测试 — 在 session A 中让 agent 创建 Todo，切到 B 确认 TodoList 为空，切回 A 确认恢复
3. **AskUser 隔离**：手动测试 — 在 session A 触发 AskUser，切换到 B 确认不显示，切回 A 确认恢复
4. **后台任务隔离**：手动测试 — 在 session A 创建后台任务（如 sleep 60 &），切换到 B 确认 Tasks 面板为空，切回 A 确认任务仍在
5. **Interactive Agent 隔离**：手动测试 — session A 的 SubAgent 运行中，切换 B 确认 Tasks 面板无 session A 的 agent
6. **新建 session 继承配置**：手动测试 — session A 设置订阅 X/模型 Y，新建 session B，确认配置与 A 一致
7. **配置覆盖**：代码层面 — 验证 `saveCLIConfig` 和 `local_transport` 路径不会被零值覆盖

## 回滚策略

- 所有改动集中在有限文件中，可通过 `git revert` 回滚
- DB schema migration 采用 additive 方式（加字段不加 `NOT NULL`），回滚无需数据迁移
- JSON 临时文件（todos/、扩展的 sessions/）删除后不影响核心功能

## 注意事项

- **不要修改 SQLite tenant schema**：加字段容易引发 migration 问题，优先用 TUI 层 JSON 持久化
- **保持 subGeneration 防竞态机制**：修改 subscription 相关逻辑时必须维护 `subGeneration` 递增
- **max_tokens 的 client 重建成本**：per-session 不同 max_tokens 需要每次重建 client，需要注意性能
- **sessionState 序列化**：`sessionState` 目前是纯内存的（含 callback 函数指针），持久化时需要拆分为「可序列化数据」+「重建逻辑」
- **不要全局加 debug log**：用户明确禁止
- **遵循现有代码风格**：观察周围代码的命名、错误处理方式

---

✅ 自审通过

