---
title: "Plugin Migration"
weight: 19
---

xbot provides a versioned data-migration system for plugins (`plugin/migration.go`). Plugins register migration steps; `RunMigrations` executes the pending ones and transforms the plugin's storage data from an old schema version to a newer one.

## Architecture

- A global, mutex-protected registry (`migrationRegistry`) maps `pluginID → []PluginMigration`.
- `RegisterMigration` accumulates steps per plugin; execution order is decided at run time by semver comparison, not by registration order.
- Applied migrations are tracked in the plugin's own storage under the reserved key `_migrations` (a `migrationRecord` with an `applied` list of migration IDs).
- Each migration runs in a transaction-like pattern: **backup → execute → record**, and restores the backup on failure.

## PluginMigration

A single version-to-version step:

```go
type PluginMigration struct {
    FromVersion string
    ToVersion   string
    Migrate     func(ctx context.Context, storage StorageAccessor) error
}
```

- `FromVersion` and `ToVersion` must be strict semver strings (e.g. `"1.0.0"`).
- `Migrate` receives the plugin's `StorageAccessor` and should transform data from the `FromVersion` schema to the `ToVersion` schema.
- The canonical migration ID is `FromVersion→ToVersion` (e.g. `"1.0.0→1.1.0"`).

## Registering Migrations

`RegisterMigration` is safe to call from `init()` or `Activate`, and from multiple goroutines:

```go
plugin.RegisterMigration("my-plugin", plugin.PluginMigration{
    FromVersion: "1.0.0",
    ToVersion:   "1.1.0",
    Migrate: func(ctx context.Context, s plugin.StorageAccessor) error {
        old, _ := s.Get("config")
        s.Set("config", fmt.Sprintf(`{"version":"1.1.0","data":%s}`, old))
        return nil
    },
})
```

Registering the same migration twice is harmless — it executes only once (the applied-record check deduplicates).

## Running Migrations

```go
func RunMigrations(ctx context.Context, pluginID string, currentVersion string, storage StorageAccessor) error
```

- `pluginID`: the plugin's unique identifier.
- `currentVersion`: the version you are migrating TO (typically the manifest version).
- `storage`: the plugin's storage accessor.

`RunMigrations` returns `nil` when all applicable migrations are applied, or an error if a migration fails (after rolling back) or the context is cancelled.

### Execution Semantics

1. All registered migrations are validated: `FromVersion` and `ToVersion` must parse as semver (otherwise an error is returned).
2. Migrations are sorted by `FromVersion`, then `ToVersion`.
3. The applied record is loaded from the `_migrations` storage key; the "last migrated version" is the `ToVersion` of the last applied migration.
4. Each migration in sorted order is considered:
   - **Already applied** → skipped.
   - **Chain continuity** — if a last-applied version exists, only the migration whose `FromVersion` equals it is eligible; any other migration is skipped. Migrations therefore form a chain `1.0.0 → 1.1.0 → 1.2.0`.
   - **Future migration** — `ToVersion` greater than `currentVersion` → skipped.
   - `FromVersion > ToVersion` → error.
5. The context is checked for cancellation before each migration executes.

### Backup and Rollback

Before a migration runs, `backupStorage` reads every storage key (except the reserved `_migrations` key) into an in-memory map. If `Migrate` returns an error, `restoreStorage` deletes all non-reserved keys and restores the backup — the storage is left exactly as it was. If the migration succeeds but saving the record fails, the same rollback is attempted, and the returned error reports both failures.

## Writing a Migration Chain

```go
plugin.RegisterMigration("my-plugin", /* 1.0.0 → 1.1.0 */ ...)
plugin.RegisterMigration("my-plugin", /* 1.1.0 → 1.2.0 */ ...)
plugin.RegisterMigration("my-plugin", /* 1.2.0 → 2.0.0 */ ...)

// Upgrade to 2.0.0 — all three steps run in order.
err := plugin.RunMigrations(ctx, "my-plugin", "2.0.0", storage)

// Upgrade to 1.2.0 first — only the first two steps run.
// A later call with "2.0.0" runs only 1.2.0 → 2.0.0.
```

Plugins typically call `RunMigrations` from their `Activate` implementation, before reading any data.

## Behaviors

The following behaviors are covered by `plugin/migration_test.go`:

| Behavior | Detail |
|----------|--------|
| Ordering | Migrations execute sorted by semver regardless of registration order |
| Idempotency | A second run executes nothing — already-applied migrations never re-run |
| Chain continuity | A migration whose `FromVersion` does not match the last applied `ToVersion` is skipped |
| Future versions | Migrations with `ToVersion` above `currentVersion` are skipped |
| Rollback on failure | Original data stays intact; the failed migration is never recorded |
| Cancellation | A cancelled context aborts between migrations |
| Concurrent registration | `RegisterMigration` from many goroutines is safe |
| Duplicate registration | The same migration registered twice executes once |

## See Also

- [Plugin Storage](./storage/) — the `StorageAccessor` API migrations operate on
- [Plugin Lifecycle](./lifecycle/) — where to invoke `RunMigrations`
- [Hot Reload & Monitoring](./hot-reload/) — reload behavior that pairs with migrations
