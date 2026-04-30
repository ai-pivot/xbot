package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	log "xbot/logger"
)

// ---------------------------------------------------------------------------
// Plugin Migration — versioned storage data migrations
// ---------------------------------------------------------------------------

// migrationKey is the storage key used to persist applied migration records.
const migrationKey = "_migrations"

// migrationRecord tracks which migrations have been applied to a plugin's storage.
type migrationRecord struct {
	Applied []string `json:"applied"`
}

// PluginMigration defines a single version-to-version migration step.
//
// FromVersion and ToVersion must be strict semver strings (e.g., "1.0.0").
// The Migrate function receives the plugin's storage and should transform
// data from FromVersion's schema to ToVersion's schema.
type PluginMigration struct {
	FromVersion string
	ToVersion   string
	Migrate     func(ctx context.Context, storage StorageAccessor) error
}

// migrationID returns the canonical identifier for a migration step.
// Format: "1.0.0→1.1.0"
func (m PluginMigration) migrationID() string {
	return m.FromVersion + "→" + m.ToVersion
}

// ---------------------------------------------------------------------------
// Global migration registry
// ---------------------------------------------------------------------------

// migrationRegistry holds all registered migrations, keyed by pluginID.
// Each pluginID maps to an ordered list of migrations.
var (
	migrationRegistry = make(map[string][]PluginMigration)
	migrationMu       sync.Mutex
)

// RegisterMigration adds a migration step to the global registry for the given plugin.
// This function is safe to call during plugin registration (init() or Activate).
//
// Migrations for the same plugin are accumulated; ordering is determined at
// execution time by RunMigrations using semver comparison.
func RegisterMigration(pluginID string, migration PluginMigration) {
	migrationMu.Lock()
	defer migrationMu.Unlock()

	migrationRegistry[pluginID] = append(migrationRegistry[pluginID], migration)
}

// getMigrations returns a copy of the registered migrations for a plugin.
func getMigrations(pluginID string) []PluginMigration {
	migrationMu.Lock()
	defer migrationMu.Unlock()

	migs := migrationRegistry[pluginID]
	result := make([]PluginMigration, len(migs))
	copy(result, migs)
	return result
}

// ---------------------------------------------------------------------------
// Migration record persistence
// ---------------------------------------------------------------------------

// loadMigrationRecord reads the applied migration list from storage.
func loadMigrationRecord(storage StorageAccessor) migrationRecord {
	raw, ok := storage.Get(migrationKey)
	if !ok {
		return migrationRecord{Applied: []string{}}
	}
	var rec migrationRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		log.WithField("key", migrationKey).Warn("Failed to parse migration record, starting fresh: ", err)
		return migrationRecord{Applied: []string{}}
	}
	return rec
}

// saveMigrationRecord persists the applied migration list to storage.
func saveMigrationRecord(storage StorageAccessor, rec migrationRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("marshal migration record: %w", err)
	}
	return storage.Set(migrationKey, string(data))
}

// ---------------------------------------------------------------------------
// Storage backup / restore
// ---------------------------------------------------------------------------

// backupStorage reads all key-value pairs from storage into an in-memory map.
// The migrationKey is excluded from backup since it is managed separately.
func backupStorage(storage StorageAccessor) (map[string]string, error) {
	keys := storage.Keys()
	backup := make(map[string]string, len(keys))
	for _, k := range keys {
		if k == migrationKey {
			continue
		}
		v, ok := storage.Get(k)
		if !ok {
			continue
		}
		backup[k] = v
	}
	return backup, nil
}

// restoreStorage clears all data (except migrationKey) and restores from backup.
func restoreStorage(storage StorageAccessor, backup map[string]string) error {
	// Remove everything except the migration record
	keys := storage.Keys()
	for _, k := range keys {
		if k == migrationKey {
			continue
		}
		if err := storage.Delete(k); err != nil {
			return fmt.Errorf("restore: delete key %q: %w", k, err)
		}
	}
	// Restore backup
	for k, v := range backup {
		if err := storage.Set(k, v); err != nil {
			return fmt.Errorf("restore: set key %q: %w", k, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Migration execution
// ---------------------------------------------------------------------------

// RunMigrations executes all pending migrations for a plugin.
//
// Parameters:
//   - ctx: context for cancellation
//   - pluginID: unique plugin identifier
//   - currentVersion: the plugin version we are migrating TO (manifest.Version)
//   - storage: the plugin's storage accessor
//
// The function determines the "last migrated version" by inspecting applied
// migration records. If no migrations have been applied, the starting version
// is unknown and all migrations with ToVersion <= currentVersion are candidates.
// Otherwise, only migrations whose FromVersion matches the last migration's
// ToVersion (and whose ToVersion <= currentVersion) are executed, forming a chain.
//
// Each migration runs in a transaction-like pattern:
// backup → execute → update record → on error: restore backup.
//
// Returns nil if all applicable migrations are applied successfully.
func RunMigrations(ctx context.Context, pluginID string, currentVersion string, storage StorageAccessor) error {
	migrations := getMigrations(pluginID)
	if len(migrations) == 0 {
		return nil
	}

	curMajor, curMinor, curPatch, err := parseSemver(currentVersion)
	if err != nil {
		return fmt.Errorf("parse current version %q: %w", currentVersion, err)
	}
	curVer := versionTuple{curMajor, curMinor, curPatch}

	rec := loadMigrationRecord(storage)
	appliedSet := make(map[string]bool, len(rec.Applied))
	for _, id := range rec.Applied {
		appliedSet[id] = true
	}

	// Validate all migration versions are valid semver strings.
	for _, m := range migrations {
		if _, _, _, err := parseSemver(m.FromVersion); err != nil {
			return fmt.Errorf("migration %s: invalid FromVersion: %w", m.migrationID(), err)
		}
		if _, _, _, err := parseSemver(m.ToVersion); err != nil {
			return fmt.Errorf("migration %s: invalid ToVersion: %w", m.migrationID(), err)
		}
	}

	// Sort migrations by FromVersion then ToVersion
	sort.Slice(migrations, func(i, j int) bool {
		mi := parseVersionTuple(migrations[i].FromVersion)
		mj := parseVersionTuple(migrations[j].FromVersion)
		if mi != mj {
			return mi.lessThan(mj)
		}
		return parseVersionTuple(migrations[i].ToVersion).lessThan(
			parseVersionTuple(migrations[j].ToVersion))
	})

	// Determine the effective starting version from applied records.
	// We look at the ToVersion of the last applied migration in sorted order.
	var lastAppliedTo string
	for _, m := range migrations {
		if appliedSet[m.migrationID()] {
			lastAppliedTo = m.ToVersion
		}
	}

	var lastTo versionTuple
	if lastAppliedTo != "" {
		lastTo = parseVersionTuple(lastAppliedTo)
	}

	executed := 0
	for _, m := range migrations {
		select {
		case <-ctx.Done():
			return fmt.Errorf("migration cancelled: %w", ctx.Err())
		default:
		}

		id := m.migrationID()
		if appliedSet[id] {
			continue // already applied
		}

		fromVer := parseVersionTuple(m.FromVersion)
		toVer := parseVersionTuple(m.ToVersion)

		// Validate chain continuity: if we have a last applied version,
		// the next migration's FromVersion must match it.
		if lastAppliedTo != "" && fromVer != lastTo {
			continue // skip — not the next in chain
		}

		// Validate target: ToVersion must be <= currentVersion
		if toVer.greaterThan(curVer) {
			continue // skip — future migration
		}

		// Validate from <= to
		if fromVer.greaterThan(toVer) {
			return fmt.Errorf("migration %s: FromVersion must be <= ToVersion", id)
		}

		log.WithField("plugin", pluginID).
			WithField("migration", id).
			Info("Running plugin migration")

		// Transaction: backup → execute → record
		backup, err := backupStorage(storage)
		if err != nil {
			return fmt.Errorf("migration %s: backup failed: %w", id, err)
		}

		if err := m.Migrate(ctx, storage); err != nil {
			log.WithField("plugin", pluginID).
				WithField("migration", id).
				WithField("error", err).
				Error("Migration failed, rolling back")
			if rollbackErr := restoreStorage(storage, backup); rollbackErr != nil {
				log.WithField("plugin", pluginID).
					WithField("migration", id).
					WithField("error", rollbackErr).
					Error("Rollback failed — storage may be inconsistent")
				return fmt.Errorf("migration %s failed: %v; rollback also failed: %w", id, err, rollbackErr)
			}
			return fmt.Errorf("migration %s failed (rolled back): %w", id, err)
		}

		// Record success
		rec.Applied = append(rec.Applied, id)
		if err := saveMigrationRecord(storage, rec); err != nil {
			// Migration succeeded but we can't record it. Attempt rollback.
			log.WithField("plugin", pluginID).
				WithField("migration", id).
				WithField("error", err).
				Error("Failed to save migration record, rolling back")
			// Remove from rec since we'll restore the backup
			rec.Applied = rec.Applied[:len(rec.Applied)-1]
			if rollbackErr := restoreStorage(storage, backup); rollbackErr != nil {
				return fmt.Errorf("migration %s succeeded but record save failed: %v; rollback also failed: %w",
					id, err, rollbackErr)
			}
			return fmt.Errorf("migration %s succeeded but record save failed (rolled back): %w", id, err)
		}

		appliedSet[id] = true
		lastAppliedTo = m.ToVersion
		lastTo = toVer
		executed++
	}

	if executed > 0 {
		log.WithField("plugin", pluginID).
			WithField("count", executed).
			Info("Plugin migrations completed")
	}

	return nil
}

// ---------------------------------------------------------------------------
// versionTuple — comparable semver triple for ordering
// ---------------------------------------------------------------------------

type versionTuple [3]int

func parseVersionTuple(v string) versionTuple {
	major, minor, patch, _ := parseSemver(v)
	return versionTuple{major, minor, patch}
}

func (v versionTuple) lessThan(other versionTuple) bool {
	for i := 0; i < 3; i++ {
		if v[i] != other[i] {
			return v[i] < other[i]
		}
	}
	return false
}

func (v versionTuple) greaterThan(other versionTuple) bool {
	return other.lessThan(v)
}
