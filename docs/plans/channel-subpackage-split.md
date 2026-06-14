# Plan: channel 包子包拆分

## Summary

将 56850 行、94 个文件的 `channel/` 单包拆分为共享核心层 + 5 个实现子包。各 channel 实现之间零交叉依赖，CLI 不引用其他 channel 类型，主要障碍是 `OutboundMsg` 等共享类型定义在 `cli_types.go` 中。拆分后子包可重命名变量（如 `CLIChannel` → `cli.Channel`）减少冗余前缀。

## 目标子包结构

```
channel/              # 根包 — 共享核心类型/接口/基础设施
├── channel.go        # Channel 接口 (不变)
├── types.go          # NEW: OutboundMsg, InboundMsg 等共享消息类型 (从 cli_types.go 提取)
├── interfaces.go     # NEW: ProgressSender, UserMessageInjector, SessionStateSender (从 channel_cli.go 提取)
├── dispatcher.go     # Dispatcher (不变)
├── agent_channel.go  # AgentChannel (不变)
├── capability.go     # SettingsCapability, SettingDefinition, UIBuilder (不变)
├── setting_keys.go   # CLIRuntimeSettingKeys, setting key 函数 (不变)
├── setting_helpers.go# MissingRegistryKeys (不变)
├── provider.go       # ChannelProvider (不变)
├── callbacks.go      # RunnerCallbacks, RegistryCallbacks, LLMCallbacks (不变)
├── card_converter.go # ConvertFeishuCard (不变 — CLI+Web 共用)
├── session_utils.go  # NEW: DeduplicateSessionName, NameEntry, NameLookup, ConvertMessagesToHistory (从 cli_session.go/cli_types.go 提取)
├── subscription.go   # NEW: Subscription, PerModelConfig, SourceConfigJSON, SourceLLMConfig, SessionInfo 等 (从 cli_types.go 提取)
├── mock.go           # MockChannel (不变)

channel/cli/          # CLI BubbleTea TUI (~44k 行)
├── cli.go            # CLIChannel → 重命名为 cli.Channel, CLIChannelConfig → cli.Config
├── cli_model.go      # cliModel → 重命名为 cli.Model
├── cli_types.go      # CLI 专有类型: AgentPanelEntry, SessionPanelEntry, PaletteExternalCommand, BgTask 等
├── cli_panel.go, cli_view.go, cli_message.go, ... (所有 cli_*.go)
├── i18n.go, math.go, mermaid.go, easter_egg.go (CLI 专用工具)
├── todo_manager.go, remote_plugin.go, browser.go
├── channel_cli.go    # ChannelCliChannel → 重命名为 cli.CliChannel (WS 桥接, 用于 agent 类型断言)

channel/feishu/       # Feishu 渠道 (~5.4k 行)
├── feishu.go         # FeishuChannel → feishu.Channel, FeishuConfig → feishu.Config
├── feishu_settings.go

channel/web/          # Web 渠道 (~5.4k 行)
├── web.go            # WebChannel → web.Channel, WebChannelConfig → web.Config, WebCallbacks → web.Callbacks
├── web_api.go, web_auth.go, web_hub.go, web_eventstream.go, web_file.go
├── web_remote_cli.go # RemoteCLIChannel → web.RemoteCLIChannel
├── oss.go            # OSSProvider 等 (Web 专用)
├── websockopt_*.go

channel/qq/           # QQ 渠道 (~1.7k 行)
├── qq.go             # QQChannel → qq.Channel, QQConfig → qq.Config
├── ws_base.go        # WSChannelBase → 移到独立包供 qq + napcat 共用

channel/napcat/       # NapCat 渠道 (~0.8k 行)
├── napcat.go         # NapCatChannel → napcat.Channel, NapCatConfig → napcat.Config

channel/wsbase/       # WebSocket 基础设施 (~164 行)
├── ws_base.go        # WSChannelBase → wsbase.ChannelBase
```

## 重命名策略

子包中去除冗余前缀，遵循 Go 包名即命名空间的约定：

| 原名 | 新名 | 说明 |
|------|------|------|
| `channel.CLIChannel` | `cli.Channel` | 包名 `cli` 已提供上下文 |
| `channel.CLIChannelConfig` | `cli.Config` | |
| `channel.FeishuChannel` | `feishu.Channel` | |
| `channel.FeishuConfig` | `feishu.Config` | |
| `channel.WebChannel` | `web.Channel` | |
| `channel.WebChannelConfig` | `web.Config` | |
| `channel.QQChannel` | `qq.Channel` | |
| `channel.QQConfig` | `qq.Config` | |
| `channel.NapCatChannel` | `napcat.Channel` | |
| `channel.NapCatConfig` | `napcat.Config` | |
| `channel.NewCLIChannel` | `cli.NewChannel` | |
| `channel.NewFeishuChannel` | `feishu.NewChannel` | |
| `channel.NewWebChannel` | `web.NewChannel` | |
| `channel.NewQQChannel` | `qq.NewChannel` | |
| `channel.NewNapCatChannel` | `napcat.NewChannel` | |
| `channel.ChannelCliChannel` | `cli.CliChannel` | WS 桥接，保留 Cli 区别于本地 Channel |
| `channel.NewChannelCliChannel` | `cli.NewCliChannel` | |

**保留在根包的共享类型不变**（`OutboundMsg`, `InboundMsg`, `Channel`, `Dispatcher`, `Subscription` 等），外部对它们的引用零修改。

## 执行步骤

### Phase 0: 准备
1. 从 master 创建分支 `refactor/channel-subpackages`
2. 确保当前 `go build ./...` 通过

### Phase 1: 提取共享类型到根包 (前置必需)
将跨子包共享的类型从 `cli_types.go` / `channel_cli.go` / `cli_session.go` 提取到根包新文件：
- `types.go`: `OutboundMsg`, `InboundMsg`, `StreamRenderInfo`, `OutboundMsgType` 等
- `interfaces.go`: `ProgressSender`, `UserMessageInjector`, `SessionStateSender`, `StreamRenderer`
- `subscription.go`: `Subscription`, `PerModelConfig`, `SourceConfigJSON`, `SourceLLMConfig`, `SessionInfo`, `DailyTokenUsage`, `UserTokenUsage`, `UserChatWithPreview`, `ConvertMessagesToHistory`, `HistoryMessage`, `HistoryIteration`
- `session_utils.go`: `DeduplicateSessionName`, `NameEntry`, `NameLookup`
- 验证: `go build ./...`

### Phase 2: 拆分 feishu 子包 (最小，验证模式)
1. 创建 `channel/feishu/` 目录
2. 移动 `feishu.go`, `feishu_settings.go`
3. 更改 package 为 `feishu`
4. 添加 `import ch "xbot/channel"` 引用共享类型
5. 重命名 `FeishuChannel` → `Channel`, `FeishuConfig` → `Config` 等
6. 更新 `serverapp/server.go` 中的引用
7. 更新 `agent/command_builtin.go` 中的类型断言
8. 更新测试文件
9. 验证: `go build ./...` + `go test ./channel/... ./serverapp/... ./agent/...`

### Phase 3: 拆分 wsbase → qq → napcat
1. 创建 `channel/wsbase/`，移动 `ws_base.go`
2. 创建 `channel/qq/`，移动 `qq.go`
3. 创建 `channel/napcat/`，移动 `napcat.go`
4. 更新外部引用 (`serverapp/server.go`)
5. 验证: `go build ./...`

### Phase 4: 拆分 web 子包
1. 创建 `channel/web/` 目录
2. 移动 `web*.go` + `oss.go` + `websockopt_*.go`
3. 更新外部引用 (`serverapp/server.go`, `agent/agent.go` 的 RemoteCLIChannel 断言)
4. 验证: `go build ./...`

### Phase 5: 拆分 CLI 子包 (最大)
1. 创建 `channel/cli/` 目录
2. 移动所有 `cli_*.go` + `i18n.go` + `math.go` + `mermaid.go` + `easter_egg.go` + `todo_manager.go` + `remote_plugin.go` + `browser.go` + `channel_cli.go`
3. 更改 package 为 `cli`
4. 添加共享类型引用 `import ch "xbot/channel"`
5. 重命名 `CLIChannel` → `Channel`, `CLIChannelConfig` → `Config` 等
6. 更新 `agent/` 包中的类型断言 (`CLIChannel`, `ChannelCliChannel`)
7. 更新 `cmd/xbot-cli/main.go` (最大外部依赖)
8. 更新所有测试文件
9. 验证: `go build ./...` + `go test ./...`

### Phase 6: 清理 + 文档
1. 删除根包中已移走的文件
2. 更新 `docs/agent/channel.md` 和 `docs/agent/architecture.md`
3. 更新 AGENTS.md 中的包描述
4. `golangci-lint run ./...`
5. `go test ./...`

## 影响范围

### 外部包修改 (预估)
| 包 | 修改量 | 说明 |
|----|--------|------|
| `agent/` | ~15 处 | 类型断言 `*channel.CLIChannel` → `*cli.Channel` 等, 新增子包 import |
| `serverapp/` | ~20 处 | channel 注册函数引用, 新增子包 import |
| `cmd/xbot-cli/` | ~40 处 | CLIChannelConfig/CLIChannel 引用, callback 类型引用 |

### 风险评估
- **低风险**: feishu/qq/napcat 拆分（独立性高，外部引用少）
- **中风险**: web 拆分（RemoteCLIChannel 被 agent 引用，web_auth 含跨平台 OAuth）
- **高风险**: CLI 拆分（44k 行，大量 unexported 符号在文件间共享，main.go 有 ~40 处引用）

### 循环依赖检查
- 子包 → 根包 channel: ✅ 单向依赖
- 子包 → 子包: ❌ 无（各 channel 实现零交叉引用）
- 根包 → 子包: ❌ 无（根包不依赖实现）
- agent → 子包: ✅ 单向（agent 导入子包做类型断言）

## Definition of Done
- [ ] `go build ./...` 通过
- [ ] `go test ./...` 通过
- [ ] `golangci-lint run ./...` 通过
- [ ] channel 根包仅包含共享类型/接口/基础设施
- [ ] 每个 channel 实现在独立子包中
- [ ] 外部引用已全部更新
- [ ] 文档已更新
