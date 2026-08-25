---
title: "插件架构"
weight: 2
---

了解 xbot 插件系统的工作原理。

## 概览

xbot 的插件系统遵循类似 VSCode 的扩展模型。插件通过 `plugin.json` 清单被发现，基于事件延迟激活，通过 `PluginContext` 接口进行沙箱化。

```
┌─────────────────────────────────────────────────────────┐
│                     xbot Agent                           │
│  ┌─────────────┐  ┌─────────────┐  ┌─────────────┐     │
│  │  Tool Registry│  │ Hook Manager│  │WidgetRegistry│    │
│  └──────┬───────┘  └──────┬──────┘  └──────┬──────┘    │
│         │                  │                │            │
│  ┌──────┴──────────────────┴────────────────┴──────┐   │
│  │              PluginManager                        │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐          │   │
│  │  │ Native   │ │ Stdio    │ │ Script   │          │   │
│  │  │ Runtime  │ │ Runtime  │ │ Runtime  │          │   │
│  │  └────┬─────┘ └────┬─────┘ └────┬─────┘          │   │
│  └───────┼────────────┼────────────┼────────────────┘   │
│          │            │            │                      │
│  ┌───────┴────────────┴────────────┴────────────────┐   │
│  │              PluginContext                        │   │
│  │  (权限过滤的 API 表面)                             │   │
│  └───────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

## 核心组件

### PluginManager

中央编排器（`plugin/manager.go`）。职责：

- **发现**：扫描 `~/.xbot/plugins/` 和 `~/.xbot/plugins/builtin/` 中的 `plugin.json`
- **激活**：基于激活事件创建运行时实例并调用 `Activate()`
- **生命周期**：管理插件状态（Discovered → Active → Inactive → Error）
- **依赖解析**：拓扑排序与循环检测（Kahn 算法）
- **热重载**：`WatchConfig` 每 30 秒轮询 `config.json` 的插件启用/禁用变更
- **自动重试**：指数退避（1s → 30s 上限）恢复失败插件

### Plugin 接口

每个插件实现三个方法（`plugin/plugin.go`）：

```go
type Plugin interface {
    Manifest() PluginManifest
    Activate(ctx PluginContext) error
    Deactivate(ctx PluginContext) error
}
```

### 运行时工厂

三种运行时类型（`plugin/runtime_factory.go`）：

| 运行时 | 描述 | 适用场景 |
|--------|------|----------|
| `native` | 进程内 Go 插件 | 最高性能，直接 API 访问 |
| `stdio` | 通过 stdin/stdout JSON-RPC 的外部进程 | 任意语言，隔离 |
| `script` | 外部脚本执行 | 简单小部件，bash/Python 脚本 |

`grpc` 是 `stdio` 的历史别名。WASM 运行时仅骨架（规划中）。

### PluginContext

插件与 xbot 交互的**唯一**接口（`plugin/context.go`）。组合了多个子接口：

- `ToolRegistrar` — 注册工具和中间件
- `HookSubscriber` — 订阅生命周期钩子
- `StorageProvider` — 每插件 KV 存储
- `SessionMetadata` — 只读会话信息
- `EventBusPublisher` — 插件间事件
- `UIContributor` — 注册小部件、主题、覆盖层
- `CronScheduler` — 调度定时任务

访问受声明权限过滤 — 插件只能使用其声明的能力。

## 插件状态

```
Discovered → Activating → Active → Deactivating → Inactive
                ↓                          ↑
              Error ←──────────────────────┘
```

| 状态 | 描述 |
|------|------|
| `StateDiscovered` | 清单已加载，尚未激活 |
| `StateActivating` | `Activate()` 进行中 |
| `StateActive` | 插件运行中，正在贡献能力 |
| `StateInactive` | 被用户或配置禁用 |
| `StateError` | 激活失败或运行时错误 |

## 发现流程

1. `PluginManager.Discover()` 扫描 `DefaultPluginDirs()`：
   - `~/.xbot/plugins/` — 用户安装的插件
   - `~/.xbot/plugins/builtin/` — 内置插件包
2. 每个子目录扫描 `plugin.json`
3. 验证清单（ID 格式、版本、运行时类型）
4. 通过 `RuntimeFactory.Create()` 创建运行时实例
5. 解析依赖激活顺序（拓扑排序）

## 文件布局

```
~/.xbot/
├── plugins/
│   ├── my-plugin/
│   │   ├── plugin.json        # 清单
│   │   ├── main.sh            # 入口点
│   │   ├── data/
│   │   │   └── storage.json   # KV 存储
│   │   ├── config.json        # 用户覆盖
│   │   └── logs/               # 每插件日志
│   └── builtin/                # 内置插件
├── config.json                 # 全局配置
```

## 另请参阅

- [插件清单](./manifest/) — 完整清单规范
- [PluginContext API](./plugin-context/) — 插件访问 xbot 的网关
- [权限系统](./permissions/) — 基于能力的安全模型
- [Stdio 运行时](./stdio-runtime/) — 任意语言的 JSON-RPC 协议
