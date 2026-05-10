# 计划：AI-Native TUI & 配置系统

> 生成时间：2026-05-08（Round 2 修正版）
> 状态：待确认
> 来源：两轮 Roundtable（5 专家收敛） + 6 路 Explore Agent 代码探索

## 背景与目标

让 AI 能够**像用户一样操作 xbot CLI TUI**——在侧边栏切换/关闭 SubAgent session、调整布局、切换主题、修改配置。实现真正的 AI-native 体验。

**核心能力**：
1. 侧边栏 session 管理：切换焦点、关闭 session
2. TUI 布局调整：侧边栏宽度、面板比例
3. 主题管理：切换/创建主题
4. 配置管理：读取/修改非敏感的运行时配置
5. ~~Agent 定义管理~~ → 已有 agent-creator skill，**不新增工具**
6. ~~Session 创建~~ → 已有 CreateChat/SubAgent 工具（SpawnInteractive），**不新增工具**

## 架构决策（Roundtable 两轮收敛）

### 工具设计

```
┌────────────────────────────────────────────────┐
│ 核心工具（零增长，18个不变）                       │
│  SubAgent 描述追加 1 行面包屑（+20 tokens）       │
│  "For TUI/layout/theme: load tui_control.       │
│   For config: load config."                     │
├────────────────────────────────────────────────┤
│ 非核心工具（load_tools 按需激活）                  │
│  tui_control  (~280 tokens)                     │
│   - switch_session / close_session               │
│   - set_layout / set_theme                       │
│  config       (~200 tokens)                      │
│   - get / set (白名单内的非敏感配置)               │
└────────────────────────────────────────────────┘
```

**Session 创建**：复用 `CreateChat` 工具（底层 `SpawnInteractive`），自动在 sidebar 显示。
**Agent 定义**：复用 `agent-creator` skill，在 `~/.xbot/agents/` 下操作。

### 通信机制

```
tool.Execute() → cliTuiActionMsg{action, params, result chan}
              → asyncCh (buffered-64) → handleAsyncDrain
              → program.Send() → BubbleTea Update()
              → 分派到 switchToSession / applyLayoutConfig / applyThemeAndRebuild
              → result ← 写回 channel → tool 返回
```

全部复用现有 `asyncCh + program.Send` 管道，零新通信层。

### 三级安全模型

| 层级 | 操作 | 行为 | 示例 |
|------|------|------|------|
| **Layer 0** — 瞬态TUI | switch_session, set_layout, set_theme | AI 自由执行，即时生效，无确认 | 切到 reviewer session，调侧边栏宽度 |
| **Layer 2** — 持久化偏好 | config set（白名单内） | 审批确认后写入 config.json | 改 default_model, context_mode |
| **Layer 3** — 敏感配置 | api_key, provider, base_url | **硬阻止**，返回"请手动配置" | LLM密钥、计费相关 |

**close_session**：唯一需要 Layer A 确认的操作（Toast Y/N），main session 硬禁止关闭。

### Session 安全约束
- 上限：`max_sessions=20`（可配置 5-100），每 turn 最多创建 2 个
- main session 硬禁止关闭（工具直接返回错误）
- 其他 session 关闭走 Toast 确认

## 现状分析

### 关键文件（修改清单）

| 文件 | 职责 | 修改类型 |
|------|------|----------|
| `tools/tui_control.go` | tui_control 工具定义 | **新增** ~120行 |
| `tools/config_tool.go` | config 工具定义 | **新增** ~100行 |
| `channel/cli_model.go` | BubbleTea Update 添加 cliSessionControlMsg case | **修改** +15行 |
| `channel/cli_helpers.go` | applyLayoutConfig 支持从工具调用路径 | **修改** 少量 |
| `channel/setting_keys.go` | AllSettingDefs 添加 Permission/Sensitive 字段 | **扩展** |
| `agent/agent.go` | 注册 tui_control + config 为非核心工具 | **修改** +5行 |
| `agent/hooks/approval.go` | ApprovalCallback 支持 tui_control/config 的 ToolType | **修改** +10行 |
| `cmd/xbot-cli/setting_handlers.go` | 新 SettingDef 的 handler（如有新增） | **可能修改** |

### 已有基础设施（直接复用）

| 机制 | 文件/位置 | 用途 |
|------|----------|------|
| `asyncCh` (buffered-64) | `channel/cli.go` | TUI 消息串行化通道 |
| `handleAsyncDrain` | `channel/cli.go` | 单 goroutine → program.Send() |
| `switchToSession(entry)` | `channel/cli_panel.go:3461` | 切换 session 的完整逻辑 |
| `applyLayoutConfig` | `channel/cli_helpers.go:733` | 布局配置即时生效 |
| `applyThemeAndRebuild` | `channel/cli_helpers.go` | 主题切换即时生效 |
| `SpawnInteractiveSession` | `agent/interactive.go:311` | 创建 SubAgent session（已有） |
| `panelAgentEntry` | `channel/cli_panel.go:85-93` | Sidebar session 条目 |
| `CreateChat` 工具 | `tools/create_chat.go` | AI 已可创建 session |
| `SettingDef` + `settingHandlerRegistry` | `channel/setting_keys.go` | 配置注册表+运行时副作用 |
| `SaveToFile` deep merge | `config/config_json.go` | 原子配置持久化 |
| `CLIApprovalHandler` | `channel/cli_approval.go` | 用户审批 dialog |
| `Register()` (非核心) | `tools/interface.go:199-216` | load_tools 按需激活 |

### 探索发现的关键数据

- **18 个核心工具**，每次 API 调用约 3750 tokens tool definitions
- **SubAgent 独占 625 tokens**，是最大单一工具
- **SpawnInteractive 已自动创建 sidebar entries**（`interactive.go:311` → `panelAgentEntry`）
- **6 种 TUI 核心配置**已在 SettingDef 中：theme, layout_mode, sidebar_enabled, sidebar_width, sidebar_position, chat_max_width
- **asyncCh 是单 goroutine 注入点**：`handleAsyncDrain` 保证线程安全

### 风险点

- **审批疲劳**：连续破坏性操作可能导致用户不耐烦全点确认（建议 5 秒冷却合并）
- **close_session 语义**：需确认是 "UI 隐藏" 还是 "terminate session"（`InteractiveSubAgentManager` 的 Kill/Unload 需确认）
- **面包屑可发现性**：SubAgent 描述中 1 行提示能否被 LLM 理解并触发 search_tools → load_tools
- **非交互 channel (Feishu/QQ)**：Toast 确认不可用，close_session 需降级为"此操作需要 CLI 环境"

## 详细计划

### Phase 1：基础设施（预计 2-3 天）

#### 1.1 新增 cliSessionControlMsg 消息类型
- [ ] 在 `channel/cli_model.go` 定义：
  ```go
  type cliSessionControlMsg struct {
      action string   // "switch" | "close"
      chatID string
      params map[string]string
      result chan cliSessionActionResult
  }
  ```
- [ ] 在 `Update()` 中添加 case，分派到 `switchToSession` / `closeSession`
- **涉及文件**：`channel/cli_model.go`

#### 1.2 实现 tui_control 工具
- [ ] 在 `tools/tui_control.go` 创建工具：
  - `action: switch_session | close_session | set_layout | set_theme`
  - `Execute()` 构造 `cliSessionControlMsg` → asyncCh → 等待 result channel
  - `close_session` 首次返回 `confirmation_required`，再次带 `confirm=true` 执行
- [ ] 注册为 `Register()`（非核心，load_tools 激活）
- [ ] 复用现有 `applyLayoutConfig` / `applyThemeAndRebuild`
- **涉及文件**：`tools/tui_control.go`（新增）、`agent/agent.go`（注册）

#### 1.3 实现 config 工具
- [ ] 在 `tools/config_tool.go` 创建工具：
  - `action: get | set`
  - 复用 `SettingService.Get/Set` + `settingHandlerRegistry`
  - Layer 3 键硬阻止（api_key/provider/base_url）
  - Layer 2 键走审批
- [ ] 注册为 `Register()`（非核心）
- **涉及文件**：`tools/config_tool.go`（新增）、`agent/agent.go`（注册）

#### 1.4 扩展 SettingDef
- [ ] 添加字段：`Permission`（Layer 0/2/3）、`Sensitive`（bool）
- [ ] 为现有 29 个 setting 标注层级
- [ ] 更新 `TestAllRuntimeKeysHaveHandlers` CI 覆盖
- **涉及文件**：`channel/setting_keys.go`

### Phase 2：安全护栏（预计 2 天）

#### 2.1 ApprovalHandler 扩展
- [ ] `ApprovalRequest` 添加 `ToolType: "tui_control" | "config"`
- [ ] `CLIApprovalHandler` 渲染针对性审批 dialog
- [ ] close_session 审批含 session 名称 + 警告
- [ ] 5 秒冷却合并：同类型连续审批合并
- **涉及文件**：`agent/hooks/approval.go`、`channel/cli_approval.go`

#### 2.2 Session 上限 + 速率限制
- [ ] `TUICtrlFn` 回调中检查活跃 session 数 vs `max_sessions`
- [ ] 每 turn 通过 `ToolContext` 计数器限制创建
- [ ] 超限返回明确错误 + 建议提高上限
- **涉及文件**：`tools/tui_control.go`

#### 2.3 面包屑提示
- [ ] SubAgent 工具 description 末尾追加 1 行：
  ```
  For TUI sidebar/layout/theme management, use search_tools to load tui_control. For configuration changes, load config.
  ```
- **涉及文件**：SubAgent 工具定义处（`tools/subagent_tool.go` 或 `agent/agent.go`）

### Phase 3：验证与文档（预计 1-2 天）

#### 3.1 测试
- [ ] 单元测试：tui_control 各 action 的参数校验和错误分支
- [ ] 单元测试：config 工具的 Layer 2/3 拦截
- [ ] 单元测试：close_session 确认流程（首次拒绝 + 再次确认）
- [ ] 单元测试：SettingDef Permission 标注完整性
- [ ] 手动测试：AI 执行 switch_session → sidebar 高亮移动
- [ ] 手动测试：AI 执行 set_theme → 界面即时切换
- [ ] 手动测试：AI 尝试 set_config(api_key=xxx) → 返回拒绝

#### 3.2 文档
- [ ] 更新 `docs/agent/tools.md` 添加 tui_control + config
- [ ] 更新 `AGENTS.md` Quick Reference
- [ ] 添加 knowledge file

## 验证方案

| 验证点 | 验收标准 | 方法 |
|--------|----------|------|
| tui_control 可发现 | AI 收到 TUI 操作指令后能找到并激活 | 手动对话测试 |
| switch_session | sidebar 焦点切换，主区域显示目标 session 历史 | 手动 CLI 测试 |
| close_session | Toast 确认 → session 从 sidebar 移除 | 手动 CLI 测试 |
| main session 防关 | close_session(main_chatID) → 返回错误 | 单元测试 |
| set_layout | ConfigSet("sidebar_width", 30) → 界面即时重排 | 手动 CLI 测试 |
| set_theme | ConfigSet("theme", "ocean") → 颜色即时切换 | 手动 CLI 测试 |
| config set 白名单 | 允许的 key 写入成功，禁止的 key 返回拒绝 | 单元测试 |
| api_key 硬阻止 | config.set("api_key", "sk-xxx") → 返回错误 | 单元测试 |
| 核心工具 token | 不涉及 TUI 的 API 调用 token 开销增加 = 0 | 对比测试 |
| session 上限 | 第 21 个 session 创建返回错误 | 单元测试 |

## 回滚策略

1. **tui_control + config 工具**：均为新增文件，删除 + 取消注册即可回滚
2. **cliSessionControlMsg**：Update 中新增 case，删除不影响现有流程
3. **SubAgent 面包屑**：删除追加的描述行即可恢复

## 注意事项

- **不要新增 Agent 管理工具**：agent-creator skill 已覆盖
- **不要新增 Session 创建工具**：CreateChat/SubAgent（SpawnInteractive）已覆盖
- **不要新增通信抽象层**：asyncCh + program.Send 已是唯一正确的 TUI 注入通道
- **密钥绝对不能经过 LLM tool call**：config 工具 Layer 3 在参数解析阶段拒绝
- **close_session 语义需实现时确认**：UI 隐藏 vs terminate（查 `InteractiveSubAgentManager` 接口）
- **flat memory 自动兼容**：`Register()` 在 flat 模式下自动升级为核心
- **非交互 channel 降级**：Toast 不可用时，close_session 返回"需 CLI 环境执行"

✅ 自审通过（Round 2 修正版）
