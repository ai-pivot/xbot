---
title: "插件快速开始"
weight: 1
---

5 分钟创建你的第一个 xbot 插件。本指南介绍三种最简单的插件类型。

## 前置条件

- xbot 已安装并运行
- 基本终端使用能力
- Go 插件需要 Go 1.22+

## 插件目录结构

所有插件位于 `~/.xbot/plugins/<plugin-id>/`。最小结构：

```
~/.xbot/plugins/my-plugin/
├── plugin.json    # 必需：插件清单
└── main.sh        # 入口点（因运行时而异）
```

## 类型 1：脚本插件（最简单）

脚本插件运行外部脚本（bash、Python、Node 等）。适合小部件和简单工具。

### 步骤 1：创建清单

```json
{
  "id": "hello-widget",
  "name": "Hello Widget",
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

### 步骤 2：创建脚本

```bash
#!/bin/bash
# main.sh — 激活时和刷新小部件时运行
echo "Hello, xbot!"
```

### 步骤 3：安装并激活

```bash
mkdir -p ~/.xbot/plugins/hello-widget
# 将 plugin.json 和 main.sh 复制到目录中
# 重启 xbot 或运行：/plugin reload-all
```

问候语将出现在 CLI 状态栏。脚本插件在钩子触发和目录变更时自动刷新。

## 类型 2：Go 原生插件

原生 Go 插件编译进 xbot 二进制。适合工具、钩子和复杂逻辑。

### 步骤 1：实现 Plugin 接口

```go
package main

import (
    "context"
    "xbot/plugin"
)

type HelloPlugin struct {
    manifest plugin.PluginManifest
}

func (p *HelloPlugin) Manifest() plugin.PluginManifest {
    return p.manifest
}

func (p *HelloPlugin) Activate(ctx plugin.PluginContext) error {
    tool := plugin.ToolFromFunc("hello", "Say hello", func(ctx context.Context, input string) (string, error) {
        return "Hello, World!", nil
    })
    return ctx.RegisterTool(tool)
}

func (p *HelloPlugin) Deactivate(ctx plugin.PluginContext) error {
    return nil
}
```

### 步骤 2：在 xbot 中注册

原生插件通过 `PluginManager.Register()` 在 xbot 初始化时注册。详见 [Go 原生插件指南](../cookbook/go-native-plugin/)。

## 类型 3：Stdio 插件（任意语言）

Stdio 插件通过 stdin/stdout 的 JSON-RPC 通信。可使用任意语言。

### 步骤 1：创建清单

```json
{
  "id": "python-hello",
  "name": "Python Hello",
  "version": "1.0.0",
  "runtime": "stdio",
  "entry": "python3 main.py",
  "activationEvents": ["onStart"],
  "permissions": ["tools.register"]
}
```

### 步骤 2：实现协议

```python
#!/usr/bin/env python3
import json
import sys

def handle_request(req):
    if req["method"] == "activate":
        return {"result": {"tools": [{"name": "hello", "description": "Say hello"}]}}
    elif req["method"] == "execute_tool":
        return {"result": {"output": "Hello from Python!"}}
    return {"error": "unknown method"}

for line in sys.stdin:
    req = json.loads(line)
    resp = handle_request(req)
    sys.stdout.write(json.dumps(resp) + "\n")
    sys.stdout.flush()
```

详见 [Stdio 运行时协议](../stdio-runtime/)。

## 下一步

- [架构概览](../architecture/) — 了解插件系统工作原理
- [插件清单](../manifest/) — 完整清单规范
- [PluginContext API](../plugin-context/) — 插件访问 xbot 的网关
- [开发指南](../cookbook/) — 逐步开发指南
