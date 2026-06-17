---
title: "常见问题"
weight: 60
---

# 常见问题

## 关键术语

| 术语 | 含义 |
|------|------|
| **订阅 (Subscription)** | LLM 提供商配置（API Key、模型、API 地址）。可创建多个。 |
| **渠道 (Channel)** | 通信端点（CLI、飞书、QQ、Web）。 |
| **子 Agent (SubAgent)** | 拥有独立上下文的子 Agent，由主 Agent 委派。 |
| **技能 (Skill)** | 指导 Agent 完成特定任务的 Markdown 能力包。 |
| **模型层 (Tier)** | 模型分级（Vanguard/Balance/Swift），用于子 Agent 按任务复杂度选模型。 |
| **会话 (Session)** | 对话上下文。多个会话可独立运行。 |
| **沙箱 (Sandbox)** | Shell 命令执行隔离（none 或 Docker 模式）。 |

## 安装

### 安装器报错"connection refused"或"timeout"

如果你在国内网络环境下，使用镜像加速安装器：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

也可手动设置 `GH_MIRROR=gh-proxy.com` 或 `GH_MIRROR=ghfast.top`。

### Standalone 还是 Server？

- **Standalone**：个人开发者，快速体验。仅 CLI，关终端就停。
- **Server**：团队、多渠道（飞书/QQ/Web）、共享 LLM、常驻运行。
  **大多数团队应选 Server 模式。**

### 如何从源码构建？

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build
```

需要 Go 1.26+。Web UI 已预编译，构建 Go 不需要 Node.js。

## LLM 配置

### 如何使用 DeepSeek / 通义千问 / Ollama 等兼容 API？

设置 `provider: "openai"` 并修改 `base_url`：

```json
{
  "subscriptions": [
    {
      "name": "DeepSeek",
      "provider": "openai",
      "api_key": "your-key",
      "base_url": "https://api.deepseek.com/v1",
      "model": "deepseek-chat"
    }
  ]
}
```

### 可以配置多个 LLM 订阅吗？

可以。在 `config.json` 中创建多个订阅（或通过 `/setup`），然后用
`Ctrl+P` 或 `/models` 切换。Server 模式下管理员创建一次，全团队共享。

### 模型层（Vanguard / Balance / Swift）是什么？

模型层让子 Agent 针对不同复杂度使用不同模型。通过 `/settings` 配置：
- **Vanguard** — 最强推理（复杂任务）
- **Balance** — 均衡（一般工作）
- **Swift** — 快速/小型（快速查找）

未配置的层自动回退：vanguard → balance → swift。

### Setup 向导没显示模型列表

模型列表从提供商异步加载。如果提供商的 `/models` 接口慢或被屏蔽，可以
手动输入模型名。用 `/setup` → 选择订阅 → 输入模型名。

## 渠道

### 如何连接飞书？

1. 在[飞书开放平台](https://open.feishu.cn)创建应用
2. 启用机器人能力和事件订阅
3. 添加所需权限（`im:message`、`im:message.receive_v1`、
   `im:message:send_as_bot`、`contact:user.base:readonly`）
4. 在 `~/.xbot/config.json` 中添加凭据：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx"
  }
}
```

详见[飞书渠道指南](/zh-cn/channels/feishu/)。

### 可以限制谁能和机器人对话吗？

可以。用 `allow_from` 字段设置白名单用户 ID：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxx",
    "app_secret": "xxx",
    "allow_from": ["ou_xxx", "ou_yyy"]
  }
}
```

所有渠道（飞书、QQ、NapCat）都支持。

## TUI / CLI

### Agent 响应慢，如何提升性能？

- 定期使用 `/compress` 保持上下文精简
- 简单任务切换到更快的模型（`Ctrl+L`）
- 用子 Agent 做并行工作（各自有独立上下文）
- 增大 `max_concurrency` 以支持并行工具执行
- 长时间运行的命令使用后台模式

### 如何切换会话？

打开侧边栏（默认始终可见），点击任意会话切换。或用 `/sessions` 列出，
`/su` 切换，`/new` 新建。

### 如何更换主题？

`Ctrl+K → Theme`，或输入 `/palette theme`。也可以创建自定义主题。

### Agent 响应慢，如何查看 token 用量？

输入 `/context` 查看当前 prompt token 用量和上下文条。用 `/clear` 重置对话，
或 `/compress` 手动压缩。

## 沙箱

### 需要启用 Docker 沙箱吗？

如果 Agent 执行不受信任的命令或在共享环境中工作，建议启用。Docker 沙箱隔离
Shell 执行。个人开发用默认的 `mode: "none"` 即可。

详见[沙箱指南](/zh-cn/guides/sandbox/)。

## 故障排查

### CLI 连接 Server 报"connection refused"

确保服务器在运行：`xbot-cli serve`。检查 `~/.xbot/config.json` 中的
`cli.server_url` 和 `cli.token` 是否匹配服务器的 `admin.token`。

### MCP 工具不显示

Agent 动态发现 MCP 工具。用 `ManageTools` 工具列出和管理 MCP Server。MCP Server
通过 stdio 或 HTTP 连接——检查可执行文件路径是否正确且可从 xbot 进程 PATH 访问。

### 子 Agent 似乎卡住

子 Agent 在自己的上下文中运行。如果子 Agent 卡住了，可以用 `Ctrl+C` 中断，
或通过子 Agent 面板（`Ctrl+T`）查看其进度。

### Agent 无法访问工作目录外的文件

Agent 的工作目录通过 `work_dir` 配置或从启动 `xbot-cli` 的目录继承。Agent 可以用
`Cd` 工具导航。如果文件在预期目录之外，告诉 Agent 完整路径。

### 上下文填得太快

用 `/compress` 总结旧消息释放上下文空间。要从头开始，用 `/clear`。用 `/context`
查看 token 用量。长会话考虑用子 Agent 委派工作——它们有独立的上下文窗口。

### 如何导出对话

让 Agent："把这个对话导出为 Markdown"——它会用 `FileCreate` 工具将对话写入文件。
也可以用 `/rewind` 回退到对话中的特定时间点。

### Agent 重复同样的操作

如果 Agent 陷入循环，用 `Ctrl+C` 中断，然后 `/clear` 重置对话。也可以用
`context_edit` 删除可能导致循环的特定消息。

{{< hint type=note >}}
**需要更多帮助？** 查看[完整文档](/zh-cn/)或在
[GitHub](https://github.com/ai-pivot/xbot/issues) 提 issue。
{{< /hint >}}
