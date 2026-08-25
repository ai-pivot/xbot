---
title: "Stdio 运行时协议"
weight: 13
---

Stdio 插件通过 stdin/stdout 上的 JSON 与 xbot 通信。该协议支持任何编程语言。

## 协议概述

stdio 运行时使用双向 JSON 行协议：

- **xbot → 插件**：请求（带 `method` 字段的 JSON 对象）
- **插件 → xbot**：响应（带 `result` 或 `error` 的 JSON 对象）或入站消息（带 `method` 字段的 JSON 对象）

```
xbot                    Plugin Process
 │                           │
 │ ── activate request ────→ │
 │ ←── activate response ── │
 │                           │
 │ ── execute_tool req ────→ │
 │ ←── tool result ───────── │
 │                           │
 │ ←── channel_inbound ───── │ (async push)
 │                           │
 │ ── hook event ──────────→ │
 │ ←── hook result ───────── │
 │                           │
 │ ── deactivate ──────────→ │
 │ ←── response ──────────── │
```

## 消息类型

### 请求（xbot → 插件）

```json
{
  "method": "activate",
  "params": {
    "pluginId": "my-plugin",
    "config": { "key": "value" }
  }
}
```

### 响应（插件 → xbot）

```json
{
  "result": "success",
  "tools": [...],
  "hooks": [...],
  "enrichers": [...],
  "channel_provider": {...}
}
```

或错误：

```json
{
  "error": "something went wrong"
}
```

### 入站消息（插件 → xbot，异步）

```json
{
  "method": "channel_inbound",
  "params": {
    "message": "user input"
  }
}
```

## 方法

### `activate`

插件被激活时发送。插件应在响应中注册其能力。

**请求：**
```json
{
  "method": "activate",
  "params": {
    "pluginId": "my-plugin",
    "config": { "setting1": "value1" }
  }
}
```

**响应：**
```json
{
  "tools": [
    {
      "name": "my-tool",
      "description": "Does something useful",
      "parameters": [
        {"name": "input", "type": "string", "description": "Input text", "required": true}
      ]
    }
  ],
  "hooks": [
    {"event": "PreToolUse", "matcher": "Shell"}
  ],
  "enrichers": [
    {"name": "context-enricher"}
  ],
  "channel_provider": {
    "name": "my-channel",
    "config_schema": [...]
  }
}
```

### `deactivate`

插件被停用时发送。插件应清理资源。

**请求：**
```json
{
  "method": "deactivate"
}
```

**响应：**
```json
{
  "result": "ok"
}
```

### `execute_tool`

LLM 调用插件注册的工具时发送。

**请求：**
```json
{
  "method": "execute_tool",
  "params": {
    "toolName": "my-tool",
    "input": "user input"
  }
}
```

**响应：**
```json
{
  "result": "Tool output text"
}
```

或错误：
```json
{
  "error": "Tool execution failed: ..."
}
```

### `hook`

生命周期 hook 触发时发送。

**请求：**
```json
{
  "method": "hook",
  "params": {
    "event": "PreToolUse",
    "toolName": "Shell",
    "toolInput": "ls -la",
    "sessionId": "session-123",
    "channel": "cli",
    "chatId": "chat-456"
  }
}
```

**响应：**
```json
{
  "hook_result": {
    "decision": "allow",
    "message": ""
  }
}
```

Hook 决策：`allow`、`deny`、`defer`、`ask`。

### `enrich`

上下文增强器被调用时发送。

**请求：**
```json
{
  "method": "enrich",
  "params": {
    "enricherName": "context-enricher"
  }
}
```

**响应：**
```json
{
  "result": "Additional context to inject"
}
```

### `config_changed`

插件配置发生变化时发送（热重载）。

**请求：**
```json
{
  "method": "config_changed",
  "params": {
    "config": { "setting1": "new-value" }
  }
}
```

**响应：**
```json
{
  "result": "ok"
}
```

## 频道插件协议

频道插件使用额外的入站消息：

### `channel_tools`

为特定频道声明工具：

```json
{
  "method": "channel_tools",
  "params": {
    "tools": [...]
  }
}
```

### `channel_prompt`

声明频道特定的系统提示词片段：

```json
{
  "method": "channel_prompt",
  "params": {
    "system_parts": {
      "05_channel_xxx": "Channel-specific context"
    }
  }
}
```

### `web_ui`

声明 Web UI 组件：

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

## 错误处理

- **进程崩溃**：xbot 检测到 stdout 关闭并将插件标记为错误状态
- **超时**：插件调用有 30 秒超时（可通过清单 `timeout` 配置）
- **畸形 JSON**：解析失败的行会被记录日志并跳过
- **自动重试**：若启用，xbot 会以指数退避重试激活

## Python 示例

```python
#!/usr/bin/env python3
import json
import sys

def handle_request(req):
    method = req.get("method")
    params = req.get("params", {})

    if method == "activate":
        return {
            "tools": [{
                "name": "greet",
                "description": "Greet someone",
                "parameters": [
                    {"name": "name", "type": "string", "description": "Name to greet", "required": True}
                ]
            }],
            "hooks": [
                {"event": "PostToolUse", "matcher": "Shell"}
            ]
        }
    elif method == "execute_tool":
        tool = params.get("toolName")
        if tool == "greet":
            return {"result": f"Hello, {params.get('input', 'World')}!"}
        return {"error": f"unknown tool: {tool}"}
    elif method == "hook":
        event = params.get("event")
        if event == "PostToolUse":
            # 记录工具使用
            return {"hook_result": {"decision": "allow"}}
        return {"hook_result": {"decision": "allow"}}
    elif method == "deactivate":
        return {"result": "ok"}

    return {"error": f"unknown method: {method}"}

for line in sys.stdin:
    try:
        req = json.loads(line)
        resp = handle_request(req)
        sys.stdout.write(json.dumps(resp) + "\n")
        sys.stdout.flush()
    except Exception as e:
        sys.stdout.write(json.dumps({"error": str(e)}) + "\n")
        sys.stdout.flush()
```

## Node.js 示例

```javascript
const readline = require('readline');
const rl = readline.createInterface({ input: process.stdin });

rl.on('line', (line) => {
    const req = JSON.parse(line);
    const method = req.method;
    const params = req.params || {};

    let resp;
    if (method === 'activate') {
        resp = {
            tools: [{
                name: 'timestamp',
                description: 'Get current timestamp',
                parameters: []
            }]
        };
    } else if (method === 'execute_tool') {
        if (params.toolName === 'timestamp') {
            resp = { result: new Date().toISOString() };
        } else {
            resp = { error: `unknown tool: ${params.toolName}` };
        }
    } else if (method === 'deactivate') {
        resp = { result: 'ok' };
    } else {
        resp = { error: `unknown method: ${method}` };
    }

    process.stdout.write(JSON.stringify(resp) + '\n');
});
```

## 协议类型（Go）

```go
type PluginRequest struct {
    Method string         `json:"method"`
    Params map[string]any `json:"params,omitempty"`
}

type PluginResponse struct {
    Result          string              `json:"result,omitempty"`
    Error           string              `json:"error,omitempty"`
    Tools           []ToolDef           `json:"tools,omitempty"`
    Hooks           []hookReg           `json:"hooks,omitempty"`
    HookResult      *HookResult         `json:"hook_result,omitempty"`
    Enrichers       []enricherReg       `json:"enrichers,omitempty"`
    ChannelProvider *ChannelProviderDecl `json:"channel_provider,omitempty"`
}

type PluginInbound struct {
    Method string         `json:"method"`
    Params map[string]any `json:"params,omitempty"`
}
```

## 参见

- [频道插件](./channel-plugins/) — 完整频道适配器开发
- [脚本运行时](./script-runtime/) — 更简单的脚本插件
- [架构概览](./architecture/) — stdio 运行时在系统中的位置
- [开发指南：Stdio 插件](./cookbook/stdio-plugin/) — 分步指南
