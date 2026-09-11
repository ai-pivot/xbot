# xbot 安装与首次配置（Agent 可执行版）

> 面向 AI agent：每一步都可直接复制执行、结果可断言。
> 人工安装说明见仓库 README 与文档站；本文件是**可执行**版本。
> 全流程已在 Debian 13 x86_64 上实测走通（装 → 起 web → 注册 → 配 LLM → 收到真实回复）。

---

## 0. 前置条件

| 项 | 要求 |
|---|---|
| OS | Linux / macOS（x86_64、arm64），Windows 见 `install.ps1` |
| 网络 | 能访问 GitHub（中国大陆用镜像，见 §1） |
| LLM | 一个 **OpenAI 兼容**端点：`base_url` / `api_key` / `model` |

`base_url` 必须带 scheme 与版本路径，例如 `https://your-endpoint/v1`。
**写错成 `/v1` 或留空会在首次聊天时报** `Post "/chat/completions": unsupported protocol scheme ""`。

---

## 1. 安装（一条命令）

```bash
# Linux / macOS
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh | bash
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.ps1 | iex
```

中国大陆（GFW 后，自动选镜像）：

```bash
curl -fsSL https://ghfast.top/https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install-cn.sh | bash
```

**非交互 / CI**（无 tty 也能装，全部走参数）：

```bash
curl -fsSL https://raw.githubusercontent.com/ai-pivot/xbot/master/scripts/install.sh \
  | MODE=standalone CHANNEL=stable PORT=8082 bash
```

安装脚本接受的环境变量：

| 变量 | 取值 | 默认 |
|---|---|---|
| `MODE` | `standalone`（CLI 本地跑）/ `server-client`（装本地服务） | `standalone` |
| `CHANNEL` | `stable` / `beta` / `nightly` | `stable` |
| `PORT` | server 端口 | `8082` |
| `GH_MIRROR` | 例如 `ghfast.top` | 空（直连） |

安装产物：

| 组件 | 位置 |
|---|---|
| CLI + server 二进制 | `~/.local/bin/xbot-cli` |
| 配置 | `~/.xbot/config.json` |
| Web UI | `~/.xbot/web/dist` |
| 内置插件 | `~/.xbot/plugins/builtin` |

断言安装成功：

```bash
export PATH="$HOME/.local/bin:$PATH"
xbot-cli --version                 # 打印版本
ls ~/.xbot/web/dist/index.html     # Web UI 已就位
```

---

## 2. 启动 Web（server 模式）

前台（调试用）：

```bash
xbot-cli serve                     # 默认 http://localhost:8082
```

后台常驻（recommended，systemd --user）：

```bash
mkdir -p ~/.config/systemd/user
cat > ~/.config/systemd/user/xbot.service <<'UNIT'
[Unit]
Description=xbot server
After=network.target

[Service]
Type=simple
ExecStart=%h/.local/bin/xbot-cli serve
Restart=always
RestartSec=3
WorkingDirectory=%h

[Install]
WantedBy=default.target
UNIT
systemctl --user daemon-reload
systemctl --user enable --now xbot
```

断言服务就绪：

```bash
systemctl --user is-active xbot                                          # active
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:8082/          # 200
curl -s http://127.0.0.1:8082/api/auth/config                            # {"invite_only":...}
```

> **Web 打不开先查 `config.json` 的 `web.enable`** —— 它必须是 `true`，且 `web.port` 与 §1 的 `PORT` 一致。
> standalone 模式历史上默认不写这两项，装完 web 会「看起来没起来」。

---

## 3. 注册首个账号（免邀请码）

**新装机的 `web_users` 表为空时，第一个注册无需邀请码**（bootstrap：`web_auth.go` 在
`invite_only && 表为空` 时放行）。注册成功后入口自动关闭，后续账号由该管理员邀请。

```bash
curl -s -X POST http://127.0.0.1:8082/api/auth/register \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<your-password>"}'
# → {"ok":true,"user_id":1}
```

浏览器路径：打开 `http://<host>:8082` → 注册页 → 填账号密码。

---

## 4. 配置 LLM

> ⚠️ **运行时以数据库订阅（`user_llm_subscriptions`）为准**，`config.json` 的 `llm` 段只是
> **首次启动时的播种来源**。装完再手改 `config.json` **不会生效** —— 订阅已经播种好了。

### 4a. Web UI（推荐）

1. 登录 `http://<host>:8082`
2. 左下角 **齿轮 → LLM**
3. **添加订阅**：填 `base_url`（带 `https://` 与 `/v1`）、`api_key`
4. 点 **刷新模型列表**，选一个模型
5. 设为默认，保存

### 4b. 无头 / 脚本

`xbot-cli` 提供订阅管理命令；也可以直接调 RPC。
**最省事的是先在 `config.json` 里写好 `llm` 段，再让它播种**（只在**尚无订阅**时生效）：

```bash
python3 - <<'PY'
import json, os
p = os.path.expanduser("~/.xbot/config.json")
d = json.load(open(p))
d["llm"].update({
    "provider": "openai",
    "base_url": "https://your-endpoint/v1",
    "api_key":  "<your-key>",
    "model":    "<your-model>",
})
json.dump(d, open(p, "w"), indent=2, ensure_ascii=False)
PY
# 关键：删掉已播种的空订阅，让下一次启动用它重新播种
sqlite3 ~/.xbot/xbot.db "DELETE FROM user_default_model; DELETE FROM user_llm_subscriptions;"
systemctl --user restart xbot
```

---

## 5. 验证聊天

```bash
BASE=http://127.0.0.1:8082
curl -s -X POST $BASE/api/auth/login -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<your-password>"}' -c /tmp/xbot.ck     # {"ok":true,...}

curl -s -X POST $BASE/api/message -H 'Content-Type: application/json' -b /tmp/xbot.ck \
  -d '{"chat_id":"chat-check","content":"用一句话介绍你自己"}'
# → {"ok":true,"data":{"turn_id":1,"queued":false,...}}
```

**回复查哪里**：v55 起回复文本**不在** `session_messages.content`（那里是空占位行），
而在 **`iteration_history`**：

```bash
sqlite3 ~/.xbot/xbot.db \
  "SELECT iteration, substr(content,1,200), tokens, ttft_ms, model
     FROM iteration_history ORDER BY id DESC LIMIT 3;"
```

成功的样子（实测样例）：

```
1|我是 xbot，一个在 CLI 界面中工作的专家级软件工程师…|42|1984|macaron-v1-venti
```

失败的样子：`content` 为空且出现 `reqerr` 工具项，`detail` 里是
`unsupported protocol scheme ""` → §4 的 `base_url` 没配对。

---

## 排错速查

| 现象 | 原因 | 处置 |
|---|---|---|
| Web 打不开（端口没监听） | `web.enable=false` / `web.port` 与启动不一致 | 改 `config.json` 或重跑 `PORT=8082` 安装 |
| 聊天报 `unsupported protocol scheme ""` | 订阅 `base_url` 为空 | §4 配好 base_url（含 `https://…/v1`） |
| 注册返回 `registration is invite-only` | 已有账号 | 用现有管理员登录，或在 Web UI 邀请 |
| `xbot-cli setup` 报 `LLM 服务调用失败` | 当前 LLM 配置不可用 | 先按 §4 配好 LLM，再跑 setup |
| 改了 `config.json` 没变化 | 订阅已播种，运行时读 DB | 见 §4b（清订阅后重启） |
| 安装脚本提示 `does not support the setup subcommand` | 装到了早于 `setup` 的旧 release | 用 `CHANNEL=nightly`，或等新 stable 发布 |

---

## 卸载

```bash
systemctl --user disable --now xbot 2>/dev/null || true
rm -f ~/.config/systemd/user/xbot.service
rm -rf ~/.xbot ~/.local/bin/xbot-cli
```
