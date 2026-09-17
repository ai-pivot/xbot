# xbot.ssh-runner — Remote Machines (SSH)

内置 **stdio 插件**（独立 Go module，仅依赖 `plugin/protocol`，零外部依赖）。

## 模型：VS Code Remote 式 SSH 管道

**runner 不是远端常驻服务**。每次连接由我们这侧发起一条 SSH 会话，`xbot-runner` 在该会话的
**前台**运行 —— 管道断 = runner 死。四条硬契约：

| 契约 | 做法 |
|---|---|
| 连接由我们发起 | supervisor 持有长时 SSH 会话；远端命令用 `exec` 起 runner（**不是** `nohup`/`&`） |
| 每次（重）连**先杀老 runner** | 远端脚本**开头**执行 `pkill -f 'xbot-runner.*--name[= ]<name>([[:space:]]\|$)'`（名字锚定+转义，不会误杀 `m10`），随后才 `exec` 新进程 ⇒ 同名永不并存两个 runner |
| 断线自动重连 | 会话结束即退避重试（1s→15s 封顶），重连再次「杀老 + 起新」；`status.restarts` 可见 |
| server 不装到远端 | `tunnel`（默认）用 `ssh -R` 把远端 `127.0.0.1:<rport>` 转发到我们这里的 server，runner 只 dial `127.0.0.1:<rport>` ⇒ **远端无需任何到 server 的网络可达性**（内网/NAT 均可）。`direct` 为显式选择，runner 直连 `--server` 地址（**无静默回退**） |

```
ssh [-R 127.0.0.1:<rport>:127.0.0.1:<sport>] <host> \
    '<pkill 老 runner>; exec <install_dir>/xbot-runner --server ws://127.0.0.1:<rport>/ws --token … --name …'
```

`provision` 只负责**把二进制装好**；连接由 `connect` 建立。

## 职责边界

控制面**只做「纳管 + 装机 + 连接」**；**执行链路不归本插件**：runner 装好并由 `connect`
拉起后，它自己通过 WebSocket 连回 server，工具执行仍走既有 runner 协议。

```
浏览器面板 ──ctx.rpc.call('xbot.ssh-runner.<method>')──▶ 本插件进程 ──ssh──▶ 目标机器
                                                                      （跑 runner，前台随管道）
        ▲                                                                        │
        └──────────────── 核心 RPC（runner_create / runner_session_set / …）◀────┘
```

## RPC 方法

前端经 `ctx.rpc.call('xbot.ssh-runner.<method>', params)` 调用。

| 方法 | 参数 | 返回 | 说明 |
|---|---|---|---|
| `probe` | `{ssh}` | `{os, arch, user, is_root, has_curl, has_wget, installed_version, install_dir}` | **只读**探测 |
| `provision` | `{ssh, name, download_base, install_dir, dry_run?}` | `{job_id}` | **仅安装**：探测 → 下载 → sha256 校验 → 杀老 runner → 原子落盘。不启动服务 |
| `connect` | `{ssh, name, connect_cmd, install_dir, connection_mode?}` | `{connected, mode, remote_port?, restarts, connected_at?, last_error?}` | 起受管 SSH 会话（先杀老 runner）；立即返回，会话在后台 |
| `disconnect` | `{ssh, name}` | `{connected:false}` | 拆管道 + 杀远端 runner |
| `status` | `{ssh, name}` | `{installed_version, service_state, detail, connected, connection_mode, restarts, connected_at, remote_port, last_error}` | `service_state` ∈ `connected` / `reconnecting` / `disconnected` |
| `logs` | `{ssh, name, lines}` | `{lines[], source}` | `source=ssh-session`（管道输出环形缓冲）或 `remote-log`（旧安装遗留日志） |
| `deprovision` | `{ssh, name, uninstall}` | `{job_id}` | 拆管道 → 杀进程 →（可选）删二进制 |
| `job_status` | `{job_id}` | `{state, steps[{name,ok,detail}], error}` | `provision` / `deprovision` 的异步进度 |

`connection_mode`：`tunnel`（默认）| `direct`。插件配置 `connectionMode` 提供默认值，targets 条目可覆盖。

## 为什么必须异步

宿主对插件 RPC 有 **30s 硬超时，且超时会直接杀掉插件进程**（`plugin/runtime.go`）——
被杀则**所有管道一起断**。所以 `provision` / `deprovision` 返回 `job_id` 由前端轮询；
`connect` 只是把 supervisor 装好就返回（长时 SSH 会话在后台持有）。

## 自愈（`state.json`）

管道由本插件进程持有 ⇒ 进程重启（server 重启 / 30s RPC 超时被杀）会断开全部管道。
`connect` 成功后把监督项写入插件目录的 `state.json`（**0600**，tmp+rename 原子写），
`Activate` 时对 `auto_connect: true` 的条目**自动重新 connect**（自动重开管道；重连又会先杀老 runner）。
**opt-in**：只有勾选 autoConnect 的目标才自愈；失败仅记日志、条目保留待重试，不阻塞启动。
`connect_cmd` 含 runner token（同机 DB 里本就明文存有），故文件严格 0600。

## 安全

- **SSH 凭据不落库、不进日志**：只存用户给的 SSH 命令/别名；私钥沿用 server 主机的 `~/.ssh` + agent。
- 日志中的 ssh 参数做脱敏（`user@host` 之外打码）。
- 每次 RPC 自带 `ssh`/`name`，插件不缓存凭据。
- `pkill` 模式**锚定 runner 名并转义正则元字符**，不会波及其它进程。
