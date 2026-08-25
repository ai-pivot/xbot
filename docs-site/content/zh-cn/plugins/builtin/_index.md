---
title: "内置插件"
weight: 1
geekdocCollapseSection: true
---

xbot 在仓库的 `plugins/` 目录下随附两个内置插件：

| 插件 ID | 目录 | 类型 | 用途 |
|---------|------|------|------|
| `xbot.genui` | `plugins/xbot-genui/` | Go stdio **channel 插件** | `display_html` 工具——LLM 生成交互式 UI（图表、3D、动画），在 Web 聊天中流式渲染预览 |
| `xbot.git-fancy` | `plugins/xbot-git-fancy/` | Go stdio 插件 | Fancy Git 面板——分支、工作区变更、分页提交历史、commit 详情、全宽 Monaco diff tab |

两者都是零依赖（或仅依赖 protocol 包）的 Go 二进制，通过 stdio 上的 JSON 协议驱动，易于构建、审计和替换。

## 安装方式

两种受支持的使用方式：

**1. 安装到用户插件目录（生产风格）**

```bash
# 在仓库根目录执行：构建并安装两个插件
make plugins-install
# → ~/.xbot/plugins/xbot.genui/ 和 ~/.xbot/plugins/xbot.git-fancy/
```

重载以激活（或重启 xbot）：

```
tui_control(action=reload_plugins)
```

**2. 直接从仓库 checkout 运行（开发模式）**

```bash
make plugins-build
XBOT_PLUGIN_DIRS="$(pwd)/plugins" xbot
```

`XBOT_PLUGIN_DIRS` 环境变量是路径分隔符分隔的额外插件扫描目录列表。
也可以在 `config.json` 中永久配置仓库路径：

```json
{
  "plugins": {
    "enabled": true,
    "dirs": ["/path/to/xbot/plugins"]
  }
}
```

已安装到用户目录的副本始终优先：插件发现按 ID 去重，且
`~/.xbot/plugins/` 先被扫描，因此已安装副本会遮蔽仓库版本。

## 自动发现机制

1. 启动时，agent 创建 `PluginManager`（当 `plugins.enabled` 为 true）并调用
   `Discover()`（见 `agent/agent.go`）。
2. `Discover()` 扫描 `plugin.DefaultPluginDirs(xbotHome)` 返回的目录——
   `~/.xbot/plugins/`、`~/.xbot/plugins/builtin/`，加上 `XBOT_PLUGIN_DIRS`
   环境变量条目和 `config.json` 的 `plugins.dirs` 条目。
3. 每个包含有效 `plugin.json` 的子目录都是候选插件；重复（manifest `id`
   相同）会被跳过并记录警告。
4. `ActivateAll()` 随后激活所有 `activationEvents` 包含 `onStart` 的插件。

## Channel 激活（GenUI）

GenUI 插件额外声明了一个 **channel provider**（`genui`）。channel 插件实例
只有在 `config.json` 中启用该 channel 时才会被创建：

```json
{
  "channels": {
    "genui": { "enabled": "true" }
  }
}
```

仅安装插件是不够的——没有 `channels.genui` 条目，`IsEnabled` 返回 false，
`display_html` 工具对 LLM 不可见。

## 插件页面

- [xbot-genui](./xbot-genui/) — GenUI（display_html）：交互式 UI 生成
- [xbot-git-fancy](./xbot-git-fancy/) — Fancy Git 面板
