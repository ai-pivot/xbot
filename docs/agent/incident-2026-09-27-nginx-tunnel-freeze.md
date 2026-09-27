# 事故报告：xbot.pivotlang.tech 全站挂死（2026-09-27）

## 结论（TL;DR）

- **现象**：`https://xbot.pivotlang.tech` 及其下所有请求"全卡死"（浏览器转圈/超时），但**后端本身健康**。
- **根因**（两层叠加，均在 43.154.191.136 的 nginx 侧，不是网络）：
  1. `xbot.pivotlang.tech` 的 vhost 里 **所有 location 都继承 `proxy_read_timeout 86400s` / `proxy_send_timeout 86400s`（24 小时）** —— 普通 API/静态请求也享受"WS 级"超时；
  2. `worker_connections 768`（2 vCPU / `worker_processes auto`）→ 连接池很小。
  ⇒ 隧道侧一旦有几条长流不干净关闭（nginx error.log：`upstream prematurely closed connection` ×26、`recv() failed (104: Connection reset by peer)` ×24），这些连接被攥住最长 24h，**371 个连接僵在 CLOSE_WAIT**，4 个 worker 合计 **181% CPU**（2 核打满）⇒ TCP 能建连但**永远等不到 TLS/HTTP 响应** ⇒ 整站卡死。
- **反证/排除**：后端 `http://127.0.0.1:6000/`（frps 远端口）从 43 本机 **200 / 0.05s** 正常；本机我的前端 `127.0.0.1:16000` 一切正常；DNS 正常。故**不是网络、不是后端、不是隧道断**，纯粹是 nginx 连接/超时配置问题。

## 修复（已上线并验证）

### 43.154.191.136（nginx）
| 改动 | 改前 | 改后 |
|---|---|---|
| 超时分级 | **所有** location 86400s | 默认 **60s**；`/api/` 300s；仅 `/ws`、`/ws/terminal`、`/api/sse`、`/bestlaser` 保留 86400s（`/runners/ws/` 3600s） |
| `worker_connections` | 768 | **8192** |
| `worker_rlimit_nofile` | 未设 | 65535 |
| `reset_timedout_connection` | 未设 | **on** |
| `proxy_next_upstream` | 默认 | **off**（单上游不重试放大） |
| 可观测 | 无 | `stub_status` on `127.0.0.1:8090` |
| 自愈 | 无 | `nginx-watch.timer`（每 60s：本机 https 自测；连续 3 次失败 ⇒ 自动 `systemctl restart nginx`，日志 `/var/log/nginx-watch.log`） |

备份：`/root/nginx-backup-20260927-220912/`、`/root/nginx-backup-20260927-221159/`

### 本机（frpc / 隧道 / 前端）
| 改动 | 说明 |
|---|---|
| `frpc.toml` 硬化 | `transport.heartbeatInterval=30`、`heartbeatTimeout=90`、`poolCount=5`、`dialServerTimeout=10`、`tcpMux=true`、`log.to=/var/log/frpc.log`（原配置**无任何传输层设置、无日志**） |
| 隧道看门狗 | `/home/smith/bin/xbot-tunnel-watch.sh`（supervisord 托管 `xbot-tunnel-watchdog`）：每 60s 双检 `127.0.0.1:16000` + `43.154.191.136:6000`；连续 3 次失败 ⇒ 自动 **start/restart frpc**；日志 `/var/log/xbot-tunnel-watch.log` |
| 备份 | `/etc/frp/frpc.toml.bak-20260927-*` |

## 验证证据

| 项 | 修复前 | 修复后 |
|---|---|---|
| nginx 自测（43 本机，Host=xbot.pivotlang.tech） | 000 / 6s（超时） | **200 / 0.06–0.17s** |
| CLOSE_WAIT(:443) | **371** | **0** |
| nginx worker CPU | 4×~45% = **181%**（2 核饱和） | ~0%（90% idle） |
| 用户视角 `https://xbot.pivotlang.tech/` | 000 / 10s | **200 / 0.49s** |
| 隧道自愈实测 | — | 停 frpc → 看门狗 2 次检测后自动拉活 → `e2e=200` ✅ |
| nginx 自愈判据实测 | — | DRY_RUN 指向必拒端口 → 记录 `FAIL 1/1 web=000 upstream6000=200` + `would restart nginx` ✅ |

## 残留事项

1. **`/runners/ws/` → `127.0.0.1:8089` 是死上游**：本机没有任何进程监听 8089（sandbox WS 未启用），该路由必然 502（此前 error.log 里的 `no live upstreams` 来源）。要么在本机启用 sandbox WS，要么在 frpc 配置里加一条 `8089 → 远端端口` 的映射并把 nginx 指向它，要么删掉这条路由。
2. `xbot-server`（supervisord 托管，`xbot-cli serve`）在本次操作期间随 `supervisorctl update` 重启过一次（uptime 归零），排障时注意区分。
3. 到 frps 7077 的 TCP 建连 5 次里出现 1 次 **1063ms**（其余 48–54ms）→ 链路有轻微抖动，已由 heartbeat 覆盖。

## 关键教训（写进运维规范）

1. **超时必须按 location 分级**：长连接（WS/SSE）单独给长超时，**绝不能把 86400s 设为该 vhost 的默认值**——那等于把所有请求都变成"可无限挂住"。
2. **`worker_connections` 要按负载给足**（768 对多站点/多长连接的机器太小），并配 `worker_rlimit_nofile`。
3. **两侧都要有自愈看门狗**（nginx 侧 + 隧道侧），且**必须留日志**——本次事故最初"无声无息"，全靠人工发现。
4. 本机 curl 排查必须带 `--noproxy '*'`，否则环境代理会让**所有端口都返回 502**（本次排查中被此假信号误导过一次）。
