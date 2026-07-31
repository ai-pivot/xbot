---
title: "沙箱指南"
weight: 45
---

# 沙箱指南

## 概览

xbot 支持多种沙箱模式，控制 Agent 执行 Shell 命令时的隔离级别。

| 模式 | 说明 | 适合 |
|------|------|------|
| `none` | 无隔离，直接在本机执行 | 个人开发机、Docker 内部 |
| `docker` | 每个用户一个隔离 Docker 容器 | 多用户服务器 |

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
    Mode        string   `json:"mode"`         // 沙箱模式："none" 或 "docker"
    RemoteMode  string   `json:"remote_mode"`  // 远程沙箱模式
    DockerImage string   `json:"docker_image"` // Docker 镜像名
    HostWorkDir string   `json:"host_work_dir"`// 宿主机工作目录，映射到容器内
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
| `docker_image` | `"ubuntu:22.04"` | 容器使用的 Docker 镜像 |
| `host_work_dir` | `""` | 映射到容器内的宿主机目录 |
| `idle_timeout` | `"30m"` | 空闲超时（`0` = 永不自动销毁） |
| `ws_port` | `8080` | 远程沙箱 WebSocket 连接端口 |
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

### docker 模式

每个用户获得一个独立的 Docker 容器，文件系统持久化。

**前置条件：**

```bash
# 安装 Docker
sudo apt-get update && sudo apt-get install -y docker.io
sudo systemctl start docker && sudo systemctl enable docker
sudo usermod -aG docker $USER  # 需要重新登录
```

**配置：**

```json
{
  "sandbox": {
    "mode": "docker",
    "docker_image": "ubuntu:22.04",
    "host_work_dir": "/home/user/projects",
    "idle_timeout": "30m"
  }
}
```

{{< hint type=note >}}
**容器生命周期**：Docker 容器在空闲超时触发时会被**停止**（而非删除），下次会话可以复用。路径转换使用 DinD（Docker-in-Docker）模式。
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
    "mode": "docker",
    "auth_token": "your-secure-token",
    "ws_port": 8080,
    "public_url": "ws://your-server.com:8080"
  }
}
```

**Runner 端**（用户机器上）：

```bash
xbot-runner --server ws://your-server.com:8080 --token your-secure-token --name my-runner
```

**路由规则**（按用户，由 `user_settings.active_runner` 决定）：

| `active_runner` 值 | 使用的沙箱 |
|---------------------|------------|
| `"__docker__"` | DockerSandbox（若已启用） |
| 具体 Runner 名称 | 对应的 RemoteSandbox（若已连接） |
| 回退 | Remote → Docker → None |

{{< hint type=tip >}}
**多 Runner 支持**：多个 Runner 可以同时连接，每个拥有独立的名称和 Token。用户通过设置面板（`/settings`）选择自己的活跃 Runner。这支持多用户场景，每个用户在自己的机器上执行命令。
{{< /hint >}}

### SandboxRouter 架构

`SandboxRouter`（`tools/sandbox_router.go`）是统一的沙箱入口。它根据每个用户的配置将执行请求路由到不同后端：

- 同时实现 `Sandbox` 和 `SandboxResolver` 接口
- 支持双模式：同时持有 Docker 和 Remote 实例
- 按用户独立路由——不同用户可以使用不同后端
- Runner 选择可通过设置面板按用户配置

### 同步配置

使用远程 Runner 时，xbot 可以将 `skills/` 和 `agents/` 目录从服务器同步到 Runner。这确保 Runner 能访问相同的工具和 Agent 定义。

通过 `/settings` → Runner 面板启用同步。

## 参见
- [配置参考](/zh-cn/configuration/) — 沙箱配置字段
- [权限控制](/zh-cn/guides/permission-control/) — 访问控制
- [开发指南](/zh-cn/development/) — 项目结构
