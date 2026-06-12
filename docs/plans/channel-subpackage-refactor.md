# Plan: channel 包子包拆分重构

## Summary

将 `channel/` 包（94个Go文件，57K行）拆分为子包：`channel/` 保留共享基础 + `channel/cli/` + `channel/web/` + `channel/feishu/` + `channel/qq/` + `channel/napcat/`。每个子包是独立的 Go package，有自己的类型和实现，通过共享接口解耦。

## 当前问题

- 57K行代码全部在 `package channel`，AI 读代码时上下文爆炸
- CLI 的 34K行和 Web 的 13K行混在一起
- `cli_types.go` 命名暗示 CLI-only 但实际包含大量共享类型（OutboundMsg、Subscription等）
- 无法独立测试/编译某个 channel 实现

## 目标结构

```
channel/                          # 共享基础 (package channel) — ~3500行
├── channel.go                    # Channel interface
├── types.go                      # ← 从 cli_types.go 提取共享类型
├── callbacks.go                  # RunnerCallbacks, RegistryCallbacks, LLMCallbacks
├── capability.go                 # SettingsCapability, UIBuilder, SettingDefinition
├── setting_keys.go               # Setting keys schema
├── setting_helpers.go            # ParseSettingBool, ParseSettingInt
├── provider.go                   # ChannelProvider interface
├── dispatcher.go                 # Dispatcher
├── agent_channel.go              # AgentChannel
├── interfaces.go                 # ← 从 channel_cli.go 提取 ProgressSender/UserMessageInjector/SessionStateSender
├── mock.go                       # MockChannel
├── mock_test.go
├── capability_test.go
├── math.go                       # LaTeX渲染（被CLI专用但暂留base避免循环依赖）
├── math_test.go
├── wrap_unicode_test.go
│
├── cli/                          # CLI TUI 子包 (package cli) — ~34K行
│   ├── cli.go                    # CLIChannel + NewCLIChannel + Start
│   ├── types.go                  # ← cli_types.go 中 CLI-only 部分
│   ├── model.go                  # ← cli_model.go
│   ├── view.go                   # ← cli_view.go
│   ├── update.go                 # ← cli_update.go
│   ├── update_handlers.go        # ← cli_update_handlers.go
│   ├── helpers.go                # ← cli_helpers.go
│   ├── panel.go                  # ← cli_panel.go
│   ├── progress.go               # ← cli_progress.go
│   ├── agent_msg.go              # ← cli_agent_msg.go
│   ├── msg_render.go             # ← cli_msg_render.go
│   ├── msg_builder.go            # ← cli_msg_builder.go
│   ├── render_turn.go            # ← cli_render_turn.go
│   ├── tool_render.go            # ← cli_tool_render.go
│   ├── diff.go                   # ← cli_diff.go
│   ├── theme.go                  # ← cli_theme.go
│   ├── session.go                # ← cli_session.go
│   ├── settings.go               # ← cli_settings.go
│   ├── inbound.go                # ← cli_inbound.go
│   ├── outbound.go               # ← channel_cli.go（远程模式CLI桥）
│   ├── cache.go                  # ← cli_cache.go
│   ├── block_cache.go            # ← cli_block_cache.go
│   ├── viewport.go               # ← cli_viewport.go
│   ├── viewport_bypass.go        # ← cli_viewport_bypass.go
│   ├── runner.go                 # ← cli_runner.go
│   ├── search.go                 # ← cli_search.go
│   ├── wizard.go                 # ← cli_wizard.go
│   ├── slash.go                  # ← cli_slash.go
│   ├── debug.go                  # ← cli_debug.go
│   ├── mouse.go                  # ← cli_mouse.go
│   ├── palette.go                # ← cli_palette.go
│   ├── approval.go               # ← cli_approval.go
│   ├── prompt.go                 # ← cli_prompt.go
│   ├── tab.go                    # ← cli_tab.go
│   ├── askuser_persist.go        # ← cli_askuser_persist.go
│   ├── noninteractive.go         # ← cli_noninteractive.go
│   ├── ctrlz_unix.go             # ← cli_ctrlz_unix.go
│   ├── ctrlz_windows.go          # ← cli_ctrlz_windows.go
│   ├── todo.go                   # ← todo_manager.go
│   ├── i18n.go                   # ← i18n.go
│   ├── mermaid.go                # ← mermaid.go
│   ├── easter_egg.go             # ← easter_egg.go
│   ├── remote_plugin.go          # ← remote_plugin.go
│   ├── card_converter.go         # ← card_converter.go (CLI使用)
│   │
│   └── *_test.go                 # 跟随对应的源文件
│
├── web/                          # Web 子包 (package web) — ~13K行
│   ├── web.go                    # WebChannel + ChatRoom + SessionInfo
│   ├── api.go                    # ← web_api.go
│   ├── auth.go                   # ← web_auth.go（含Web用户管理 + Feishu联合登录）
│   ├── hub.go                    # ← web_hub.go
│   ├── remote_cli.go             # ← web_remote_cli.go
│   ├── eventstream.go            # ← web_eventstream.go
│   ├── file.go                   # ← web_file.go
│   ├── ws_options_other.go       # ← websockopt_other.go
│   ├── ws_options_windows.go     # ← websockopt_windows.go
│   │
│   └── web_test.go
│
├── feishu/                       # Feishu 子包 (package feishu) — ~5.7K行
│   ├── feishu.go                 # FeishuChannel
│   ├── settings.go               # ← feishu_settings.go
│   │
│   └── feishu_settings_test.go
│
├── qq/                           # QQ 子包 (package qq) — ~1.7K行
│   ├── qq.go
│   └── qq_test.go
│
├── napcat/                       # NapCat 子包 (package napcat) — ~0.8K行
│   ├── napcat.go
│   └── napcat_test.go
│
└── ws/                           # WebSocket 共享基础 (package ws) — ~165行
    └── base.go                   # ← ws_base.go
```

## 共享类型提取清单

### channel/types.go（从 cli_types.go 提取）
| 类型 | 说明 |
|------|------|
| `InboundMsg` | 入站消息，所有channel使用 |
| `OutboundMsg` | 出站消息，外部158处引用 |
| `BgTaskStatus` / `BgTask` | 后台任务 |
| `UserTokenUsage` / `DailyTokenUsage` | Token用量 |
| `HistoryIteration` / `HistoryMessage` | 历史消息（protocol alias） |
| `Subscription` / `PerModelConfig` | 订阅模型（protocol alias） |
| `SubscriptionManager` | 订阅管理接口 |
| `LLMSubscriber` | LLM切换接口 |
| `SessionPanelEntry` / `AgentPanelEntry` | 面板数据 |
| `SessionChatMessage` | 会话消息 |
| `ConvertMessagesToHistory` | 历史转换函数 |
| `MetadataReplyPolicy` / `ReplyPolicyOptional` | 元数据常量 |

### channel/interfaces.go（从 channel_cli.go 提取）
| 接口 | 说明 |
|------|------|
| `ProgressSender` | 进度推送 |
| `UserMessageInjector` | 消息注入 |
| `SessionStateSender` | 会话状态 |

### 留在 channel/ 的文件（不变）
| 文件 | 说明 |
|------|------|
| `channel.go` | Channel interface |
| `callbacks.go` | RunnerCallbacks等 |
| `capability.go` | SettingsCapability等 |
| `setting_keys.go` | 设置schema |
| `setting_helpers.go` | ParseSettingBool等 |
| `provider.go` | ChannelProvider接口 |
| `dispatcher.go` | Dispatcher |
| `agent_channel.go` | AgentChannel |
| `mock.go` | MockChannel |

### CLI子包专属（从 cli_types.go 移到 cli/types.go）
| 类型 | 说明 |
|------|------|
| `CLIChannel` | CLI channel实现 |
| `CLIChannelConfig` | CLI配置（含大量回调） |
| `SettingsService` | CLI设置接口 |
| `ModelLister` | 模型列表接口 |
| `cliTodoManager` | TODO管理 |
| `truncateToWidth` / `hardWrapRunes` 等工具函数 | CLI渲染辅助 |
| `newGlamourRenderer` | Glamour渲染器 |
| `formatToolLabel` | 工具标签格式化 |
| `iterSnapshot` / `iterToolSnap` | 内部类型 |

## 实施步骤

### Phase 1: 准备工作（最低风险）
1. 创建 `channel/types.go`，从 `cli_types.go` 提取共享类型
2. 创建 `channel/interfaces.go`，从 `channel_cli.go` 提取接口
3. 编译验证：此时 `types.go` 和原文件有重复定义 → 删除原文件中的重复

### Phase 2: 最小子包试水（qq/napcat/ws）
4. 创建 `channel/ws/` 目录，移入 `ws_base.go`
5. 创建 `channel/qq/` 目录，移入 `qq.go` + `qq_test.go`
6. 创建 `channel/napcat/` 目录，移入 `napcat.go` + `napcat_test.go`
7. 更新 package 声明 + import 路径
8. 全量编译 `go build ./...`，修复编译错误

### Phase 3: Web 子包
9. 创建 `channel/web/`，移入 web_*.go + websockopt_*.go
10. 更新 package + import + 外部引用（serverapp/）
11. 编译验证

### Phase 4: Feishu 子包
12. 创建 `channel/feishu/`，移入 feishu*.go
13. 更新 package + import + 外部引用
14. 编译验证

### Phase 5: CLI 子包（最大步骤）
15. 创建 `channel/cli/`，移入所有 cli_*.go + 独占文件
16. `channel_cli.go` → `channel/cli/outbound.go`
17. 更新 package + import + 外部引用（cmd/xbot-cli, agent, plugin等）
18. 编译验证

### Phase 6: 收尾
19. 清理 channel/ 根目录残留文件
20. 全量 `go build ./...` + `go test ./channel/...`
21. 更新 AGENTS.md 相关文档

## 关键决策

### Q1: `ChannelCliChannel`（远程模式CLI桥）放哪里？
**决策：放 `channel/cli/outbound.go`**
- 它是 CLI channel 的远程模式桥接，实现 `Channel` 接口
- 被 Web 的 `RemoteCLIChannel` 和 agent 的 `transport_channel_plugin.go` 引用
- 但外部只通过 `Channel` 接口使用它，不需要直接引用 `ChannelCliChannel` 类型
- `NewChannelCliChannel` 构造函数需导出给 serverapp 使用

### Q2: `ws_base.go`（QQ/NapCat共享）怎么处理？
**决策：独立 `channel/ws/` 子包**
- 只有 164 行，被 QQ 和 NapCat 引用
- 放在 channel/ 基础包也可以，但语义上不属于基础接口

### Q3: Feishu 认证函数（`FeishuLinkUser`等在 web_auth.go）怎么处理？
**决策：保留在 `channel/web/auth.go`**
- 这些函数是 Web API 的一部分（联合登录通过 Web 端点）
- serverapp 直接调用它们，移到 feishu/ 会创建 web→feishu 依赖

### Q4: `card_converter.go` 怎么处理？
**决策：移到 `channel/cli/`**
- `ConvertFeishuCard` 被 CLI 的 msg_render 和 Web 的 web.go 使用
- Web 调用极少（仅在用户通过 Web 查看 Feishu 卡片时）
- 可以在 channel/ 基础包保留一份，或复制到两个子包
- **实际方案**：留在 channel/ 基础包，作为共享工具函数

### Q5: `oss.go` 放哪里？
**决策：移到 `channel/web/`**
- `NewOSSProvider` 仅被 Web channel 和 serverapp 使用
- 放在基础包没有意义

### Q6: `browser.go` 放哪里？
**决策：移到 `channel/web/`**
- `OpenBrowser` 仅 Web 使用

### Q7: math.go / mermaid.go 放哪里？
**决策：`math.go` 留在 channel/ 基础包（被 cli 和 test 引用），`mermaid.go` 移到 cli/**
- `math.go` 的 `renderLatex` 被 CLI 使用，但 test 文件也在根目录
- `mermaid.go` 仅 CLI 使用

## 外部引用更新映射

| 旧引用 | 新引用 |
|--------|--------|
| `channel.NewCLIChannel` | `cli.NewCLIChannel` |
| `channel.CLIChannel` | `cli.CLIChannel` |
| `channel.CLIChannelConfig` | `cli.CLIChannelConfig` |
| `channel.ApplyTheme` | `cli.ApplyTheme` |
| `channel.ModelsLoadErrorCh` | `cli.ModelsLoadErrorCh` |
| `channel.NewAutoSession` | `cli.NewAutoSession` |
| `channel.GetLastActiveSession` | `cli.GetLastActiveSession` |
| `channel.ParseChatID` | `cli.ParseChatID` |
| `channel.GenerateSessionName` | `cli.GenerateSessionName` |
| `channel.NewWebChannel` | `web.NewWebChannel` |
| `channel.WebChannel` | `web.WebChannel` |
| `channel.WebCallbacks` | `web.WebCallbacks` |
| `channel.NewFeishuChannel` | `feishu.NewFeishuChannel` |
| `channel.FeishuChannel` | `feishu.FeishuChannel` |
| `channel.FeishuConfig` | `feishu.FeishuConfig` |
| `channel.SettingsCallbacks` | `feishu.SettingsCallbacks` |
| `channel.NewQQChannel` | `qq.NewQQChannel` |
| `channel.QQConfig` | `qq.QQConfig` |
| `channel.NewNapCatChannel` | `napcat.NewNapCatChannel` |
| `channel.NapCatConfig` | `napcat.NapCatConfig` |
| `channel.NewChannelCliChannel` | `cli.NewChannelCliChannel` |
| `channel.NewRemoteCLIChannel` | `web.NewRemoteCLIChannel` |
| `channel.RemoteCLIChannel` | `web.RemoteCLIChannel` |
| `channel.CreateWebUser` | `web.CreateWebUser` |
| `channel.ListWebUsers` | `web.ListWebUsers` |
| `channel.DeleteWebUser` | `web.DeleteWebUser` |
| `channel.FeishuLinkUser` | `web.FeishuLinkUser` |
| `channel.FeishuGetLinkedUser` | `web.FeishuGetLinkedUser` |
| `channel.FeishuUnlinkUser` | `web.FeishuUnlinkUser` |
| `channel.NewOSSProvider` | `web.NewOSSProvider` |
| `channel.QiniuConfig` | `web.QiniuConfig` |
| `channel.ChatRoom` / `SessionInfo` / `UserChatWithPreview` | `web.ChatRoom` / `web.SessionInfo` / `web.UserChatWithPreview` |
| `channel.PaletteExternalCommand` | `cli.PaletteExternalCommand` |
| `channel.PaletteCategoryXxx` | `cli.PaletteCategoryXxx` |
| `channel.NewRemotePluginCache` | `cli.NewRemotePluginCache` |
| `channel.ChannelCliChannel` | `cli.ChannelCliChannel` |
| 不变 | `channel.OutboundMsg`, `channel.Channel`, `channel.Dispatcher`, `channel.Subscription` 等基础类型 |

## 风险

| 风险 | 缓解 |
|------|------|
| cliModel 方法分布在 27 个文件，必须整体移动 | 一次性移动所有 cli_*.go，保持内部结构不变 |
| 循环依赖：CLI 可能引用 Web 类型（SessionChatMessage 等） | 共享类型留在 channel/ 基础包 |
| 测试文件大量白盒访问 | 测试文件跟随源文件移动，保持同 package |
| 外部引用更新量大（cmd/main.go 约 60 处） | 用 sed 批量替换，逐一编译验证 |
| `HistoryMessage`/`SessionChatMessage` 在 web 和 cli 之间共享 | 留在 channel/types.go |

## Definition of Done

- [ ] `go build ./...` 编译通过，无错误
- [ ] `go test ./channel/...` 全部通过
- [ ] 每个子包（cli/web/feishu/qq/napcat/ws）有独立的 package 声明
- [ ] channel/ 基础包不含任何 channel 实现细节（只有接口+共享类型）
- [ ] 外部包（cmd/serverapp/agent/plugin）的 import 正确更新
- [ ] 不提交代码（用户要求）

## Open Questions

无。所有关键决策已在上方记录。
