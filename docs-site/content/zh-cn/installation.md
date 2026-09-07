---
title: "安装指南"
weight: 10
---

# 安装指南

## 安装方式

### 一键安装（推荐）

```bash
# Linux / macOS (amd64, arm64)
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

指定版本或安装路径：

```bash
VERSION=v0.0.48 curl -fsSL ... | bash          # 指定版本
INSTALL_PATH=~/.local/bin curl -fsSL ... | bash  # 自定义安装路径
```

### 从源码构建

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make build          # 构建 xbot (server + runner)
make run            # 构建并运行 server
```

只构建 CLI：

```bash
go build -o xbot-cli ./cmd/xbot-cli
```

一条命令完成本地全套安装（构建 CLI + Web UI + 内置插件，装入 `~/.xbot`，
激活 channel 插件——安装器在源码场景的等价物）：

```bash
make setup           # 需要 Node.js（web 构建）+ Go
```

环境要求：**Go 1.26+**（`make setup` 额外需要 Node.js）。Go 二进制构建本身
不需要 Node.js（Web 产物已提交）。

## 两种安装模式

安装器会让你选择 **Standalone** 或 **Server** 模式。

### Standalone（单机模式）

CLI 直接在本地运行 Agent，不依赖后台服务。

- ✅ 简单，安装即用
- ✅ 无后台进程
- ❌ 关终端就停
- ❌ 仅 CLI 渠道，不支持飞书/QQ/Web
- ❌ 不能团队共享 LLM

**适合**：个人开发者快速体验。

### Server（服务端模式）

后台运行一个 Server 进程，CLI 通过 WebSocket 远程连接。同时启用飞书/QQ/Web 等渠道。

- ✅ Agent 常驻运行，开机自启
- ✅ 支持飞书/QQ/Web 多渠道同时接入
- ✅ Web 浏览器聊天界面
- ✅ 管理员配置 LLM Key，全团队共享使用
- ✅ 多个 CLI 客户端可同时连接

**适合**：团队使用、需要飞书/QQ 接入、需要 Web 界面的场景。

> 💡 **大多数团队应选 Server 模式。**

### Server 模式的服务管理

安装器会自动配置系统服务（无需 sudo）：

| 平台 | 服务方式 |
|------|----------|
| Linux | systemd --user（用户级服务） |
| macOS | launchd（LaunchAgent） |
| Windows | Startup 文件夹 / 计划任务 / nssm 服务 |

Server 启动命令：`xbot-cli serve`

### 安装器做了什么

1. 下载 `xbot-cli` 二进制到 `~/.local/bin/`（或你指定的路径）
2. 生成随机 admin token
3. 写入/更新 `~/.xbot/config.json`
4. 运行 `xbot-cli setup` —— **一条命令装齐本发行版的全部组件**：
   - Web UI 前端 → `~/.xbot/web/dist`（与二进制同一 GitHub release，SHA-256 校验；
     **standalone 和 server 模式都安装**）
   - 内置插件（`xbot.genui`、`xbot.git-fancy`、`xbot.ambience`）→
     `~/.xbot/plugins/builtin/`（版本对齐，含对应平台的插件二进制 +
     git-fancy 前端资产）
   - Channel 激活配置：为随发行的 channel 插件写入
     `channels.<name>.enabled=true`（如 `channels.genui.enabled=true`），
     GenUI（`display_html`）与 Git 面板开箱即用
5. Server 模式额外：安装系统服务（Web UI 服务于 `http://localhost:8082`）

若 release 资产下载失败（离线安装、或旧 release 没有插件 tarball），安装器
只警告不中断——之后随时运行 `xbot-cli setup` 补齐。

## 补齐 / 修复安装：`xbot-cli setup`

`setup` 子命令幂等，可随时重跑：

```bash
xbot-cli setup            # 安装/刷新 Web UI + 插件 + 激活配置
xbot-cli setup --check    # 仅诊断（缺件时 exit 1）
xbot-cli setup --force    # 版本戳匹配也强制重新下载
```

它下载**与当前二进制版本严格对应**的资产（nightly 二进制拉 `nightly` tag，
stable 二进制拉自身版本号），校验 SHA-256，并对已完成安装的版本跳过重复
下载（版本戳：`~/.xbot/web/.dist-version` 与 `~/.xbot/plugins/.builtin-version`）。

离线机器：从 [releases 页面](https://github.com/ai-pivot/xbot/releases)手动下载
两个文件后本地安装：

```bash
xbot-cli setup --offline-web xbot-web-dist.tar.gz \
               --offline-plugins xbot-plugins-$(go env GOOS)-$(go env GOARCH).tar.gz
```

升级（重新跑 `curl ... install.sh | bash`）后运行一次 `xbot-cli setup` ——
新二进制版本号会自动刷新两个组件。

## 首次配置

安装完成后运行：

```bash
xbot-cli
```

### Setup 向导

首次运行会自动弹出 Setup 向导，引导你配置：

**LLM 订阅配置**
1. 选择 LLM 提供商（OpenAI / Anthropic / 自定义兼容 API）
2. 输入 API Key（**必填**）
3. 输入 API 地址（默认 `https://api.openai.com/v1`，使用兼容服务时修改）
4. 选择模型
5. 配置模型层（Vanguard / Balance / Swift，可按不同场景选用不同模型）
6. Tavily 搜索 Key（可选，不填则无法使用网页搜索）

**环境配置**
- 沙箱模式（默认 `none`，Docker 用户选 `docker`）
- 记忆模式（默认 `flat`）

**外观**
- 配色方案（9 种可选）

配置完成后即可开始对话。随时可用 `/setup` 命令或 `Ctrl+K → Setup` 重新配置。

### 手动编辑配置

配置文件位于 `~/.xbot/config.json`，也可以直接编辑。详见 [配置参考](/zh-cn/configuration/)。

**最小配置（Standalone 模式）：**

```json
{
  "subscriptions": [
    {
      "name": "default",
      "provider": "openai",
      "api_key": "sk-xxx",
      "model": "gpt-4o"
    }
  ]
}
```

**使用 DeepSeek 等兼容 API：**

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

## 验证安装

```bash
# 查看版本
xbot-cli --version

# Server 模式检查服务状态
# Linux:
systemctl --user status xbot-server
# macOS:
launchctl list | grep xbot
```

{{< hint type=tip >}}
**快速健康检查：** 运行 `xbot-cli` 并输入"你好"。如果 Agent 回复了，说明一切正常。
{{< /hint >}}

## 参见
- [快速开始](/zh-cn/getting-started/) — 5 分钟快速上手
- [配置参考](/zh-cn/configuration/) — config.json 全字段
- [渠道](/zh-cn/channels/) — 飞书、QQ、Web、CLI 配置
