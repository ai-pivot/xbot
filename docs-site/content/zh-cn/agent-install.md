---
title: "Agent 安装手册"
weight: 11
---

# Agent 安装手册（可执行版）

> **给 AI agent 用**：每条命令都能直接执行，每个结果都能断言。
> 人读版本见 [安装与配置](/zh-cn/installation/)。
> 全流程已在 Debian 13 x86_64 实测走通：装 → 起 web → 注册 → 配 LLM → 收到真实回复。

## 0. 前置条件

| 项 | 要求 |
|---|---|
| OS | Linux / macOS（x86_64 / arm64）；Windows 用 `install.ps1` |
| 网络 | 能访问 GitHub（中国大陆用镜像，见下） |
| LLM | 一个 OpenAI 兼容端点：`base_url` + `api_key` + `model` |

`base_url` 必须带 scheme 和版本路径，例如 `https://your-endpoint/v1`。**写错或留空会在首次聊天时报
`Post "/chat/completions": unsupported protocol scheme ""`。**

## 1. 安装

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
```

中国大陆（自动选镜像）：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

非交互 / CI（无 tty 也能装，全走环境变量）：

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh \
  | MODE=standalone CHANNEL=stable PORT=8082 bash
```

可选环境变量**只有这 5 个**：

| 变量 | 取值 | 默认 |
|---|---|---|
| `MODE` | `standalone`（`xbot-cli serve` 按需启动）｜`server-client`（装成常驻服务） | `standalone` |
| `PORT` | Web UI + WebSocket 端口 | `8082` |
| `XBOT_HOME` | 数据目录 | `~/.xbot` |
| `INSTALL_PATH` | 二进制目录 | `~/.local/bin` |
| `CHANNEL` | `stable`｜`beta`｜`nightly` | `stable` |

另有内部变量：`GH_MIRROR`（CDN 镜像）、`NONINTERACTIVE=1`（跳过一切交互）。

**脚本幂等**：重跑不会破坏已有配置（已有值一律保留）。

产物：

| 组件 | 位置 |
|---|---|
| CLI + server 二进制 | `~/.local/bin/xbot-cli` |
| 配置 | `~/.xbot/config.json` |
| Web UI | `~/.xbot/web/dist` |
| 内置插件 | `~/.xbot/plugins/builtin` |
| 数据库 | `~/.xbot/xbot.db` |

断言安装成功：

```bash
export PATH="$HOME/.local/bin:$PATH"
xbot-cli --version                    # 打印版本
xbot-cli setup --check                # 完整性自检：全过打印 "all good" 退出 0；缺件退出 1
ls ~/.xbot/web/dist/index.html        # Web UI 已就位
```

> `setup --check` 非 0 时重跑 `xbot-cli setup` 修复。旧 release 缺插件包时用 `CHANNEL=nightly` 重装。

## 2. 启动 Web

```bash
xbot-cli serve                        # 前台；端口取 config.json 的 web.port（默认 8082）
```

**要让服务常驻**，用安装脚本的 server-client 模式（它会写好 systemd --user unit 并 enable/start，
不需要手工建 unit）：

```bash
MODE=server-client bash install.sh    # 服务名固定为 xbot-server
```

管理命令：

```bash
systemctl --user status xbot-server
systemctl --user restart xbot-server
journalctl --user -u xbot-server -f
```

断言服务就绪（**必须看到 200 才算起来**）：

```bash
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/       # 200
```

> 本地 curl 若被代理拦截，加 `--noproxy '*'`。
> 端口没监听先查 `config.json` 的 `web.enable`（必须 `true`）与 `web.port`。

## 3. 注册首个账号（免邀请码）

`web_users` 表为空时放行首次注册（bootstrap），之后入口自动关闭。

```bash
curl -s -X POST http://127.0.0.1:8082/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<your-password>"}'
# → {"ok":true,"data":{"user_id":1},"error":null}
```

## 4. 配置 LLM

> ⚠️ **运行时以数据库订阅（`user_llm_subscriptions`）为准**，`config.json` 的 `llm` 段只是**首次启动的播种来源**。
> 装完再手改 `config.json` **不生效** —— 除非按下面的方式清掉已播种订阅让它重新播种。

**4a. Web UI（推荐）**：登录 → 齿轮 → **LLM** → 添加订阅（`base_url` 带 `https://…/v1` + `api_key`）
→ 刷新模型列表选模型 → 设为默认 → 保存。

**4b. 无头 / 脚本**：

```bash
python3 - <<'PY'
import json, os
p = os.path.expanduser("~/.xbot/config.json")
d = json.load(open(p))
d["llm"].update({"provider": "openai", "base_url": "https://your-endpoint/v1",
                 "api_key": "<your-key>", "model": "<your-model>"})
json.dump(d, open(p, "w"), indent=2, ensure_ascii=False)
PY
# 关键：删掉已播种的空订阅，让下一次启动重新用它播种
sqlite3 ~/.xbot/xbot.db "DELETE FROM user_default_model; DELETE FROM user_llm_subscriptions;"
systemctl --user restart xbot-server      # 前台跑的话：Ctrl-C 后重跑 xbot-cli serve
```

## 5. 验证聊天

```bash
BASE=http://127.0.0.1:8082
curl -s -X POST $BASE/api/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<your-password>"}' -c /tmp/xbot.ck

curl -s -X POST $BASE/api/message -H 'Content-Type: application/json' -b /tmp/xbot.ck \
  -d '{"chat_id":"chat-check","content":"用一句话介绍你自己"}'
# → {"ok":true,"data":{"turn_id":1,"queued":false,...}}
```

**回复文本查哪里**：v55 起不在 `session_messages.content`（那是空占位行），而在 **`iteration_history`**：

```bash
sqlite3 ~/.xbot/xbot.db \
  "SELECT iteration, substr(content,1,200), tokens, ttft_ms, model
     FROM iteration_history ORDER BY id DESC LIMIT 3;"
```

成功样例：`1|我是 xbot，一个…|42|1984|macaron-v1-venti`
失败：`content` 为空 + `detail` 里 `unsupported protocol scheme ""` → 第 4 节 `base_url` 没配对。

## 排错速查

| 现象 | 原因 | 处置 |
|---|---|---|
| 端口没监听 / Web 打不开 | `web.enable=false`，或 `web.port` 与实际端口不一致 | 改 `config.json`，或重跑 `PORT=8082` |
| 聊天报 `unsupported protocol scheme ""` | 订阅 `base_url` 为空 | 按第 4 节配好（含 `https://…/v1`） |
| 注册返回 `registration is invite-only` | 已有账号 | 用现有管理员登录，或在 Web UI 邀请 |
| `setup --check` 报某组件 MISSING | 缺件 | `xbot-cli setup`；离线用 `--offline-web/--offline-plugins` |
| 改了 `config.json` 没变化 | 订阅已播种，运行时读 DB | 见 4b（清订阅后重启） |
| 提示 `does not support the setup subcommand` | 装到了早于 `setup` 的旧 release | `CHANNEL=nightly` 重装 |
| `command not found: xbot-cli` | `~/.local/bin` 不在 PATH | `source ~/.bashrc` 或重开终端 |

## 卸载

```bash
systemctl --user disable --now xbot-server 2>/dev/null || true
rm -f ~/.config/systemd/user/xbot-server.service
rm -f ~/.local/bin/xbot-cli
rm -rf ~/.xbot          # 数据目录（含数据库，谨慎）
```
