---
title: "沙箱指南"
weight: 45
---

# 沙箱指南

## 概览

xbot 支持多种沙箱模式，控制 Agent 执行 Shell 命令时的隔离级别。

| 模式 | 说明 | 适合 |
|------|------|------|
| `none` | 无隔离，直接在本机执行（**默认**） | 个人开发机 |
| `remote` | 通过 Runner（`xbot-runner`）在你的机器上执行 | 需要隔离 / 多机 |

## 配置

在 `~/.xbot/config.json` 中设置：

```json
{
  "sandbox": {
    "mode": "none"
  }
}
```

### SandboxConfig 参考

沙箱配置结构体（`config/config.go`）：

```go
type SandboxConfig struct {
    Mode        string   `json:"mode"`         // 沙箱模式："none"（默认）或 "remote"
    RemoteMode  string   `json:"remote_mode"`  // 远程沙箱模式（非空 ⇒ 启用 Runner 接入）
    IdleTimeout Duration `json:"idle_timeout"` // 空闲超时，超时后自动销毁
    WSPort      int      `json:"ws_port"`      // 远程沙箱 WebSocket 端口
    AuthToken   string   `json:"auth_token"`   // Runner 认证 Token
    PublicURL   string   `json:"public_url"`   // Runner 连接的公开 URL
}
```

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `mode` | `"docker"` | 沙箱模式：`"none"` 或 `"docker"` |
| `remote_mode` | `""` | 远程沙箱模式 |
| `mode` | `"none"` | 沙箱模式：`"none"` 或 `"remote"` |
| `remote_mode` | `""` | 远程沙箱模式（非空 ⇒ 启用 Runner 接入） |
| `auth_token` | `""` | Runner 共享认证 Token |
| `public_url` | `""` | Runner 用于连接的公开 URL |

### none 模式（推荐个人使用）

命令直接在本机执行。Windows 下使用 PowerShell。

```json
{
  "sandbox": {
    "mode": "none"
  }
}
```

{{< hint type=warning >}}
**无隔离意味着 Agent 可以执行你当前用户有权限执行的所有命令。** 请确保你信任 Agent 的行为。在共享服务器或生产环境中请使用沙箱模式。
{{< /hint >}}

### 远程沙箱（推荐的隔离方式）

{{< hint type=warning >}}
**本地 Docker 沙箱已于 2026-09-16 整体移除**（沙箱统一走 Runner 接入）。需要隔离时请用下一节的远程 Runner —— 命令在**你自己的机器**上执行，服务器不再管理容器。默认 `mode` 为 `"none"`（本机直连）。
{{< /hint >}}

### 远程沙箱

用户可以连接自己的远程 Runner，在自己的机器上执行命令。通过 CLI 的 `/settings` 面板配置。

**工作原理：**

1. **Runner**（`xbot-runner`）通过 WebSocket 连接到 xbot 服务器
2. 使用共享 Token（`auth_token`）进行认证，验证采用 `subtle.ConstantTimeCompare`
3. 服务器将工具执行请求路由到 Runner 所在的机器
4. 支持 stdio 流式输出和 Runner 本地的 LLM 模型

**服务器端配置：**

```json
{
  "sandbox": {
    "remote_mode": "remote",
    "auth_token": "your-secure-token",
    "ws_port": 8080,
    "public_url": "ws://your-server.com:8080"
  }
}
```

**Runner 端**（被纳管的机器上）：

```bash
xbot-runner --server ws://your-server.com:8080/ws --token your-secure-token --name my-runner
```

`--name` 是这台机器的标识；归属由一次性连接 token 在服务端解析，因此 runner 自报名字与注册表里的名字始终一致。

**路由规则**（按 **会话**，全局单 operator）：

| 会话状态 | 使用的沙箱 |
|---------------------|------------|
| 绑定到 runner R 且 R 在线 | R 的 RemoteSandbox |
| 绑定到 runner R 但 R **离线** | **硬失败** —— 工具拒绝执行（绝不静默回退本机） |
| 未绑定 | None（本机直连） |

绑定方式：`config` 工具（`runner` action，`sub=switch name=...`；`sub=unbind` 切回本机），或 RPC `runner_session_set` / `runner_session_get`。

{{< hint type=tip >}}
**多机器、单 operator**：任意数量的 runner 可同时连接，各带独立名称与 token。绑定是**会话级**的，因此两个会话可以同时跑在两台不同机器上。所有维度都不带用户 —— 见 `docs/design/runner-ssh-provisioning.md`。

**自动纳管与连接模型（VS Code Remote 式）**：内置插件 `xbot.ssh-runner`（`plugins/xbot-ssh-runner/`）
接受一条 SSH 命令，探测目标机器、安装 `xbot-runner`，然后**连接** —— 连接方式是开一条 SSH 会话，
runner 在该会话的**前台**运行（管道断 = runner 死）；每次（重）连都会**先杀掉老 runner 再起新的**，
因此同一个 name 永远不会并存两个 runner。

默认走**隧道**（`ssh -R`）：runner 只连目标机器上的 `127.0.0.1:<port>`，该端口被转发回你的 server，
所以**目标机器完全不需要能访问 server** —— 只需要你能 SSH 到它（两端都在 NAT 后也可以）。
若机器本身能访问 server，可显式设 `connectionMode: direct`。
{{< /hint >}}

### SandboxRouter 架构

`SandboxRouter`（`tools/sandbox_router.go`）是统一的沙箱入口。它**按会话**把执行请求路由到对应后端：

- 同时实现 `Sandbox` 和 `SandboxResolver` 接口
- 仅两种后端：Remote（Runner）与 None（本地直连；默认）
- `SandboxForSession("channel:chatID")` → 未绑定=本机、已绑定且在线=Remote、**已绑定但离线=硬失败**
- 绑定是**会话级**的（`tenants.runner_id`），不是用户级；无 per-user 维度

### 同步配置

使用远程 Runner 时，xbot 可以将 `skills/` 和 `agents/` 目录从服务器同步到 Runner。这确保 Runner 能访问相同的工具和 Agent 定义。

通过 `/settings` → Runner 面板启用同步。

## 参见
- [配置参考](/zh-cn/configuration/) — 沙箱配置字段
- [权限控制](/zh-cn/guides/permission-control/) — 访问控制
- [开发指南](/zh-cn/development/) — 项目结构
