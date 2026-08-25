---
title: "Plugin Dependencies"
weight: 16
---

Plugins can declare dependencies on other plugins. The dependency resolver uses topological sort to determine activation order.

## Manifest Declaration

```json
{
  "dependencies": [
    {
      "id": "xbot.utils",
      "version": "^1.0.0"
    }
  ]
}
```

| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Required plugin ID |
| `version` | string | Version constraint (semver range) |

## DependencyResolver

The `DependencyResolver` (`plugin/dependency.go`) uses Kahn's algorithm (BFS-based topological sort) with O(V+E) time complexity.

```go
type DependencyResolver struct { ... }

func NewDependencyResolver() *DependencyResolver
func (dr *DependencyResolver) AddManifest(m *PluginManifest)
func (dr *DependencyResolver) Resolve() ([]string, error)
func (dr *DependencyResolver) Validate() error
```

### Resolve

Returns the activation order — plugins with no dependencies come first, followed by plugins that depend on them:

```go
dr := NewDependencyResolver()
dr.AddManifest(manifestA)  // no deps
dr.AddManifest(manifestB)  // depends on A
dr.AddManifest(manifestC)  // depends on A and B

order, err := dr.Resolve()
// order = ["xbot.a", "xbot.b", "xbot.c"]
```

### Validate

Checks that all declared dependencies exist among the added manifests. Returns `ErrMissingDependency` for the first missing dependency.

## Error Types

### ErrMissingDependency

```go
type ErrMissingDependency struct {
    PluginID string  // Plugin that has the missing dependency
    Missing  string  // Missing dependency ID
}
```

Returned when a plugin declares a dependency on a plugin that doesn't exist.

### ErrCircularDependency

```go
type ErrCircularDependency struct {
    Cycle []string  // Plugin IDs in the cycle
}
```

Returned when a circular dependency is detected. The `Cycle` field lists the plugins involved in the cycle.

## Version Constraints

Version constraints use semver range syntax:

| Constraint | Description |
|-----------|-------------|
| `1.0.0` | Exact version |
| `^1.0.0` | Compatible with 1.0.0 (>=1.0.0, <2.0.0) |
| `>=1.0.0` | At least 1.0.0 |
| `~1.0.0` | Patch-level compatible (>=1.0.0, <1.1.0) |
| `1.x` | Major version 1 |
| `*` | Any version |

**Note**: Currently only format validation is performed. Actual version resolution will be added in a future iteration.

## Activation Order

During `PluginManager.Discover()`, after all manifests are loaded:

1. All manifests are added to the `DependencyResolver`
2. `Resolve()` is called to get the activation order
3. `ActivateAll()` activates plugins in dependency order
4. If resolution fails (missing dependency or cycle), an error is logged but discovery continues — plugins are still loaded without guaranteed order

## Example

```json
// plugin.json for "xbot.utils"
{
  "id": "xbot.utils",
  "name": "Utils",
  "version": "1.0.0",
  "runtime": "native",
  "entry": ""
}

// plugin.json for "xbot.my-plugin"
{
  "id": "xbot.my-plugin",
  "name": "My Plugin",
  "version": "1.0.0",
  "runtime": "stdio",
  "entry": "python3 main.py",
  "dependencies": [
    {"id": "xbot.utils", "version": "^1.0.0"}
  ]
}
```

`xbot.utils` activates before `xbot.my-plugin`.

## See Also

- [Plugin Manifest](./manifest/) — Where dependencies are declared
- [Architecture](./architecture/) — How dependencies affect activation
- [API Reference](./api-reference/) — DependencyResolver API
