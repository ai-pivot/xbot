# Feishu (Lark) Channel

## 概述

飞书渠道使用 **WebSocket 长连接模式**。xbot 作为客户端主动连接飞书服务器，**不需要公网 IP、域名或回调 URL**。只需要在飞书开放平台创建应用并开启 WebSocket 模式即可。

支持能力：
- 文本/图片/文件消息收发
- 交互式消息卡片（form 表单、按钮、选择器等）
- 文档/知识库读写
- 多维表格读写
- 云盘文件上传下载
- 话题回复（thread）
- 飞书 MCP 工具（20+ 个内置工具）

## 配置

编辑 `~/.xbot/config.json`：

```json
{
  "feishu": {
    "enabled": true,
    "app_id": "cli_xxxxxxxx",
    "app_secret": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx",
    "encrypt_key": "",
    "verification_token": "",
    "domain": "https://open.feishu.cn"
  }
}
```

或通过 TUI `/settings` 面板直接修改，修改后自动生效（无需重启）。

| 字段 | 环境变量 | 必填 | 说明 |
|------|---------|------|------|
| `enabled` | `FEISHU_ENABLED` | 是 | 设为 `true` 启用 |
| `app_id` | `FEISHU_APP_ID` | 是 | 飞书应用 App ID |
| `app_secret` | `FEISHU_APP_SECRET` | 是 | 飞书应用 App Secret |
| `encrypt_key` | `FEISHU_ENCRYPT_KEY` | 否 | 事件加密密钥 |
| `verification_token` | `FEISHU_VERIFICATION_TOKEN` | 否 | 验证 Token |
| `domain` | `FEISHU_DOMAIN` | 否 | 飞书域名（默认 `https://open.feishu.cn`） |

**访问控制**: 环境变量 `FEISHU_ALLOW_FROM` 可设置允许使用的用户 `open_id` 列表（逗号分隔）。留空表示允许所有人。

## 快速配置步骤

### 1. 创建飞书应用

1. 访问 [飞书开放平台](https://open.feishu.cn/app)
2. 点击「创建企业自建应用」
3. 填写应用名称和描述

### 2. 开启机器人能力

1. 在应用详情页 → 左侧菜单「添加应用能力」
2. 添加「机器人」能力

### 3. 开启 WebSocket 长连接

1. 左侧菜单「事件与回调」→「事件配置方式」
2. 选择 **「使用长连接接收事件」**（关键步骤！）
3. 这一步**不需要**配置请求地址 URL，也不需要公网 IP

### 4. 配置权限

在「权限管理」中开通以下权限（按需）：

| 权限 | 用途 |
|------|------|
| `im:message` | 接收消息 |
| `im:message:send_as_bot` | 发送消息 |
| `im:resource` | 读取图片/文件 |
| `docx:document` | 读写文档 |
| `wiki:wiki` | 读写知识库 |
| `bitable:bitable` | 读写多维表格 |
| `drive:drive` | 云盘文件读写 |
| `contact:user.id:readonly` | 获取用户信息 |

### 5. 填写配置并启动

将 `app_id` 和 `app_secret` 填入 `~/.xbot/config.json`，启动 xbot。看到以下日志说明连接成功：

```
Feishu bot starting with WebSocket long connection...
```

## 消息格式

### Markdown 支持

飞书消息支持基础 Markdown 格式，但有以下限制：
- **不支持**复杂表格、嵌套列表、HTML 标签
- 代码块使用 `` ``` `` 包裹
- 链接使用 `[text](url)` 格式
- 建议使用飞书表情符号增强表达

### 交互式卡片

xbot 使用飞书卡片实现交互功能：
- **表单收集** — 通过 card_send 发送表单卡片，用户填写后自动回调
- **按钮操作** — 支持确认对话框、链接跳转
- **数据展示** — 表格、图表（VChart）

### 文件处理

- 用户发送的文件/图片会标记为 `<file .../>` 或 `<image .../>`
- Agent 可通过 `feishu_download_file` 工具下载
- Agent 可通过 `feishu_send_file` 发送文件

## 飞书 MCP 工具

内置 20+ 个飞书 API 工具，Agent 可直接调用：

| 分类 | 工具 |
|------|------|
| **文档** | 创建/读取/编辑文档 |
| **知识库** | 创建/搜索/编辑知识库节点 |
| **多维表格** | 读取/写入/搜索记录 |
| **云盘** | 上传/下载文件 |
| **消息** | 发送消息、更新卡片 |

## 网络架构

```
xbot 进程                    飞书服务器
   │                            │
   │──── WebSocket 连接 ────────▶│  (出站，不需要公网 IP)
   │                            │
   │◀── 事件推送（消息/交互）───│
   │                            │
   │──── API 调用（发消息等）──▶│
   │                            │
```

- xbot 主动建立 WebSocket 长连接
- 飞书通过该连接推送事件
- API 调用通过 HTTPS
- **无需公网 IP、无需域名、无需端口映射、无需反向代理**

## 故障排查

### 连接失败

```
feishu WebSocket failed: ...
```

- 检查 `app_id` 和 `app_secret` 是否正确
- 确认已在飞书开放平台开启「WebSocket 长连接」模式
- 检查网络是否能访问 `https://open.feishu.cn`

### 收不到消息

- 检查是否开通了 `im:message` 权限
- 确认应用已发布（至少发布到测试企业）
- 如果设置了 `FEISHU_ALLOW_FROM`，检查用户的 `open_id` 是否在列表中

### 卡片交互无响应

- 确认已开通 `im:message:send_as_bot` 权限
- 检查 `verification_token` 配置是否与飞书开放平台一致
