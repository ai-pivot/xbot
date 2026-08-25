---
title: "Storage"
weight: 16
---

Every plugin gets an isolated, persistent key-value store. Implementation: `plugin/storage.go` (file-backed) with typed accessors on `PluginContext`. This is how the hello-world example keeps a `tool_call_count` counter across restarts.

## The API

```go
// StorageProvider (plugin/context.go:54)
type StorageProvider interface {
	Storage() StorageAccessor
	StorageInt(key string) (int64, bool)
	StorageBool(key string) (bool, bool)
	StorageJSON(key string, value any) error
	StorageGetJSON(key string, target any) error
}

type StorageAccessor interface {
	Get(key string) (string, bool)
	Set(key, value string) error
	Delete(key string) error
	Keys() []string
	Clear() error
}
```

## Usage

```go
func (p *Plugin) Activate(ctx plugin.PluginContext) error {
	// raw strings
	storage := ctx.Storage()
	storage.Set("last_run", time.Now().Format(time.RFC3339))
	if v, ok := storage.Get("tool_call_count"); ok {
		// ...
	}

	// typed helpers (zero-value + false on parse failure)
	if n, ok := ctx.StorageInt("counter"); ok {
		_ = n
	}
	ctx.StorageBool("feature_enabled")       // (bool, bool)
	ctx.StorageJSON("session", someStruct)   // marshal + Set
	ctx.StorageGetJSON("session", &target)   // Get + unmarshal (returns error)
	return nil
}
```

## Persistence details

`NewFileStorage(pluginDir)` (`plugin/storage.go:25`):

- **Location**: `<plugin-dir>/data/storage.json` (for plugins under `~/.xbot/plugins/<id>/`, that is `~/.xbot/plugins/<id>/data/storage.json`).
- **Atomic writes**: `json.MarshalIndent` → write `storage.json.tmp` → `os.Rename`. Never torn files.
- **Permissions**: `0600` — never use `0644` for plugin storage.
- **Corrupt file recovery**: an unparseable storage.json logs a warning and starts fresh.
- **Concurrency**: all operations guarded by a `sync.RWMutex`.

## Permissions

`storage.private` grants the plugin's own store. `storage.shared` is declared for shared storage (reserved for future cross-plugin storage).

## Migrations

Storage data is yours to version. `plugin/migration.go` provides the machinery:

```go
plugin.RegisterMigration("xbot.demo", plugin.PluginMigration{
	FromVersion: "1.0.0",
	ToVersion:   "1.1.0",
	Migrate: func(ctx context.Context, storage plugin.StorageAccessor) error {
		// transform data from the 1.0.0 schema to 1.1.0
		return nil
	},
})
```

`RunMigrations(pluginID, storage)` applies pending steps in semver order; applied steps are recorded under the `_migrations` key and never re-run. Each run takes a **backup** first (`Migrator`, `~/.xbot/plugins/<id>/backups/<version>/`) — rollback restores the most recent backup.

## Script plugins

Scripts don't get a storage API — persist state by writing files under `XBOT_WORK_DIR` or (better) ship a tiny stdio companion. Keep script plugins stateless where possible: their output cache is already per-workDir.

## Pitfalls

- Storage is per-plugin and **not shared** — two plugins never see each other's keys (that's what the [Event Bus](../event-bus/) is for).
- `StorageInt`/`StorageBool` return `(zero, false)` on missing **or** unparseable values — treat `false` as "not available", not "error".
- `Delete` and `Clear` persist immediately (each call rewrites the file). Batch large mutations into few `Set`s.
