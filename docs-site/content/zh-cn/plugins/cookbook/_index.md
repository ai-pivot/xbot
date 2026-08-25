---
title: "插件 Cookbook"
weight: 1
geekdocCollapseSection: true
---

手把手的 xbot 插件开发食谱。每一篇都以一个可运行的真实示例开篇——示例全部来自本仓库的真实插件代码（`plugin/examples/`、`plugins/xbot-genui`、`plugins/xbot-git-fancy`），随后解释背后的 API。

## 你将学到什么

| 食谱 | 你将构建 |
|---|---|
| [快速上手：Script 插件](./quick-start-script/) | 一个 bash 编写的 git 状态小组件——零编译 |
| [快速上手：Go 插件](./quick-start-go/) | 带工具、Hook、上下文注入器的原生 Go 插件 |
| [快速上手：Stdio 插件](./quick-start-stdio/) | 一个走 stdio NDJSON 协议的 Python 插件 |
| [快速上手：Web 插件](./quick-start-web/) | 一个前端 ESM 视图面板 |
| [Script 插件](./script-plugins/) | 完整指南：组件、环境变量、触发器、同步提示 |
| [Go 插件](./go-plugins/) | 完整指南：`PluginContext`、SDK 助手、Hook→UI 桥接 |
| [Stdio 插件](./stdio-plugins/) | 完整指南：任意语言实现协议处理器 |
| [Channel 插件](./channel-plugins/) | 完整渠道适配器：工具、提示词、Web UI |
| [Web 插件](./web-plugins/) | 类型即契约的前端插件（`@xbot/plugin-api`） |
| [配置系统](./configuration/) | 声明式插件设置与默认值 |
| [Widget 组件](./widgets/) | 状态栏、信息栏、页脚组件 |
| [Hook 钩子](./hooks/) | 生命周期事件：deny / ask / defer / allow |
| [工具注册](./tools/) | 注册可被 agent 调用的工具 |
| [事件总线](./event-bus/) | 插件间 pub/sub 通信 |
| [存储系统](./storage/) | 插件级持久化键值状态 |
| [权限系统](./permissions/) | 完整权限目录 |
| [依赖管理](./dependencies/) | 插件依赖图与激活顺序 |
| [调试与日志](./debugging/) | 日志、性能剖析、热重载 |
| [测试插件](./testing/) | `TestKit`、Mock 与金丝测试 |
| [发布与分发](./publishing/) | 分发、校验和、数据迁移 |

## 五分钟路径

刚接触 xbot 插件？按顺序读完四篇快速上手。它们覆盖了 `plugin.NewCompositeRuntimeFactory()`（`plugin/runtime_factory.go`）支持的四种运行时：

```go
switch manifest.Runtime {
case RuntimeNative:  // 进程内 Go 插件
case RuntimeGRPC, RuntimeStdio: // 外部 NDJSON 进程
case RuntimeScript: // 周期性外部脚本
}
```

每种运行时对应不同的取舍：**script** 用于零构建的组件，**native Go** 用于紧密集成，**stdio** 支持任意语言，**web** 用于纯前端扩展。一个 `plugin.json` 可以同时组合它们——参考内置的 `xbot.git-fancy` 插件：Go stdio 后端 + React 视图。

## 插件目录结构

```
~/.xbot/plugins/<plugin-id>/
├── plugin.json      # 清单 —— id、runtime、entry、permissions、contributes
├── <入口文件>        # main.sh / main.go / main.py / web/index.js
└── data/            # 运行时存储（自动创建）
```
