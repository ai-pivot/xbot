---
title: "频道插件"
weight: 14
---

频道插件是完整的频道适配器，为 xbot 扩展新的通信渠道（例如 GenUI display_html）。它们使用 stdio 协议并附加频道专属消息。

## 概述

频道插件是在 activate 响应中声明 `channel_provider` 的 stdio 插件。xbot 创建一个 `ChannelProvider` 桥接，把插件进程连接到 xbot 的频道系统。

## 频道提供者声明

在 activate 响应中返回 `channel_provider`：

```json
{
  "channel_provider": {
    "name": "my-channel",
    "config_schema": [
      {
        "key": "enabled",
        "label": "Enable",
        "description": "Enable this channel",
        "type": "toggle",
        "default_value": "true"
      }
    ]
  }
}
```

### ChannelProviderDecl（Go）

```go
type ChannelProviderDecl struct {
    Name         string           `json:"name"`
    ConfigSchema []map[string]any `json:"config_schema,omitempty"`
    // Entry info populated by xbot from the plugin manifest
    Entry      string   `json:"-"`
    Executable string   `json:"-"`
    Args       []string `json:"-"`
    Dir        string   `json:"-"`
}
```

## 频道插件协议

频道插件在标准 stdio 协议之外使用额外的入站消息：

### `channel_tools`

为特定频道声明工具。以入站消息形式从插件发送：

```json
{
  "method": "channel_tools",
  "params": {
    "tools": [
      {
        "name": "display_html",
        "description": "Display HTML content",
        "input_schema": {
          "type": "object",
          "properties": {
            "code": {"type": "string"}
          }
        }
      }
    ]
  }
}
```

工具通过 `RegisterForChannel("channel-name", tool)` 注册——仅在频道激活时可见。

### `channel_prompt`

声明频道特定的系统提示词片段：

```json
{
  "method": "channel_prompt",
  "params": {
    "system_parts": {
      "05_channel_myplugin": "You have access to display_html tool..."
    }
  }
}
```

键命名约定：`"05_channel_xxx"` 前缀（在 `"00_base"` 之后、`"10_skills"` 之前）。

### `web_ui`

声明频道的 Web UI 组件：

```json
{
  "method": "web_ui",
  "params": {
    "widgets": [...]
  }
}
```

### `channel_inbound`

将频道的用户消息推送到 xbot：

```json
{
  "method": "channel_inbound",
  "params": {
    "message": "user input from channel"
  }
}
```

## ChannelToolBridge

`ChannelToolBridge` 包装频道声明的工具。LLM 调用频道工具时：

1. xbot 将调用路由到 `ChannelToolBridge.Execute`
2. 桥接通过 `Call("execute_tool")` 向插件进程发送 `execute_tool` 请求
3. 插件处理请求并返回结果
4. 桥接用 `Detail`（用于 UI 渲染）和 `ui_code`（用于 web）包装结果

### 工具执行流程

```
LLM calls tool → ChannelToolBridge.Execute
  → Call("execute_tool", {toolName, input})
  → Plugin processes and returns result
  → Bridge wraps result with Detail/ui_code
  → Result returned to LLM
```

## 频道配置

频道插件在 `channel_provider` 响应中声明配置 schema。用户在 `config.json` 中配置频道：

```json
{
  "channels": {
    "my-channel": {
      "enabled": "true"
    }
  }
}
```

**重要**：频道提供者的 `IsEnabled` 检查要求 `config.json` 中存在 `channels.<name>.enabled=true`。缺少该配置，频道永远不会被创建。

## ChannelProviderFactory

`ChannelProviderFactory` 由 `serverapp` 在初始化时注册，用于创建频道提供者实例，避免 `plugin → channel` 的导入循环：

```go
type ChannelProviderFactory func(decl *ChannelProviderDecl, process *StdioPluginProcess) (any, error)
```

## WireChannelProviders

`ActivateAll()` 之后，`WireChannelProviders(pm)` 将所有激活的频道提供者连接到外部注册表：

```go
func WireChannelProviders(pm *PluginManager) {
    // 遍历激活的插件
    // 对每个插件，从上下文获取 ChannelProviders
    // 通过 globalChannelProviderRegistrar 逐个注册
}
```

## 完整示例：GenUI 插件

`xbot-genui` 插件是真实的频道插件示例：

```json
{
  "id": "xbot.genui",
  "name": "GenUI (display_html)",
  "version": "1.0.0",
  "runtime": "grpc",
  "entry": "./bin/genui-plugin",
  "activationEvents": ["onStart"],
  "permissions": ["channels.register", "tools.register", "ui.contribute"],
  "contributes": {
    "channelProvider": {
      "name": "genui",
      "config_schema": [
        {
          "key": "enabled",
          "label": "Enable",
          "type": "toggle",
          "default_value": "true"
        }
      ]
    }
  }
}
```

插件进程：

1. 在 `activate` 时返回名称为 `"genui"` 的 `channel_provider`
2. 发送 `channel_tools` 声明 `display_html` 工具
3. 发送 `channel_prompt` 声明系统提示词片段
4. 在 `execute_tool` 时渲染 HTML 并返回结果

## 参见

- [Stdio 运行时协议](./stdio-protocol/) — 基础 JSON-RPC 协议
- [架构概览](./architecture/) — 频道插件如何融入系统
- [插件配置](./configuration/) — 频道配置
- [内置插件：GenUI](./builtin/genui/) — 真实的频道插件示例
