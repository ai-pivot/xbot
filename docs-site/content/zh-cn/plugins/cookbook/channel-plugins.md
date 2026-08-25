---
title: "Channel 插件"
weight: 9
---

Channel 插件是贡献**完整渠道适配器**的 stdio 插件——一种新的消息传输通道（类似飞书、QQ），拥有自己的工具、系统提示词片段与 Web UI。示例：`plugin/examples/echo-channel/`（HTTP echo 渠道）、`plugin/examples/web-ui-demo/`（声明式 Web 组件），以及生产级的 `plugins/xbot-genui/`（`display_html` 工具作为渠道插件实现）。

## 两层协议

Channel 插件运行 stdio 运行时，但使用**双向 JSON-RPC**（请求带 `id`；响应回显 id）。协议新增两个方向的异步消息：

```
xbot → 插件（事件推送）： {"type":"progress","progress":{...}}
xbot → 插件（RPC 请求）： {"id":"1","method":"channel_send","params":{...}}
插件 → xbot（RPC 请求）： {"id":"p1","method":"send_inbound","params":{...}}
插件 → xbot（声明）：     {"type":"channel_tools","tools":[...]}
```

这正是 `plugin/examples/echo-channel/main.py` 处理的形态：`handle_incoming` 按 `id`/`method`/`type` 是否存在路由（`handle_xbot_rpc`、`handle_xbot_event`、`handle_plugin_request`）。

## 声明渠道提供者

`activate` 响应包含 `channel_provider`（`protocol.ChannelProviderDecl`）：

```json
{
  "id": "com.example.echo-channel",
  "runtime": "grpc",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["channels.register"],
  "contributes": {
    "channelProvider": {
      "name": "echo",
      "config_schema": [
        { "key": "enabled", "label": "Enable", "type": "toggle", "default_value": "true" },
        { "key": "port", "label": "Port", "type": "number", "default_value": "9876" }
      ]
    }
  }
}
```

插件的 `activate` 处理器在运行时返回同样的声明：

```python
def handle_activate(params):
    return {
        "channel_provider": {
            "name": "echo",
            "config_schema": config.get("config_schema", []),
        }
    }
```

后端接线：`plugin/channel_provider.go` —— `SetChannelProviderFactory`（由 serverapp 注册）根据声明 + 进程创建 `channel.ChannelProvider`；`WireChannelProviders(pm)` 在 `ActivateAll()` 之后注册所有活跃插件的 provider。

⚠️ **渠道激活需要 config.json 里的 `channels.<name>.enabled=true`。** `stdioChannelPluginProvider.IsEnabled(nil)` 返回 false（`serverapp/channel_plugin.go`）——只装插件不够。若 `channels` 里没有该插件渠道名的条目，渠道实例永远不会创建，`channel_config` 永远不会发送，声明的工具也不可见。

## 三种声明协议

`channel_config` 到达后，插件以异步 type 消息推送声明（每种都可热更新——新消息整体替换上一份）：

### 1. `channel_tools` —— 渠道限定工具

```json
{"type":"channel_tools","tools":[
  {"name":"display_html","description":"Render an interactive UI...","parameters":[
     {"name":"code","type":"string","description":"TSX module source","required":true}
   ],
   "channels":["web"],
   "ui":{"mode":"genui","surface":{"kind":"panel","title":"UI","collapsible":true,"fullscreen":true,"default_open":true}}}
]}
```

`ui` 块镜像 `tools.UIDecl`（mode/param/libs/surface）。`xbot-genui`（`plugins/xbot-genui/main.go declareTools`）就是这样为 `web` 渠道声明 `display_html` 的。每个工具包装为 `ChannelToolBridge`，经 `RegisterForChannel(channel, tool)` 注册（`plugin/channel_tool_bridge.go`）；执行经 `execute_tool` RPC 代理到插件进程。

### 2. `channel_prompt` —— 系统提示词片段

```json
{"type":"channel_prompt","system_parts":{"05_channel_genui":"..."}}
```

存入 `channelPluginPromptProvider`（RWMutex 保护），经 `OnChannelPrompt` 回调 → `Agent.AddChannelPromptProvider` → `ChannelPromptMiddleware`（优先级 5）。键命名约定：`"05_channel_xxx"` 前缀（在 `"00_base"` 之后、`"10_skills"` 之前）。

### 3. `web_ui` —— 声明式 Web 组件

`plugin/examples/web-ui-demo/main.py` 发送 `{"type":"web_ui", ...}` 声明（sparkline/table/badge 组件与自由代码模式）。交互经 `web_ui_action` RPC 回传：所属渠道插件优先，其次原生处理器，最后 agent 循环。

## 收发消息：send_inbound / channel_send

- **插件 → xbot** 用户消息：`send_inbound` RPC，参数 `{channel, chat_id, content, sender_id, sender_name, chat_type}`（`echo-channel/main.py send_inbound_message`）。
- **xbot → 插件** 出站回复：`channel_send` RPC，参数 `{content, chat_id}`——插件负责渲染/投递（`handle_xbot_rpc` 记入历史并回 `"ok"`）。

插件之上可以自由构建任何传输（echo-channel 跑了个 HTTP server；真实适配器对接 IM API）。

## 渠道工具的四步生命周期

1. `channel_config` 事件 → 插件发送 `channel_tools` 声明。
2. xbot 把每个工具包装为 `ChannelToolBridge`，注册到对应渠道。
3. Agent 调用工具 → `execute_tool` RPC → 插件返回 `{"result": ...}`。
4. GenUI 类工具：`ChannelToolBridge.Execute` 返回 `ui_code`；`WebChannel.Send` 必须识别 `genui` 元数据并转发为 `MsgTypeGenUI`——否则 TSX 会被渲染成普通代码块（两条路径都要通：流式期间的 `GenUIContent` progress 事件，流结束后的 `genui` 消息）。

## 参考实现

| 插件 | 演示内容 |
|---|---|
| `plugin/examples/echo-channel/` | 完整 JSON-RPC 循环：路由、send_inbound、历史、HTTP 前端 |
| `plugin/examples/web-ui-demo/` | `web_ui` 声明 + `web_ui_action` 处理 |
| `plugins/xbot-genui/` | 零依赖 Go 渠道插件；带 `ui` 元数据的工具；TSX 校验；`surface` 面板 |
| `plugins/xbot-git-fancy/` | 前端视图的 `web_plugin_rpc` 数据源（非渠道） |

新增插件→xbot 消息类型时，遵循既有模式（`AGENTS.md`）：加 `protocol.MsgTypeXxx` 常量 + `ChannelPluginTransport.handleIncoming` 的 `peek.Type` 分支 + `handleChannelXxx` 方法 + `ChannelPluginTransportConfig` 的 `OnChannelXxx` 回调。
