# Plan: 工具系统重构 — 超时保护、废弃字段清理、冗余工具移除

## Summary

针对 Issue #176（Grep 工具在大型文件系统上无超时且无法中断），进行三方面重构：
1. 清理已废弃的 `RunConfig.ToolTimeout` / `runState.toolTimeout` 死代码
2. 为每个文件系统工具（Grep/Glob/Read/Edit）添加独立的 context 取消响应 + per-tool 超时（不使用公共超时）
3. 移除 `logs` 工具（纯运维辅助、仅 admin 可用、不被任何业务逻辑引用）；审查 `ChatHistory` 工具冗余性

分支：`refactor/tool-timeout-cleanup`

## Changes

### 第一部分：废弃字段清理

#### `agent/engine.go:141-144`
- What: 删除 `RunConfig.ToolTimeout` 字段及其 deprecated 注释
- Why: 字段已废弃，引擎不再用它包装 context，保留会误导新代码以为设置它会生效

#### `agent/engine_run.go:45`
- What: 删除 `runState` struct 中的 `toolTimeout time.Duration` 字段
- Why: 死代码 — 在 `:137` 被写入后，整个 agent 包中从未被读取

#### `agent/engine_run.go:111-114`
- What: 删除 deprecated 注释和 `toolTimeout := cfg.ToolTimeout` 局部变量赋值
- Why: 唯一读取 `cfg.ToolTimeout` 的地方，赋值后无人使用

#### `agent/engine_run.go:137`
- What: 从 runState 构造 literal 中删除 `toolTimeout: toolTimeout,`
- Why: 对应字段已删除

#### `agent/engine_wire.go:164`
- What: 删除 `// ToolTimeout: no longer used. Tools manage their own timeouts.` 注释
- Why: 字段已不存在，注释无意义

#### `plugin/middleware.go` + `plugin/middleware_test.go`
- ⚠️ **不动** — `plugin.ToolTimeout()` 是活跃的插件装饰器，与 agent 层废弃字段无关

### 第二部分：每个工具独立 context 取消 + 超时

设计原则：**每个工具根据自身场景定义独立超时常量**，不引入公共超时配置。

#### `tools/grep.go` — Grep 本地模式 context 取消 + 60s 超时

- What:
  1. 添加常量 `const grepLocalTimeout = 60 * time.Second`
  2. `executeLocal` 开头创建 `ctx, cancel := context.WithTimeout(parentCtx, grepLocalTimeout)`
  3. `filepath.WalkDir` 回调首行添加 `ctx.Err()` 检查，返回 `filepath.SkipAll` 中断遍历
  4. `searchFile` 函数签名增加 `ctx context.Context` 参数，在 `scanner.Scan()` 循环中每 100 行检查一次 `ctx.Err()`
  5. 遍历结束后如果 `ctx.Err() != nil`，在输出中标注 "(搜索被中断或超时)"
- Why: 60s 足够覆盖大型代码仓库搜索，网络文件系统场景下可被 Ctrl+C 中断
- 注意: `searchFile` 新增 ctx 参数后需更新所有调用点（grep.go 内仅 `executeLocal` 一处调用）

#### `tools/glob.go` — Glob 本地模式 context 取消 + 30s 超时

- What:
  1. 添加常量 `const globLocalTimeout = 30 * time.Second`
  2. `executeLocal` 开头创建 timeout context
  3. `globWithDoublestar` 函数签名增加 `ctx context.Context` 参数
  4. `filepath.WalkDir` 回调首行添加 `ctx.Err()` 检查 + 在回调内部做 200 截断（而不是收集全部后截断）
  5. 截断提前后 `return filepath.SkipAll` 停止遍历
- Why: 30s 足够文件模式匹配；在回调内部截断避免海量匹配先全部加载到内存
- 注意: `globWithDoublestar` 新增 ctx 参数后需更新调用点（glob.go 内仅 `executeLocal` 一处调用）

#### `tools/read.go` — Read 本地模式文件大小限制 + 10s 超时

- What:
  1. 添加常量 `const maxReadFileSize = 10 * 1024 * 1024` (10MB) 和 `const readLocalTimeout = 10 * time.Second`
  2. `executeLocal` 在 `os.ReadFile` 前先 `os.Stat` 检查文件大小，超限返回错误
  3. `executeLocal` 开头创建 timeout context（用于 NFS 等慢速 I/O 场景的保护 goroutine）
  4. 用 goroutine + channel 包装 `os.ReadFile`，select `ctx.Done()` 提前返回（I/O 本身仍阻塞，但 agent 层能提前感知取消）
- Why: 10MB 限制防止 OOM；10s 超时防止 NFS 挂载文件阻塞；`os.ReadFile` 无法接受 context，需 goroutine 包装
- 注意: goroutine 泄漏风险 — I/O 完成后 goroutine 自然退出；timeout 场景下 goroutine 仍会阻塞直到 I/O 完成，但这是 Go 标准库限制，可接受

#### `tools/edit.go` — FileReplace/FileCreate 本地模式文件大小限制 + 10s 超时

- What:
  1. 添加常量 `const maxEditFileSize = 10 * 1024 * 1024` (10MB) 和 `const editLocalTimeout = 10 * time.Second`
  2. `FileReplaceTool.executeLocal` 在 `ReadFileAsUser` 前先检查文件大小
  3. `FileCreateTool.executeLocal` 和 `FileReplaceTool.executeLocal` 开头创建 timeout context
  4. 用 goroutine + channel 包装 I/O 操作，支持 context 取消
- Why: 与 Read 一致的保护策略，防止大文件 OOM 和 NFS 阻塞

#### `tools/sandbox_exec.go` — 沙箱超时可配置化

- What:
  1. 为 `RunInSandbox`/`RunInSandboxWithShell`/`RunInSandboxRaw`/`RunInSandboxRawWithShell` 增加 `timeout time.Duration` 参数
  2. 调用方按工具场景传入不同超时值（grep 60s, glob 30s, read 10s, edit 10s）
  3. 保留 `30 * time.Second` 作为默认值（通过新加的 `defaultSandboxTimeout` 常量）
  4. 零值 fallback 到 `defaultSandboxTimeout`
- Why: 不同工具在沙箱模式下的超时需求不同（grep 在大型仓库可能需要更长时间），硬编码 30s 不合理
- 注意: 需更新所有调用点（grep.go/glob.go/read.go/edit.go 中的沙箱路径）

### 第三部分：移除 logs 工具 + 审查 ChatHistory

#### 删除 `tools/logs.go` + `tools/logs_test.go`
- What: 删除这两个文件
- Why: logs 是纯运维辅助工具（读 .xbot/logs 目录），仅 admin 可用，不被任何业务逻辑引用，有 341 行代码 + 644 行测试可省维护成本

#### 删除 `serverapp/server.go:710-716` — logs 工具注册
- What: 删除 `if adminChatID != "" { ... NewLogsTool ... }` 注册块
- Why: 工具已删

#### 删除 `serverapp/server_core.go:130-133` — logs 工具注册
- What: 删除 `if adminChatID := cfg.Admin.ChatID; adminChatID != "" { ... }` 注册块
- Why: 工具已删

#### 审查 ChatHistory 工具 — **保留**
- 分析: ChatHistory 不像 logs 那样"可有可无"：
  - `ChatHistoryStore` 作为 Agent struct 字段 (`agent.go:230`) 被**主动维护**
  - 每条入站消息都会 `a.chatHistory.Add(...)` (`agent.go:2392`)，是消息管道的活跃组件
  - 在 engine.go (并发工具列表)、tool_progress.go (进度标签)、cli_render_turn.go (TUI 展示) 中有集成
  - 对 Feishu/QQ 等多用户群聊场景有实际价值（查看其他用户的最近消息）
- 结论: 保留，不移除

### 第四部分：文档更新

#### `docs/agent/tools.md`
- What: 更新知识文件
  1. 从工具列表中移除 `logs` 条目
  2. 添加 "Tool Timeout Summary" 小节，记录每个工具的超时常量
  3. 更新 sandbox_exec 相关描述（超时可配置化）

## Per-Tool Timeout 设计汇总

| 工具 | 本地超时 | 沙箱超时 | context 取消 | 文件大小限制 |
|------|---------|---------|-------------|-------------|
| Shell | 120s (可配至 600s) | 同左 | ✅ 已有 | N/A |
| Fetch | 30s HTTP | N/A | ✅ 已有 | 10MB |
| **Grep** | **60s 新增** | **60s 可配置** | **✅ 新增** | 1MB/文件 (已有) |
| **Glob** | **30s 新增** | **30s 可配置** | **✅ 新增** | N/A |
| **Read** | **10s 新增** | **10s 可配置** | **✅ 新增** | **10MB 新增** |
| **Edit** | **10s 新增** | **10s 可配置** | **✅ 新增** | **10MB 新增** |

超时值选择依据：
- Grep 60s：大型代码仓库搜索可能耗时，200 match 限制是另一道保护
- Glob 30s：文件匹配比内容搜索快，200 结果限制兜底
- Read/Edit 10s：单文件 I/O 通常秒级完成，10s 足够覆盖 NFS/SSHFS 延迟

## Risks

- **`RunConfig.ToolTimeout` 删除可能影响外部调用者**：如果有 channel adapter 或 test harness 构造 RunConfig 时设置了此字段，删除会导致编译错误。经 grep 确认，agent 包内仅 `engine_run.go:114` 读取它，无外部引用。
- **goroutine 泄漏**：Read/Edit 用 goroutine 包装同步 I/O，超时后 goroutine 仍阻塞直到 I/O 完成。这是 Go 标准库限制，可接受（NFS I/O 最终会完成或被系统超时中断）。
- **sandbox_exec 签名变更影响面**：4 个函数增加 timeout 参数，需更新所有调用点。遗漏会导致编译错误（Go 编译器会捕获）。
- **logs 工具删除影响管理员排障**：管理员将无法通过 agent 查看日志，需直接读文件。这是可接受的取舍——logs 工具使用率极低，且管理员通常有 shell 访问权限。

## Definition of Done

- [ ] `RunConfig.ToolTimeout` 和 `runState.toolTimeout` 全部删除，`go build ./...` 通过
- [ ] Grep 本地模式 WalkDir 回调检查 `ctx.Done()`，`searchFile` 接受 context 参数
- [ ] Glob 本地模式 WalkDir 回调检查 `ctx.Done()`，回调内部即时截断
- [ ] Read 本地模式添加 10MB 文件大小限制 + 10s 超时
- [ ] Edit 本地模式添加 10MB 文件大小限制 + 10s 超时
- [ ] sandbox_exec 4 个函数支持可配置 timeout 参数
- [ ] logs 工具及其注册代码全部删除
- [ ] `go build ./...` 通过
- [ ] `go test ./tools/... ./agent/... ./plugin/...` 通过
- [ ] `golangci-lint run ./...` 无新增警告
- [ ] `docs/agent/tools.md` 更新

## Open Questions

无 — 设计方向已确认。
