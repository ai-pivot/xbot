---
title: "渠道概览"
weight: 20
---

# 渠道概览

xbot 通过**渠道（Channel）**接收和发送消息。每个渠道是一个可插拔的适配器，连接到同一个 Agent 引擎。

## 渠道对比

| 渠道 | 适合谁 | 连接方式 | 特点 |
|------|--------|----------|------|
| [CLI](/zh-cn/channels/cli/) | 开发者、终端用户 | 本地进程 / WebSocket | 全功能 TUI，流式输出，工具调用，SubAgent |
| [飞书](/zh-cn/channels/feishu/) | 团队协作 | WebSocket | 群内 @机器人 对话，消息卡片交互，飞书 API 集成 |
| [QQ](/zh-cn/channels/qq/) | QQ 用户 | WebSocket | QQ 官方机器人协议 |
| [NapCat](/zh-cn/channels/napcat/) | 个人/小圈子 | WebSocket | OneBot 11 协议，兼容 NapCat 等 |
| [Web](/zh-cn/channels/web/) | 任何有浏览器的人 | HTTP + WebSocket | 网页聊天，注册/登录，邀请制 |

## 启用方式

所有渠道通过 `~/.xbot/config.json` 启用，无需设置环境变量。

**在 TUI 中可视化配置**：输入 `/channel` 命令打开频道配置面板，可以图形化管理各频道的启用/禁用和参数，变更会直接写入 `config.json`。这在 Remote 模式下特别有用，因为无需 SSH 登录到 Server 手动编辑配置文件。

**在 Web 中可视化配置**：Web 端「设置 → 渠道」列出全部渠道 —— 内置渠道（web/feishu/qq/napcat）与**插件自行注册的渠道**统一管理（启用开关 + 字段编辑，保存后按需热启停）。飞书卡片上还有一个**一键绑定飞书智能体应用**按钮：点开后把飞书官方授权链接贴出来，在飞书里确认即可给当前应用叠加智能体权限、事件订阅与卡片回调（含「创建与更新卡片」，流式进度卡片需要它），凭据自动写回 `config.json`。

> ⚠️ **飞书、QQ、NapCat、Web 渠道需要 Server 模式。** Standalone 模式仅支持 CLI。

## 选择建议

- **个人使用、快速体验** → CLI（Standalone 模式）
- **团队在飞书里用 AI** → Server 模式 + 飞书渠道
- **给朋友/QQ 群用** → Server 模式 + NapCat 渠道
- **提供公开的网页聊天入口** → Server 模式 + Web 渠道
- **多渠道同时使用** → Server 模式 + 启用多个渠道

## 权限控制

所有渠道都支持 `allow_from` 白名单：

- **留空**（默认）：允许所有人使用
- **设置后**：只有列表中的用户可以与 Agent 对话

飞书用 `open_id`，QQ 用 `openid`，NapCat 用 QQ 号。
