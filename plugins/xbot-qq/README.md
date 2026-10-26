# xbot-qq — QQ 渠道插件

把 QQ（OneBot v11）作为 xbot 的**正式 channel** 接入：私聊与群聊消息进入 agent，
agent 的回复发回 QQ。

形态是 xbot 的标准 **stdio 渠道插件**（`plugin.json` 里 `runtime: "stdio"`）：
xbot 用 `entry` 拉起一个独立进程，双方用 JSON lines over stdin/stdout 通信，
协议与 WebSocket 客户端同一套（见仓库 `plugin/protocol/protocol.go`）。
不是独立脚本，也不是外挂服务。

```
plugins/xbot-qq/
├── plugin.json        渠道声明（id=xbot.qq，channel 名=qq，config_schema）
├── main.go            协议主循环 + activate / channel_config / channel_send
├── config.go          配置模型（宽松解析 + 严格校验 + 日志脱敏）
├── patterns.go        白名单模式匹配（精确项 / 通配项）
├── policy.go          访问策略（准入 ShouldHandle / 权限 IsAdmin）
├── skeleton.go        阶段 2/3 预留结构（身份注入 / slash 门禁 / 重连退避）
├── *_test.go          单测
└── Makefile           build / test / vet / install
```

## 阶段状态

| 阶段 | 内容 | 状态 |
|---|---|---|
| 1 | 插件骨架 · 配置模型 · 白名单/管理员解析 · 单测 | **完成** |
| 2 | OneBot 连接 · 渠道注册接线 · 身份注入 · 渠道提示词落盘 | 未开始 |
| 3 | slash 门禁 · 断线重连 | 未开始 |

阶段 1 的进程**不发起任何网络连接**：`channel_config` 只解析并记录，
`channel_send` 一律回 `error`（诚实失败，不假装发出去）。

## 配置

配置存在 `config.json` → `channels.qq`（`map[string]string`，由 xbot 推给插件）。

```json
{
  "channels": {
    "qq": {
      "enabled": "true",
      "ws_url": "ws://127.0.0.1:3001",
      "access_token": "…",
      "allow": "10001, group:20002",
      "admins": "10001"
    }
  }
}
```

| 键 | 含义 |
|---|---|
| `enabled` | 是否连接 OneBot（`true/1/yes/on`） |
| `ws_url` | OneBot v11 正向 WS 地址，`ws` / `wss` / `http` / `https` |
| `access_token` | OneBot token（日志里只打长度） |
| `allow` | **消息白名单**：决定哪些发送者的消息交给 agent |
| `admins` | **管理员名单**：决定谁是管理员 |

### 白名单与管理员语义

两个名单共用同一套匹配规则（`patterns.go`）：

- **精确项**：全串相等，如 `10001`、`group:20002`；
- **通配项**：`*` 任意长度（含空）、`?` 恰好一个字符，如 `*`、`group:*`、`1000?`；
- 分隔符宽容：英文/中文逗号、分号、空白、换行都行；
- **空名单 = 谁都不匹配**（fail-closed）：漏配白名单的后果是"渠道不工作"，不是"对所有人开放"；
- **要处理所有消息，必须显式写 `*`**；
- 空发送者一律拒绝（没有身份的消息无法审计）。

`Policy` 把两件事分开：`ShouldHandle(sender)` 是准入，`IsAdmin(sender)` 是权限。
被白名单拒绝的管理员**仍然是管理员**（阶段 3 的 slash 门禁需要这个信息来给准确的拒绝理由）。

> ⚠️ 权限的最终权威在 host 侧 `config.AgentConfig.IsAdminSender`（`cli`/`web` 恒 admin，
> 其他渠道看 `agent.admins`）。因此 QQ 号必须**同时**写进 `agent.admins` 与
> `channels.qq.admins`，否则插件放行而 host 判非管理员（或反之）。

## 构建 / 测试

```bash
cd plugins/xbot-qq
make build    # → bin/qq-plugin
make test     # 单测
make vet      # go vet
```

安装（拷到 `~/.xbot/plugins/xbot.qq/`，随后 reload plugins）：

```bash
make install
```

仓库根目录的 `make plugins-build` 已经包含本插件；`plugins/package.sh`
也会按 `plugin.json` 的 `entry` 交叉编译它（放行到 release tarball）。

## 协议速查

| 方向 | 帧 |
|---|---|
| host → plugin | `{"method":"activate","params":{"pluginId":"xbot.qq"}}` |
| plugin → host | `{"result":"ok","channel_provider":{"name":"qq","config_schema":[…]}}` |
| host → plugin | `{"type":"channel_config","metadata":{"config":"{…}"}}` |
| host → plugin | `{"id":"srv-1","method":"channel_send","params":{"chat_id":"…","content":"…"}}` |
| plugin → host | `{"id":"srv-1","result":"…"}` 或 `{"id":"srv-1","error":"…"}` |
| plugin → host | `{"id":"plugin-1","method":"send_inbound","params":{…}}`（阶段 2） |
| host → plugin | `{"method":"deactivate"}` → 进程必须自行退出 |

未知 RPC（有 `id`）一定回 `error`，否则 host 侧调用方会一直等到超时；
未识别的异步事件（`stream_content` / `text` / `session` …）安全忽略。
