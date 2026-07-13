---
type: Design Spec
title: Web API REST + SSE 改造 — 插件市场删除
description: 删除插件市场 REST 端点和前端实现
tags:
  - spec
  - cleanup
  - plugin-market
status: draft
repos:
  xbot: 3def45b807e4ed93c9df5b33479373f5e75a8c81
---

# 插件市场删除

> 主设计: [Web API REST + SSE 改造主设计](./07-13-Web-API-REST-SSE-改造主设计.md)

## 目标

- 删除插件市场相关 REST 端点
- 删除前端插件市场页面和组件
- 不影响插件系统本身（插件加载、hook、widget 等不受影响）

## 范围

### 包含

- 删除 6 个插件市场 REST 端点
- 删除前端插件市场 UI（页面、路由、组件）
- 清理相关的 WebCallbacks 回调（如 `MarketBrowse`、`MarketInstall` 等仅服务市场浏览的回调）

### 不包含

- 插件系统本身（`plugin/` 目录、插件加载、hook、widget）
- `plugin_status`、`plugin_widgets`、`plugin_reload` 等 RPC（这些是插件管理，不是市场浏览）
- SSE 的 `plugin_widgets` 推送（这是 widget 渲染，不是市场）

## 删除的端点

| 端点 | 方法 | 原用途 |
|------|------|--------|
| `/api/market` | GET | Agent/Skill 市场浏览 |
| `/api/market/install` | POST | 安装市场项 |
| `/api/market/uninstall` | POST | 卸载市场项 |
| `/api/market/my` | GET | 已安装列表 |
| `/api/market/publish` | POST | 发布到市场 |
| `/api/market/unpublish` | POST | 从市场下架 |

## 后端清理

### 路由注册

从 `web.go` 的 `Start()` 方法中删除以下路由注册：

```
mux.HandleFunc("/api/market", ...)
mux.HandleFunc("/api/market/install", ...)
mux.HandleFunc("/api/market/uninstall", ...)
mux.HandleFunc("/api/market/my", ...)
mux.HandleFunc("/api/market/publish", ...)
mux.HandleFunc("/api/market/unpublish", ...)
```

### Handler 函数

从 `web_api.go` 中删除以下 handler：

- `handleMarket`
- `handleMarketInstall`
- `handleMarketUninstall`
- `handleMarketMy`
- `handleMarketPublish`
- `handleMarketUnpublish`

### WebCallbacks

检查 `WebCallbacks` 结构体中仅服务市场浏览的回调字段，如果不再有其他使用者则删除。

### RPC handler

检查 `rpc_table.go` 中是否有仅服务市场的 RPC handler（如 `plugin_install`、`plugin_uninstall`）。这些 RPC 如果 CLI 也使用则保留；如果仅 Web 市场使用则删除。

## 前端清理

### 页面和路由

- 删除插件市场页面组件
- 删除前端路由中的市场入口
- 删除导航中的市场链接

### 组件

- 删除市场浏览组件
- 删除安装/卸载交互组件
- 删除发布/下架表单组件

### API 调用

- 删除前端中所有调用 `/api/market*` 的代码

## 验收标准

1. 6 个市场端点返回 404
2. 前端不再有市场入口
3. 插件系统正常工作（加载、hook、widget）
4. `plugin_widgets` SSE 推送不受影响
5. 编译通过，无残留引用
