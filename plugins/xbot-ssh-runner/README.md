# xbot.ssh-runner — Remote Machines (SSH)

内置 **stdio 插件**（独立 Go module，仅依赖 `plugin/protocol`，零外部依赖）。

职责边界：**控制面只做「纳管 + 装机」**——用一条 SSH 命令探测目标机器、下载并安装
`xbot-runner`、注册为 systemd user unit（或 nohup 兜底）并启动。
**执行链路不归本插件**：装好之后的 `xbot-runner` 自己通过 WebSocket 连回 server，
工具执行仍走既有 runner 协议。

```
浏览器面板 ──ctx.rpc.call('xbot.ssh-runner.<method>', …)──▶ 本插件进程 ──ssh──▶ 目标机器
                                                                        （安装并启动 xbot-runner，
                                                                          之后由它自己连回 server）
```

## 构建 / 安装

```bash
cd plugins/xbot-ssh-runner
make build                 # → bin/ssh-runner-plugin
make vet test              # go vet ./... / go test ./...
make install               # 装到 ~/.xbot/plugins/xbot.ssh-runner/
make install PLUGIN_DIR=/tmp/x   # 或指定目录
```

## RPC 方法（前端经 `web_plugin_rpc` 调用）

| 方法 | 参数 | 返回 | 同步性 |
|---|---|---|---|
| `probe` | `{ssh, install_dir?}` | `{os, arch, user, is_root, has_systemd, has_curl, has_wget, installed_version, install_dir}` | 同步（只读，15s 超时） |
| `provision` | `{ssh, name, connect_cmd, download_base?, install_dir?, service_mode?, dry_run?}` | `{"job_id":"…"}` **立即返回** | 异步（后台 job） |
| `job_status` | `{job_id}` | `{job_id, kind, name, state: running\|done\|failed, steps:[{name,ok,detail}], error}` | 同步（只查内存表，立即返回） |
| `deprovision` | `{ssh, name, uninstall?}` | `{"job_id":"…"}` | 异步（后台 job） |
| `status` | `{ssh, name}` | `{installed_version, service_state, detail}` | 同步（只读） |
| `logs` | `{ssh, name, lines?}` | `{lines: [...]}` | 同步（只读，lines 默认 200、上限 2000） |

未知方法返回 `rpcErr("unknown method: X")`。

**为什么 provision 必须异步**：宿主对单个插件 RPC 有 30s 硬超时，超时**会杀掉插件进程**
（`plugin/runtime.go` 的 `pluginCallTimeout`）。装机含下载 10–20MB + 多轮 ssh，必然超时，
所以长任务一律在后台 goroutine 里跑，状态经 `job_status` 轮询。

`provision` 的 job 步骤（成功路径）：

```
detect → prepare-dir → download → verify → stop-old → install → service → start
```

- `service_mode=systemd`（默认）：写 `~/.config/systemd/user/xbot-runner-<name>.service`
  （`ExecStart=<install_dir>/xbot-runner <connect_cmd 参数>`、`Restart=always`、`RestartSec=5`），
  `systemctl --user daemon-reload && enable --now`；远端无 systemd（或 `--user` 起不来，
  如无用户 D-Bus）时**自动降级为 nohup**，并在 `start` 步骤 detail 里标注。
- `service_mode=nohup`：`nohup <bin> <args> >> ~/.xbot-runner/<name>.log 2>&1 &` + pid 文件。
- 幂等：重复 provision 同名目标会先停旧服务（systemd stop / 杀 pid 文件的进程），
  二进制用同目录内 `install + mv` 原子替换，不会叠加多个进程、也不会撞 `ETXTBSY`。
- `dry_run=true`：只做只读探测（detect/prepare-dir），其余步骤以 `dry-run: …` 计划记录，
  不下载、不写盘。

## SSH 执行规范

- `ssh` 参数是用户给的**完整命令前缀**，例如 `ssh user@1.2.3.4 -p 2222`、`ssh -i ~/.ssh/k user@host`。
  实现按 `strings.Fields` 拆词后原样传给本机 ssh 客户端，**脚本作为最后一个 argv 元素**
  （远端登录 shell 执行它）。
- 统一注入（**用户已自带同名 `-o` 选项则跳过**，大小写不敏感）：
  `-o BatchMode=yes -o ConnectTimeout=10 -o StrictHostKeyChecking=accept-new`。
  默认项插在程序名之后、用户参数之前，保证用户把 host 写在前面（`ssh user@host -p 2222`）时
  默认项也一定被 ssh 解析。用户的参数顺序逐字保留。
- 超时：probe 15s；下载步骤 300s；装机/卸载命令 60s；同步 RPC（status/logs）20s
  —— 同步调用必须留在宿主 30s RPC 预算内（超时会杀插件进程）。

## 凭据策略

- SSH 凭据**不落盘、不进日志/事件**：`ssh` 前缀原样用于本机 ssh 调用，
  日志与步骤 detail 一律经 `maskSSH` 脱敏为 `<prog> <user@host> <redacted>`
  （私钥路径、端口、`-o` 参数全部打码）。私钥沿用 server 主机 `~/.ssh` 与 ssh-agent。
- `connect_cmd`（含 runner token）不在步骤 detail 中回显；它只会写进远端 unit 文件 /
  nohup 命令行 —— 这是 runner 自身的连接方式所必需。

## 已知限制 / 设计取舍

- `ssh` 前缀须以 ssh 客户端开头（`ssh`、绝对路径均可）；`sshpass`/`sudo` 之类的包装命令
  不受支持（默认选项注入在程序名之后，会破坏包装命令的参数解析）。
- `strings.Fields` 拆词意味着前缀里**不能带引号参数**（如含空格的 `-o "ProxyCommand=…"`）。
- 远端平台仅支持 `linux`/`darwin` × `amd64`/`arm64`（与 release 产物矩阵一致），其余明确报错。
- `deprovision({uninstall:true})` 通过常见路径（`~/.local/bin`、`/usr/local/bin`、PATH）
  发现二进制；非默认安装目录的二进制可能删不掉（会在步骤 detail 里如实说明）。
- job 表在内存中（上限 128 条，先清理已完成项）；插件进程重启后 `job_status` 会返回
  `unknown job`，但远端状态不受影响（所有方法都是无状态的，每次调用自带 `ssh`/`name`）。
