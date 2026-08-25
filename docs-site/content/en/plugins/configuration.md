---
title: "Plugin Configuration"
weight: 15
---

Plugins can declare user-configurable settings that appear in the settings UI and are stored per-plugin.

## Overview

Plugin configuration uses a declarative schema in `plugin.json` under `contributes.configuration`. Users override defaults via `~/.xbot/plugins/<id>/config.json`. The configuration is injected into the plugin at activation and on hot-reload.

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
    Options     []ConfigOption `json:"options,omitempty"` // for select/multiselect
    Section     string        `json:"section,omitempty"`  // grouping
    Secret      bool          `json:"secret,omitempty"`   // mask in UI
    Placeholder string        `json:"placeholder,omitempty"`
    Required    bool          `json:"required,omitempty"`
    Minimum     *float64      `json:"minimum,omitempty"` // for number
    Maximum     *float64      `json:"maximum,omitempty"` // for number
}
```

## Manifest Declaration

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

## Property Types

| Type | Description | UI Element |
|------|-------------|------------|
| `string` | Text input | Text field (masked if `secret: true`) |
| `number` | Numeric input | Number field with min/max bounds |
| `boolean` | Toggle | Checkbox |
| `select` | Single choice | Dropdown |
| `multiselect` | Multiple choices | Multi-select dropdown |

## PluginConfigStore

Configuration is managed by `PluginConfigStore` (`plugin/config.go`):

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

### Storage

Configuration is stored at `~/.xbot/plugins/<id>/config.json`:

```json
{
  "apiKey": "sk-abc123",
  "maxItems": 20,
  "enabled": true,
  "mode": "auto"
}
```

### Default Values

`GetDefaultConfig` extracts defaults from the manifest's `contributes.configuration.properties`. If no user config exists, defaults are used.

### Caching

Configuration is cached in memory. `InvalidateCache(pluginID)` clears the cache for a specific plugin. `Save` and `Update` automatically invalidate the cache.

## Accessing Configuration

### Go Plugins (Native/Stdio)

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    cfg, err := ctx.Config()
    if err != nil {
        return err
    }
    
    apiKey := cfg["apiKey"].(string)
    maxItems := int(cfg["maxItems"].(float64))
    
    // Use configuration...
    return nil
}
```

### Hot Reload

Plugins subscribe to configuration changes:

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Initial config
    cfg, _ := ctx.Config()
    p.applyConfig(cfg)
    
    // Hot reload
    ctx.OnConfigChanged(func(config map[string]any) {
        p.applyConfig(config)
    })
    
    return nil
}
```

### Script Plugins

Script plugins receive configuration via the `XBOT_PLUGIN_CONFIG` environment variable (JSON):

```bash
#!/bin/bash
config=$(echo "$XBOT_PLUGIN_CONFIG" | jq -r '.')
api_key=$(echo "$XBOT_PLUGIN_CONFIG" | jq -r '.apiKey // "default"')
echo "ok|API: $api_key"
```

### Stdio Plugins

Stdio plugins receive configuration in the `activate` request:

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

And receive `config_changed` messages on hot reload:

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

## Web Plugin Configuration

Web plugins declare configuration via the `contributes.configuration` field in the web manifest. The frontend reads configuration via `ctx.config.get(key)` and writes via `ctx.config.set(key, value)`.

## See Also

- [Plugin Manifest](./manifest/) — Where configuration is declared
- [PluginContext API](./plugin-context/) — Config access methods
- [Hot Reload](./hot-reload/) — Configuration-driven reload
- [Script Runtime](./script-runtime/) — XBOT_PLUGIN_CONFIG env var
