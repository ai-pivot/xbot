---
title: "快速开始"
weight: 5
---

# 快速开始

5 分钟内让 xbot 跑起来。

{{< hint type=note >}}
**系统要求：** 任何现代操作系统（Linux、macOS、Windows）。无需预装依赖——安装器会下载静态二进制。
{{< /hint >}}

## 1. 安装

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

安装器会下载 `xbot-cli` 二进制、生成随机 admin token、写入
`~/.xbot/config.json`。Server 模式还会安装系统服务并下载 Web UI。

{{< hint type=note >}}
**国内网络？** 使用镜像加速安装器：
```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```
{{< /hint >}}

## 2. 选择模式

安装器会让你选择：

| | Standalone（单机） | Server（服务端） |
|--|-----------|--------|
| 架构 | CLI 直接运行 Agent | 后台 Server + CLI 远程连接 |
| 适合 | 个人 | 团队、多渠道 |
| 渠道 | 仅 CLI | 飞书 · QQ · Web · CLI |
| LLM 共享 | 各自配置 | 管理员配一次，全团队用 |
| 持久化 | 关终端就停 | 系统服务，开机自启 |

> **大多数团队应选 Server 模式。** 个人快速体验选 Standalone。

## 3. 配置 LLM

运行 `xbot-cli`，首次启动会弹出 **Setup 向导**：

1. 选择 LLM 提供商（OpenAI / Anthropic / 兼容 API）
2. 输入 API Key
3. 配置 API 地址（DeepSeek、Qwen、Ollama 等需修改）
4. 选择模型
5. 配置模型层（Vanguard / Balance / Swift）

随时用 `/setup` 或 `Ctrl+K → Setup` 重新配置。

> **订阅系统，非单一全局 key。** xbot 使用订阅系统，可创建多个订阅（如工作用
> Claude、个人用 DeepSeek），按会话切换。

{{< hint type=tip >}}
**使用 DeepSeek、通义千问或 Ollama？** 在 Setup 向导中设置 `provider: "openai"` 并修改
`base_url`。xbot 兼容任何 OpenAI 兼容 API。
{{< /hint >}}

## 4. 开始对话

准备就绪。输入消息按回车，Agent 会调用工具、执行命令、搜索网页、委派子 Agent。

### 立即试试

```text
你：这个目录里有哪些文件？

Agent：*使用 Shell 工具运行 ls*
Agent：当前目录包含...

你：读取 README.md 并总结

Agent：*使用 Read 工具*
Agent：这个项目是关于...

你：创建一个名为 notes.txt 的文件，写入今天的日期

Agent：*使用 FileCreate 工具*
Agent：完成！已创建 notes.txt 并写入今天的日期。

你：搜索最新的 Go 版本

Agent：*使用 WebSearch 工具*
Agent：最新的 Go 版本是...
```

Agent 会自动选择合适的工具——你只需描述你想要的。

输入 `/` 查看所有命令。常用命令：

| 命令 | 作用 |
|------|------|
| `/setup` | 重新配置 LLM、沙箱、主题 |
| `/context` | 查看 token 用量 |
| `/clear` | 清空对话 |
| `/new` | 新建会话 |
| `/sessions` | 列出 / 切换会话 |
| `/settings` | 打开设置面板 |
| `/help` | 显示所有命令 |

### 常用快捷键

| 快捷键 | 功能 |
|--------|------|
| `Ctrl+K` | 命令面板（模糊搜索） |
| `Ctrl+N` | 循环切换模型 |
| `Ctrl+P` | 管理订阅（添加/禁用/删除） |
| `Ctrl+T` | 子 Agent 进度面板 |
| `Ctrl+L` | 切换模型 — 可搜索选择器（`订阅名 · 模型名`，按会话） |
| `Ctrl+N` | 轮转下一个模型 |

## 接下来

{{< columns >}}

- [安装指南](/zh-cn/installation/) — 源码构建、服务管理
- [配置参考](/zh-cn/configuration/) — config.json 全字段
- [渠道](/zh-cn/channels/) — 飞书、QQ、Web 配置

<--->

- [功能](/zh-cn/features/) — 工具、技能、子 Agent、MCP、插件
- [沙箱指南](/zh-cn/guides/sandbox/) — Docker 沙箱
- [架构](/zh-cn/architecture/) — 系统设计

{{< /columns >}}
