---
title: "Permissions"
weight: 6
---

xbot's plugin permission system provides fine-grained capability control. Plugins declare required permissions in `plugin.json` and can only access the APIs they declare.

## How It Works

1. **Declaration**: Plugins list required permissions in `plugin.json`:
   ```json
   {
     "permissions": ["tools.register", "ui.contribute", "storage"]
   }
   ```

2. **Validation**: During manifest loading, permissions are validated against the known list. Unknown permissions are rejected.

3. **Enforcement**: At runtime, `PluginContext` wraps every method call with a `PermissionChecker`. Undeclared permissions return an error.

## Permission List

### Backend (Go) Permissions

| Permission | Description | PluginContext Methods |
|-----------|-------------|----------------------|
| `tools.register` | Register tools for the LLM | `RegisterTool`, `RegisterTools`, `UseMiddleware` |
| `hooks.register` | Register lifecycle hooks | `OnPreToolUse`, `OnPostToolUse`, `OnUserPrompt`, `OnAgentStop`, `OnSessionStart`, `OnSessionEnd`, `OnEvent`, `OnAllToolUse`, `OnError` |
| `bus.read` | Subscribe to event bus | `Subscribe` |
| `bus.write` | Publish to event bus | `Publish` |
| `bus.plugin` | Plugin-to-plugin events | Requires `bus.read` + `bus.write` |
| `ui.contribute` | Contribute UI widgets | `ContributeUI`, `UpdateWidget`, `RegisterWebActionHandler` |
| `ui.themes` | Contribute themes | `ContributeTheme` |
| `channels.register` | Register channel providers | Channel provider registration |
| `storage` | Access per-plugin KV storage | `Storage`, `StorageInt`, `StorageBool`, `StorageJSON`, `StorageGetJSON` |
| `cron` | Schedule cron jobs | `ScheduleCron` |

### Frontend (Web) Permissions

| Permission | Description | Context API |
|-----------|-------------|------------|
| `rpc` | Make RPC calls to the backend | `ctx.rpc` |
| `ui` | Access UI API (open views, tabs) | `ctx.ui` |
| `events` | Access event bus | `ctx.events` |
| `commands` | Register and execute commands | `ctx.commands` |
| `state` | Access shared state | `ctx.state` |
| `plugins` | Access plugin management API | `ctx.plugins` |
| `config` | Access configuration API | `ctx.config` |

## PermissionChecker

The `PermissionChecker` (`plugin/permissions.go`) validates permissions:

```go
type PermissionChecker struct {
    permissions map[string]bool
}

func NewPermissionChecker(permissions []string) *PermissionChecker

func (pc *PermissionChecker) Has(permission string) bool
func (pc *PermissionChecker) HasAll(permissions ...string) bool
func (pc *PermissionChecker) HasAny(permissions ...string) bool
```

## Validation

During manifest loading, `validateManifest` checks that all declared permissions are valid:

```go
func IsValidPermission(perm string) bool
func AllPermissions() []string
```

Unknown permissions cause manifest validation to fail, preventing the plugin from loading.

## Best Practices

1. **Declare only what you need**: Minimize the permission surface
2. **Don't request `bus.plugin` unless you need both read and write**: Use `bus.read` or `bus.write` individually
3. **Frontend permissions are separate**: Web plugins use a different permission set (`rpc`, `ui`, `events`, etc.)
4. **Permissions are not hierarchical**: `ui.contribute` does not imply `ui.themes`

## See Also

- [Plugin Manifest](./manifest/) — Where permissions are declared
- [PluginContext API](./plugin-context/) — How permissions filter API access
- [API Reference: Permissions List](./api-reference/permissions-list/) — Complete permission reference
