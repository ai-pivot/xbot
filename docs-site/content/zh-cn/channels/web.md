---
title: "Web"
weight: 35
---

# Web 渠道

浏览器聊天界面。支持用户注册/登录、邀请制和 Persona 隔离。

**需要 Server 模式。**

## 配置

```json
{
  "web": {
    "enable": true,
    "port": 8082
  },
  "admin": {
    "token": "your-secret-token"
  }
}
```

| 字段 | 必填 | 默认值 | 说明 |
|------|------|--------|------|
| `enable` | ✅ | `false` | 启用 Web 渠道 |
| `host` | ❌ | `""` | 监听地址（空 = 所有接口） |
| `port` | ❌ | `0` | 监听端口 |
| `static_dir` | ❌ | 自动检测 | 前端静态文件目录 |
| `upload_dir` | ❌ | `""` | 上传文件自定义目录 |
| `persona_isolation` | ❌ | `false` | 启用后每个 Web 用户的 persona 互相隔离 |
| `invite_only` | ❌ | `false` | 启用后禁止自主注册，只能由管理员创建账号 |

## Web UI 安装

Server 模式安装时，安装器会自动下载 Web UI 到 `~/.xbot/web/dist/`。

如果需要手动安装：

```bash
# 下载对应版本的 web 压缩包并解压到 ~/.xbot/web/dist/
```

## 访问

启动 Server 后，在浏览器中打开 `http://your-server:8082`。

## 消息输入框

富文本（WYSIWYG）编辑器，支持 Markdown 快捷语法（`**粗体**`、`-` 列表等）与链接：

| 功能 | 说明 |
|------|------|
| 链接 | 输入 `https://…` 后跟空格自动成链；粘贴链接/Markdown 链接语法（`[文字](URL)`）实时渲染；链接以主题色高亮显示 |
| 链接编辑 | 选中文本浮现工具栏（加粗/斜体/删除线/行内代码/链接）；`Ctrl/Cmd+K` 快速添加或修改链接，支持一键解除 |
| 文件上传 | **支持任意类型文件**（无类型白名单）。点击 📎 选择文件、直接**粘贴**（截图自动上传）或**拖拽**到输入框，文件作为附件随消息发送 |
| 上传限制 | 单文件 10MB（仅限大小，不限类型） |

## 认证方式

| 方式 | 说明 |
|------|------|
| 用户名/密码 | 注册后登录，Session Cookie 有效期 30 天 |
| CLI Token | WebSocket 连接时使用 admin token |
| 飞书登录 | 通过飞书账号一键登录/关联 |

## Invite-Only 模式

当 `invite_only: true` 时：

- 新用户无法自主注册（返回 403）
- 管理员可通过飞书渠道的管理命令或直接操作数据库创建账号
- 适合内部团队使用

## Persona Isolation

当 `persona_isolation: true` 时：

- 每个 Web 用户的系统人格（persona）互相隔离
- 用户 A 设置的 Agent 行为不影响用户 B
- 适合多租户场景

## 参见
- [飞书渠道](/zh-cn/channels/feishu/) — 团队协作
- [CLI 渠道](/zh-cn/channels/cli/) — 终端 TUI
- [配置参考](/zh-cn/configuration/) — Web 渠道设置
