---
title: "插件系统"
weight: 25
geekdocCollapseSection: true
---

xbot 的插件系统提供类似 VSCode 的扩展能力，通过统一架构支持多种运行时（原生 Go、stdio/gRPC、脚本、以及未来的 WASM）。插件可以贡献工具、钩子、小部件、上下文增强器、命令、主题、渠道提供者和 Web UI 扩展。

## 核心特性

- **多运行时**：原生 Go 插件、stdio/gRPC 插件（任意语言）、脚本插件（bash）、WASM（规划中）
- **声明式清单**：每个插件通过 `plugin.json` 声明其能力
- **权限系统**：细粒度能力控制 — 插件只能访问其声明的能力
- **Hook 系统**：工具前后、会话生命周期、用户输入等事件钩子
- **Widget 系统**：CLI 状态栏、标题栏、信息栏和页脚小部件
- **Web 插件 v2**：类型安全的 ESM 前端插件，声明式 UI 贡献
- **Channel 插件**：通过 stdio 协议的完整渠道适配器（如 GenUI display_html）
- **事件总线**：插件间发布/订阅通信
- **KV 存储**：每插件持久化键值存储
- **热重载**：配置驱动的插件激活/停用
- **自动重试**：失败插件的指数退避恢复
- **依赖解析**：拓扑排序与循环检测

## 文档章节

- [快速开始](./getting-started/) — 5 分钟创建第一个插件
- [架构概览](./architecture/) — 插件系统工作原理
- [插件清单](./manifest/) — 完整 `plugin.json` 规范
- [插件生命周期](./lifecycle/) — 激活、停用和激活事件
- [PluginContext API](./plugin-context/) — 插件访问 xbot 的网关
- [权限系统](./permissions/) — 基于能力的安全模型
- [存储系统](./storage/) — 每插件 KV 存储
- [事件总线](./event-bus/) — 插件间通信
- [Hook 系统](./hooks/) — 生命周期钩子和拦截器
- [Widget 系统](./widgets/) — CLI 和 Web UI 小部件
- [插件工具](./tools/) — 为 LLM 注册自定义工具
- [Script 运行时](./script-runtime/) — Bash 脚本插件
- [Stdio 运行时协议](./stdio-protocol/) — 任意语言的 JSON-RPC 协议
- [Channel 插件](./channel-plugins/) — 完整渠道适配器
- [配置系统](./configuration/) — 用户可配置的插件设置
- [依赖管理](./dependencies/) — 插件依赖解析
- [迁移系统](./migration/) — 插件数据迁移
- [日志与审计](./logging/) — 每插件日志与审计追踪
- [热重载与监控](./hot-reload/) — 重载、配置监控、自动重试、健康检查
- [Web 插件系统](./web/) — 前端 ESM 插件运行时
- [开发指南](./cookbook/) — 逐步开发指南
- [API 参考](./api-reference/) — 完整 API 参考
- [内置插件](./builtin/) — xbot 内置插件

## 快速示例

最小化的脚本插件（`plugin.json`）：

```json
{
  "id": "my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "runtime": "script",
  "entry": "bash main.sh",
  "activationEvents": ["onStart"],
  "permissions": ["ui.contribute"],
  "contributes": {
    "ui": [
      {
        "id": "greeting",
        "slot": "statusBarRight",
        "description": "Show a greeting"
      }
    ]
  }
}
```

脚本 `main.sh`：

```bash
#!/bin/bash
echo "Hello from my plugin!"
```

将两个文件放入 `~/.xbot/plugins/my-plugin/` 并重启 xbot。问候语将出现在状态栏。
