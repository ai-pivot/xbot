---
title: "插件配置"
weight: 15
---

插件可以声明用户可配置的设置，这些设置会出现在设置 UI 中，并按插件独立存储。

## 概述

插件配置使用 `plugin.json` 中 `contributes.configuration` 下的声明式 schema。用户通过 `~/.xbot/plugins/<id>/config.json` 覆盖默认值。配置在激活时以及热重载时注入插件。

## ConfigurationContribution

```go
type ConfigurationContribution struct {
    Title      string                  `json:"title"`
    Properties map[string]ConfigProperty `json:"properties"`
}
```

## ConfigProperty

```go
type ConfigProperty struct {
    Type        string        `json:"type"`        // "string", "number", "boolean", "select", "multiselect"
    Label       string        `json:"label,omitempty"`
    Description string        `json:"description,omitempty"`
    Default     any           `json:"default,omitempty"`
    Options     []ConfigOption `json:"options,omitempty"` // 用于 select/multiselect
    Section     string        `json:"section,omitempty"`  // 分组
    Secret      bool          `json:"secret,omitempty"`   // 在 UI 中掩码
    Placeholder string        `json:"placeholder,omitempty"`
    Required    bool          `json:"required,omitempty"`
    Minimum     *float64      `json:"minimum,omitempty"` // 用于 number
    Maximum     *float64      `json:"maximum,omitempty"` // 用于 number
}
```

## 清单声明

```json
{
  "contributes": {
    "configuration": {
      "title": "My Plugin Settings",
      "properties": {
        "apiKey": {
          "type": "string",
          "label": "API Key",
          "description": "Your API key for the service",
          "secret": true,
          "required": true,
          "placeholder": "sk-..."
        },
        "maxItems": {
          "type": "number",
          "label": "Max Items",
          "description": "Maximum number of items to fetch",
          "default": 10,
          "minimum": 1,
          "maximum": 100
        },
        "enabled": {
          "type": "boolean",
          "label": "Enabled",
          "default": true
        },
        "mode": {
          "type": "select",
          "label": "Mode",
          "default": "auto",
          "options": [
            {"label": "Auto", "value": "auto"},
            {"label": "Manual", "value": "manual"}
          ]
        }
      }
    }
  }
}
```

## 属性类型

| 类型 | 描述 | UI 元素 |
|------|------|---------|
| `string` | 文本输入 | 文本框（`secret: true` 时掩码） |
| `number` | 数字输入 | 带 min/max 边界的数字框 |
| `boolean` | 开关 | 复选框 |
| `select` | 单选 | 下拉框 |
| `multiselect` | 多选 | 多选下拉框 |

## PluginConfigStore

配置由 `PluginConfigStore`（`plugin/config.go`）管理：

```go
type PluginConfigStore struct { ... }

func NewPluginConfigStore(xbotHome string) *PluginConfigStore

func (c *PluginConfigStore) Load(pluginID string) map[string]any
func (c *PluginConfigStore) Save(pluginID string, config map[string]any) error
func (c *PluginConfigStore) Update(pluginID, key string, value any)
func (c *PluginConfigStore) Subscribe(pluginID string, cb func(map[string]any))
func (c *PluginConfigStore) InvalidateCache(pluginID string)
func (c *PluginConfigStore) GetDefaultConfig(manifest *PluginManifest) map[string]any
func (c *PluginConfigStore) ConfigSchema(m *PluginManifest) []ConfigSchemaEntry
```

### 存储

配置存放在 `~/.xbot/plugins/<id>/config.json`：

```json
{
  "apiKey": "sk-abc123",
  "maxItems": 20,
  "enabled": true,
  "mode": "auto"
}
```

### 默认值

`GetDefaultConfig` 从清单的 `contributes.configuration.properties` 提取默认值。若不存在用户配置，则使用默认值。

### 缓存

配置在内存中缓存。`InvalidateCache(pluginID)` 清除特定插件的缓存。`Save` 和 `Update` 会自动使缓存失效。

## 访问配置

### Go 插件（Native/Stdio）

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    cfg, err := ctx.Config()
    if err != nil {
        return err
    }

    apiKey := cfg["apiKey"].(string)
    maxItems := int(cfg["maxItems"].(float64))

    // 使用配置...
    return nil
}
```

### 热重载

插件订阅配置变更：

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // 初始配置
    cfg, _ := ctx.Config()
    p.applyConfig(cfg)

    // 热重载
    ctx.OnConfigChanged(func(config map[string]any) {
        p.applyConfig(config)
    })

    return nil
}
```

### 脚本插件

脚本插件通过 `XBOT_PLUGIN_CONFIG` 环境变量（JSON）接收配置：

```bash
#!/bin/bash
config=$(echo "$XBOT_PLUGIN_CONFIG" | jq -r '.')
api_key=$(echo "$XBOT_PLUGIN_CONFIG" | jq -r '.apiKey // "default"')
echo "ok|API: $api_key"
```

### Stdio 插件

Stdio 插件在 `activate` 请求中接收配置：

```json
{
  "method": "activate",
  "params": {
    "pluginId": "my-plugin",
    "config": {
      "apiKey": "sk-abc123",
      "maxItems": 20
    }
  }
}
```

并在热重载时收到 `config_changed` 消息：

```json
{
  "method": "config_changed",
  "params": {
    "config": {
      "apiKey": "sk-new-key"
    }
  }
}
```

## Web 插件配置

Web 插件通过 web 清单中的 `contributes.configuration` 字段声明配置。前端通过 `ctx.config.get(key)` 读取配置，通过 `ctx.config.set(key, value)` 写入配置。

## 参见

- [插件清单](./manifest/) — 配置的声明位置
- [PluginContext API](./plugin-context/) — 配置访问方法
- [热重载与监控](./hot-reload/) — 配置驱动的重载
- [脚本运行时](./script-runtime/) — XBOT_PLUGIN_CONFIG 环境变量
