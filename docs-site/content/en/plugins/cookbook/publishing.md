---
title: "Publishing"
weight: 21
---

How to package, distribute, version, and migrate plugins. The machinery: `PluginRegistry` (`plugin/registry.go`), config export/import (`plugin/export.go`), storage migrations (`plugin/migration.go`), and checksums (`plugin/manifest.go VerifyChecksum`).

## Package layout

```
my-plugin/
├── plugin.json       # manifest — the entry point for discovery
├── main.go           # or main.sh / main.py / bin/
└── web/              # optional frontend module (index.js + chunks)
```

Install = copy the directory under `~/.xbot/plugins/<id>/` (or an extra search dir registered via `PluginManager.AddSearchDirs`). That's the entire distribution story for the MVP — there is no archive format yet.

## The registry (MVP)

`PluginRegistry` (`plugin/registry.go`) wraps a `PluginManager` with sources (`RegistrySource{Type, URL}`) where `Type` is `github`/`url`/`local`:

- **`Search(ctx, query)`** — case-insensitive match against ID/Name/Description of **locally installed** plugins only.
- **`Install(ctx, id)`** — MVP supports **local sources only**: `URL` is the plugin directory path; `InstallPlugin` copies it. GitHub/URL sources are defined but `InstallFromSource` is Phase 3.
- `RegistryEntry` carries ID/Name/Version/Description/Author/Source/DownloadURL/**Checksum** (SHA256 of the plugin archive).

## Checksums

`VerifyChecksum(dir)` (`plugin/manifest.go`) verifies the SHA256 recorded in the manifest against directory contents. Use it to detect tampering or incomplete copies:

```go
if err := plugin.VerifyChecksum(pluginDir); err != nil {
	// reject the plugin — contents don't match the declared checksum
}
```

## ID and version rules

- **ID**: regex `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$` (`isValidPluginID`, `plugin/manifest.go`) — reverse DNS recommended (`com.example.echo-channel`, `xbot.git-fancy`). This same regex guards the static-serving path (`isValidPluginIDForServe`).
- **Version**: strict semver, parsed by `ParseVersion`/`parseSemver`. Invalid versions fail manifest loading.
- Manifest `homepage` + `author` fields exist for marketplace display.

## Lifecycle: install/uninstall events

`PluginManager` emits `PluginEventInstalled`/`PluginEventUninstalled`/`PluginEventReloaded`/`PluginEventError` via `PluginEventNotifier` (`plugin/events.go`). Consumers subscribe with `pm.OnPluginEvent(cb)` — a marketplace UI or audit system listens here. All actions land in the audit log (`~/.xbot/plugins/audit.jsonl`).

`InstallPlugin` uses `filepath.EvalSymlinks` to resolve the real path before deletion checks — only directories under `xbotHome` are ever deleted (symlink traversal protection).

## Config export / import

`PluginManager.ExportConfig()` serializes all plugins' manifests, states, and user configs; `ImportConfig(data)` restores configs for locally-present plugins (missing ones skipped) and merges the disabled set (union):

```go
data, _ := pm.ExportConfig()       // ConfigExport{Version, ExportedAt, Disabled, Plugins}
err := pm.ImportConfig(data)       // best-effort restore
```

⚠️ `ExportConfig` acquires the manager RLock — never call it from inside a plugin's `Activate`/`Deactivate` (which run under the write lock).

## Versioning storage data

Plugins that persist state must migrate it across versions. `plugin/migration.go`:

```go
plugin.RegisterMigration("xbot.demo", plugin.PluginMigration{
	FromVersion: "1.0.0",
	ToVersion:   "1.1.0",
	Migrate: func(ctx context.Context, storage plugin.StorageAccessor) error {
		old, _ := storage.Get("count")
		storage.Set("counters.total", old)   // restructure the schema
		return nil
	},
})
```

- `RunMigrations(pluginID, storage)` orders steps by semver and applies pending ones exactly once (recorded under `_migrations`).
- The `Migrator` takes a **backup** before each run: `~/.xbot/plugins/<id>/backups/<version>/`. Rollback restores the latest backup.
- Migrations run **sequentially in version order**.

## Release checklist

1. `plugin.json` parses (`python3 -m json.tool`) and version bumps are semver.
2. Permissions are minimal **and complete** (see [Permissions](../permissions/)).
3. `go build ./...` / `go test ./...` for Go backends; `vitest` for web views.
4. Manifest permission test (git-fancy `TestManifestPermissions` pattern) passes.
5. Storage migrations registered for schema changes; backup/restore verified manually.
6. `VerifyChecksum` passes on the release directory.
7. README documents the plugin ID, runtime, permissions, and configuration schema.
8. For channel plugins: users must also add `channels.<name>.enabled=true` to config.json — document it prominently.
9. Ship the built frontend bundle (for web plugins) under `web/`; never commit dev-only files there.
