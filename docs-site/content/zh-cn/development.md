---
title: "开发指南"
weight: 70
---

# 开发指南

本指南面向希望为 xbot 贡献代码或理解其内部实现的开发者。

## 前置条件

- **Go 1.26+**
- **Node.js 22+**（Web UI 开发）
- **Hugo Extended**（文档站开发）
- **golangci-lint v2.10+**

## 项目结构

```
xbot/
├── agent/          # 核心 Agent 循环、中间件、引擎、子 Agent
├── bus/            # 消息总线（发布/订阅）
├── channel/        # 渠道适配器（CLI、飞书、QQ、NapCat、Web）
├── cli/            # CLI 渠道（BubbleTea TUI）
├── cmd/            # 入口点（xbot-cli、runner、server）
├── config/         # 配置加载和类型
├── cron/           # 定时任务系统
├── docs-site/      # Hugo 文档站
├── internal/       # 内部工具（textarea、runner 客户端等）
├── llm/            # LLM 客户端（OpenAI、Anthropic、流式）
├── memory/         # 记忆提供者（flat、letta）
├── plugin/         # 插件系统（工具、hooks、widget）
├── protocol/       # 共享协议类型
├── serverapp/      # 服务器核心（RPC 表、分发器）
├── session/        # 多租户会话管理
├── storage/        # SQLite 存储层
├── tools/          # 内置工具（Shell、Read、Edit、Grep 等）
├── web/            # Web 前端（React + TypeScript）
└── prompt/         # 系统 prompt 模板
```

## 构建与运行

```bash
# 构建 server + runner
make build

# 构建并运行 server
make run

# 仅构建 CLI
go build -o xbot-cli ./cmd/xbot-cli

# 运行测试
make test                    # Go 测试
cd web && npm run lint && npm run build  # 前端检查

# 本地完整 CI
make ci                      # lint + build + test + web-lint + web-build
```

## Pre-commit hooks

项目使用 pre-commit hooks（`scripts/pre-commit`），运行：
1. `gofmt` 检查
2. `golangci-lint run ./...`
3. `go build ./...`
4. `go test ./...`
5. `plugin/protocol` 子模块测试

安装：`cp scripts/pre-commit .git/hooks/pre-commit && chmod +x .git/hooks/pre-commit`

## 架构概览

阅读 [架构](/zh-cn/architecture/) 页面了解完整系统设计。核心概念：

- **Backend** 是纯 RPC 客户端（零业务逻辑），每个方法都是 1-3 行类型化调用。
- **Transport** 是执行层：`localTransport` 直接调用 Agent，`remoteTransport` 通过 WebSocket 转发。
- **Pipeline** 通过有序中间件组装系统 prompt
  （prompt → 全局上下文 → 渠道 prompt → 技能 → 记忆 → 用户消息）。
- **并发**：全局 LLM 信号量、按租户信号量、并行 Read 执行、子 Agent goroutine。

## 添加新工具

1. 在 `tools/` 中创建文件实现 `Tool` 接口：
   ```go
   type MyTool struct{}
   func (t *MyTool) Name() string { return "MyTool" }
   func (t *MyTool) Description() string { return "工具描述" }
   func (t *MyTool) Parameters() json.RawMessage { /* JSON schema */ }
   func (t *MyTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
       // 实现
   }
   ```

2. 在 `tools/registry.go` 中注册。

3. 在 `tools/my_tool_test.go` 中添加测试。

## 添加新渠道

1. 在 `channel/yourchannel/` 下创建包。
2. 实现渠道接口（消息处理、重连）。
3. 在 `config/config.go` 中添加配置结构体。
4. 在 `channel/` 分发器中注册。
5. 在 `docs-site/content/zh-cn/channels/` 下添加文档。

## 文档

文档站使用 [Hugo](https://gohugo.io/) 和
[GeekDoc](https://github.com/thegeeklab/hugo-geekdoc) 主题。

```bash
cd docs-site
hugo server -D    # 本地开发服务器（含草稿）
hugo --minify     # 生产构建
```

文档是双语的：英文（默认，`content/en/`）和中文（`content/zh-cn/`）。两者使用
`hugo.toml` 中定义的相同菜单结构。

## 贡献

1. Fork 仓库并创建功能分支
2. 遵循现有约定编写代码（参见 `AGENTS.md`）
3. 运行 `make ci` 验证所有检查通过
4. 为新功能添加测试
5. 如果行为变化则更新文档
6. 提交带有清晰描述的 PR

{{< hint type=note >}}
在进行代码修改前，阅读项目根目录的 `AGENTS.md` 了解详细约定、陷阱和架构说明。
{{< /hint >}}
