---
title: "安装与配置"
weight: 10
---

# 安装与配置

## 一条命令

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash

# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

装完就位：**二进制 + Web UI + 全部内置插件（genui / git-fancy / ambience）+ web 通道已开启**。
不需要再装别的东西，也不需要先编辑任何配置。

```bash
xbot-cli serve          # 打开 http://localhost:8082
```

浏览器里：注册账号 → 右下角齿轮 → **LLM** → 填 Base URL / API Key / 选模型 → 开聊。

{{< hint type=note >}}
新装的第一个注册账号免邀请码，它同时是管理员。
{{< /hint >}}

## 让 Agent 帮你装

把下面这段原样复制给你的 AI agent（Claude Code / Codex / Cursor …），它会自己装好并验证：

```text
帮我在这台机器上安装并启动 xbot：

1. 安装：curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
2. 自检：xbot-cli setup --check          # 退出码 0 才算装好
3. 启动：nohup xbot-cli serve >/tmp/xbot.log 2>&1 &
4. 验证：curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:8082   # 必须输出 200

任何一步失败，读下面「排错」一节，不要跳过自检直接说装好了。
```

**Agent 自己来读的话**，有一份专门给 agent 的可执行手册（每步都能断言、含无头配 LLM 与排错表）：

{{< hint type=tip >}}
[**Agent 安装手册（可执行版）**](/zh-cn/agent-install/) —— `https://ai-pivot.github.io/xbot/zh-cn/agent-install/`
{{< /hint >}}

`install.sh` 的头部注释里也有同样的指引；脚本跑完会在最后打印这份文档的地址，
所以「把上面那段发给 agent」的流程里，agent 自然能看到下一步该读什么。

Agent 侧要点：

- `install.sh` **幂等**，重跑不会破坏已有配置（已有值一律保留）。
- `setup --check` 是唯一的完整性判据，退出码 0 = 六项检查全过。
- `serve` 是前台进程，agent 需自行后台化（`nohup` / `systemd` / `tmux`）。
- 装完后 `~/.xbot/config.json` 已有 web + 插件配置；**不要再手改 `llm.*`** ——
  LLM 配置存在数据库里，改配置文件不生效，用 Web 设置面板或 `xbot-cli` 命令改。

## 可选参数（就这 5 个）

环境变量前缀，写在命令前即可，例如
`MODE=server-client PORT=9000 bash install.sh`：

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `MODE` | `standalone` | `standalone` = `xbot-cli serve` 按需启动；`server-client` = 装成常驻服务（systemd --user / launchd），CLI 远程连接 |
| `PORT` | `8082` | Web UI 与 WebSocket 端口 |
| `XBOT_HOME` | `~/.xbot` | 数据目录（配置、数据库、插件、Web 产物） |
| `INSTALL_PATH` | `~/.local/bin` | 二进制安装目录 |
| `CHANNEL` | `stable` | `stable` / `beta` / `nightly`；`nightly` 是每次 master 推送覆盖的最新构建 |

中国大陆网络走镜像（`GH_MIRROR` 由镜像脚本自动设置）：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

**其余全部配置项** —— LLM 订阅、渠道（飞书 / QQ / Web / CLI）、沙箱与 Runner、
记忆、Hooks、日志、插件 —— 见 [配置参考](/zh-cn/configuration/)。

## 验证安装

```bash
xbot-cli --version        # 版本号
xbot-cli setup --check    # 完整性自检，退出码 0 = OK
```

```bash
# Web 起来了吗（本地 curl 若被代理拦截，加 --noproxy '*'）
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082     # 期望 200

# server-client 模式看服务状态
systemctl --user status xbot-server     # Linux
launchctl list | grep xbot              # macOS
```

## 排错

| 症状 | 处理 |
|------|------|
| `setup --check` 报某组件 MISSING | `xbot-cli setup` 重跑补齐；离线环境用 `--offline-web/--offline-plugins` 指定本地包 |
| 能打开页面但发消息报 `unsupported protocol scheme ""` | LLM 没配。Web → 齿轮 → LLM 填 Base URL + API Key + 选模型（改 `config.json` 无效） |
| 端口被占用 / 想换端口 | `PORT=9000 xbot-cli serve`，或改 `web.port` 后重启 |
| 页面 404 或样式全丢 | Web 产物缺失：`xbot-cli setup` |
| 插件面板空白 | 插件未激活：`xbot-cli setup --config-only` 或 `/plugin reload-all` |
| 提示 `command not found: xbot-cli` | `~/.local/bin` 不在 PATH：`source ~/.bashrc` 或重开终端 |
| 旧 release 装完没有插件 | `xbot-cli setup`；仍失败则用 `CHANNEL=nightly` 重装（nightly 一定带插件包） |

## 升级

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
```

重跑安装脚本即可：二进制覆盖，`config.json` 与数据库原样保留。

## 卸载

```bash
systemctl --user disable --now xbot-server   # server-client 模式
rm -f ~/.local/bin/xbot-cli
rm -rf ~/.xbot                                # 数据目录（含数据库，谨慎）
```

## 从源码构建

```bash
git clone https://github.com/ai-pivot/xbot.git && cd xbot
make setup      # 构建 CLI + Web UI + 内置插件，装进 ~/.xbot 并激活 channel 插件
```

需要 **Go 1.26+**；`make setup` 额外需要 Node.js（构建 Web 前端）。

## 参见

- [配置参考](/zh-cn/configuration/) — `config.json` 全字段、LLM 订阅、模型 tier
- [快速开始](/zh-cn/getting-started/) — 装完之后的第一次对话
- [渠道](/zh-cn/channels/) — 飞书 / QQ / Web / CLI 接入
- [插件](/zh-cn/plugins/) — 插件系统与内置插件
