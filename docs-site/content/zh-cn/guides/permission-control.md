---
title: "Permission Control"
weight: 50
---

# Permission Control

OS user-based permission control for tool execution. Restricts which OS users the agent can execute commands as, with optional approval workflows for privileged operations.

## Overview

When permission control is enabled, the agent can execute tools as different OS users via the `run_as` parameter. Sensitive operations (e.g., running as root) require user approval before execution.

## Setup

### 1. Configure Users

Set the permission users via per-user settings:

```
/settings set default_user user
/settings set privileged_user root
```

| Setting | Description |
|---------|-------------|
| `default_user` | Non-privileged user. Tool execution proceeds without approval. |
| `privileged_user` | Privileged user (e.g., `root`). Requires user approval before execution. |

### 2. Configure Sudoers

Run the setup script to configure NOPASSWD sudo entries:

```bash
sudo bash scripts/setup-perm-control.sh --default-user user --privileged-user root
```

### 3. Enable

Permission control activates automatically when `default_user` or `privileged_user` is set. When enabled:

- All raw `sudo` commands are blocked (use `run_as` instead)
- `run_as` and `reason` must be provided together
- Executing as `privileged_user` triggers an approval workflow

## Behavior

### sudo Blocking

When permission control is enabled, any raw `sudo` in Shell commands is denied:

```
error: sudo is not allowed when permission control is enabled (use run_as instead)
```

### Pair Validation

`run_as` and `reason` must always be provided together:

```
error: run_as and reason must be provided together
```

### Approval Workflow

When the agent wants to execute as the `privileged_user`:

1. An approval request is sent to the user (CLI panel or Feishu card)
2. User approves or denies
3. If approved, the command executes as the specified user
4. If denied, the tool returns an error with the optional deny reason

### Timeout

If the user doesn't respond within the LLM context timeout, the approval card closes automatically and the tool returns a timeout error.

## Affected Tools

| Tool | Additional Parameters |
|------|----------------------|
| `Shell` | `run_as`, `reason` |
| `FileCreate` | `run_as`, `reason` |
| `FileReplace` | `run_as`, `reason` |

## Channel-specific Approval

### CLI

TUI 审批面板提供：
- 批准按钮
- 拒绝按钮（打开文本输入框，可填写可选的拒绝理由）
- 拒绝理由会传递到工具的错误信息中

### Feishu

基于交互式卡片的审批：
- 初始卡片上有批准按钮
- 拒绝按钮打开第二张卡片，包含可选的拒绝理由表单
- 卡片超时自动关闭，显示 "Timed Out" 状态

## Hooks 集成

权限请求会流经 Hooks 系统。`PermissionRequest` 和 `PermissionDenied` 生命周期事件允许自定义 Hook 处理器来：

- 在权限请求到达用户之前拦截它们
- 自动批准或拒绝特定模式
- 记录所有权限请求以备审计
- 与外部审批系统集成（如 PagerDuty、Slack）

在 `~/.xbot/hooks.json` 中配置 Hooks：

```json
{
  "enable_command_hooks": true,
  "hooks": {
    "PermissionRequest": [
      {
        "matcher": "",
        "hooks": [{
          "type": "command",
          "command": ".xbot/hooks/perm-request.sh"
        }]
      }
    ],
    "PermissionDenied": [
      {
        "matcher": "",
        "hooks": [{
          "type": "http",
          "url": "https://your-audit-service.example.com/hooks/perm-denied"
        }]
      }
    ]
  }
}
```

完整 Hook 配置说明参见 [Hooks](/zh-cn/features/hooks/) 页面。

## 参见
- [Hooks](/zh-cn/features/hooks/) — 生命周期事件
- [沙箱指南](/zh-cn/guides/sandbox/) — Docker 沙箱
- [配置参考](/zh-cn/configuration/) — 权限设置
