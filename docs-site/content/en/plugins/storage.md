---
title: "Plugin Storage"
weight: 7
---

Every plugin gets a private key-value storage for persisting state across sessions.

## Overview

Storage is file-based, using a JSON file per plugin:

```
~/.xbot/plugins/<plugin-id>/data/storage.json
```

The storage is loaded on plugin activation and persisted on every write using atomic write (tmp + rename).

## API

Storage is accessed via `PluginContext` (requires `storage` permission):

```go
type StorageAccessor interface {
    Get(key string) (string, bool)
    Set(key, value string) error
    Delete(key string) error
    Keys() []string
    Clear() error
}
```

### Typed Helpers

`PluginContext` provides typed convenience methods:

```go
// Integer storage
count, ok := ctx.StorageInt("counter")  // (int64, bool)

// Boolean storage
enabled, ok := ctx.StorageBool("enabled")  // (bool, bool)

// JSON storage (marshal/unmarshal)
ctx.StorageJSON("config", map[string]any{"theme": "dark"})

// JSON retrieval
var cfg map[string]any
ctx.StorageGetJSON("config", &cfg)
```

## Usage Example

```go
func (p *MyPlugin) Activate(ctx plugin.PluginContext) error {
    // Read a counter
    count, _ := ctx.StorageInt("call_count")
    count++
    
    // Store it back
    ctx.Storage().Set("call_count", strconv.FormatInt(count, 10))
    
    // Store structured data
    ctx.StorageJSON("last_run", map[string]any{
        "timestamp": time.Now().Unix(),
        "workDir":   ctx.WorkingDir(),
    })
    
    return nil
}
```

## Implementation Details

- **File location**: `~/.xbot/plugins/<id>/data/storage.json`
- **File permissions**: `0600` (owner read/write only)
- **Atomic writes**: Uses tmp file + `os.Rename` for crash safety
- **Thread-safe**: Protected by `sync.RWMutex`
- **Auto-load**: Storage is loaded from disk on plugin activation
- **Failed parse**: If the storage file is corrupted, the plugin starts fresh with an empty map

## Storage vs Configuration

| Feature | Storage | Configuration |
|---------|---------|----------------|
| Who writes | Plugin code | User (via settings UI) |
| Mutability | Read/write at runtime | Read-only at runtime |
| Location | `data/storage.json` | `config.json` |
| Permission | `storage` | None (read-only) |
| Use case | Plugin state, caches | User preferences |

## See Also

- [PluginContext API](./plugin-context/) — StorageProvider interface
- [Configuration](./configuration/) — User-configurable settings
- [Permissions](./permissions/) — `storage` permission
