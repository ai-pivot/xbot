---
title: "Dependencies"
weight: 18
---

Plugins can declare dependencies on other plugins. The `DependencyResolver` (`plugin/dependency.go`) builds a dependency graph and computes the **activation order** using Kahn's algorithm (BFS topological sort, O(V+E)).

## Declaring dependencies

```json
{
  "id": "xbot.ci-status",
  "dependencies": [
    { "id": "xbot.git-fancy", "version": ">=0.3.0" }
  ]
}
```

`PluginDependency` (`plugin/plugin.go:145`): `ID` + `Version` (semver range string). Manifest loading validates the version format (`plugin/manifest.go`); **actual version resolution is planned for a future iteration** — today only format validation and graph resolution are enforced.

## The resolver

```go
dr := plugin.NewDependencyResolver()
dr.AddManifest(manifestA) // duplicate ID replaces the existing entry
dr.AddManifest(manifestB)
order, err := dr.Resolve() // []string of plugin IDs in activation order
```

Behavior (`plugin/dependency.go:48`):

1. Build in-degree map + adjacency list from `m.Dependencies`.
2. Queue all in-degree-0 nodes (sorted for determinism).
3. BFS: emit a plugin, decrement dependents, enqueue newly-free nodes (kept sorted).
4. **Cycle detection**: if fewer plugins were emitted than added, the remaining in-degree>0 set is reported as `ErrCircularDependency{Cycle}`.
5. **Missing dependency**: a dependency referencing an un-added plugin returns `ErrMissingDependency{PluginID, Missing}`.

`Validate()` checks existence only, returning the first missing dependency.

## Integration with the manager

`PluginManager.Discover` (`plugin/manager.go:511`) calls `resolveActivationOrder()` after loading manifests. Failure **does not fail discovery** — plugins load without guaranteed order and the error is logged. `ActivateAll` follows the computed topological order: dependencies activate before dependents.

## Error types

```go
type ErrCircularDependency struct{ Cycle []string }
type ErrMissingDependency struct{ PluginID, Missing string }
```

Both implement `error`; `errors.As`/`errors.Is` work for typed handling.

## Practical patterns

```json
// A widget plugin depending on a data-source plugin
{
  "id": "xbot.ci-panel",
  "dependencies": [ { "id": "xbot.git-fancy", "version": "1.0.0" } ]
}
```

Recommended practices:

- **Reverse-DNS IDs** make dependencies unambiguous (`xbot.git-fancy`, not `git-fancy`).
- **Pin minimum versions** even though enforcement is future work — manifests are read by humans and future versions of xbot.
- **Don't create cycles** — even A→B→A "soft" cycles fail resolution. Restructure shared logic into a common plugin both depend on.
- **Missing dependencies are soft failures at discovery** — if your plugin requires another, verify it yourself in `Activate` (e.g. check the manager or the event bus) and fail activation with a clear error.
- Dependency order is **deterministic** (sorted queues) — useful for reproducible startup in tests.
