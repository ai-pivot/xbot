package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// resetMigrationRegistry clears the global registry and returns a cleanup func.
// This is necessary because tests share the package-level state.
func resetMigrationRegistry() func() {
	migrationMu.Lock()
	defer migrationMu.Unlock()
	backup := migrationRegistry
	migrationRegistry = make(map[string][]PluginMigration)
	return func() {
		migrationMu.Lock()
		defer migrationMu.Unlock()
		migrationRegistry = backup
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_RegisterAndRun
// ---------------------------------------------------------------------------

func TestPluginMigration_RegisterAndRun(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.basic"
	storage := newMapStorage()

	// Pre-populate some data that the migration will transform
	storage.Set("config", `{"theme":"dark"}`)

	var migrated bool
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			migrated = true
			// Transform: wrap config in a versioned envelope
			old, _ := s.Get("config")
			s.Set("config", fmt.Sprintf(`{"version":"1.1.0","data":%s}`, old))
			return nil
		},
	})

	err := RunMigrations(context.Background(), pluginID, "1.1.0", storage)
	if err != nil {
		t.Fatalf("RunMigrations() error: %v", err)
	}
	if !migrated {
		t.Fatal("migration was not executed")
	}

	// Verify data was transformed
	config, ok := storage.Get("config")
	if !ok {
		t.Fatal("config key missing after migration")
	}
	expected := `{"version":"1.1.0","data":{"theme":"dark"}}`
	if config != expected {
		t.Errorf("config = %q, want %q", config, expected)
	}

	// Verify migration record
	raw, ok := storage.Get(migrationKey)
	if !ok {
		t.Fatal("migration record not saved")
	}
	var rec migrationRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("parse migration record: %v", err)
	}
	if len(rec.Applied) != 1 || rec.Applied[0] != "1.0.0→1.1.0" {
		t.Errorf("applied = %v, want [1.0.0→1.1.0]", rec.Applied)
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_Ordering
// ---------------------------------------------------------------------------

func TestPluginMigration_Ordering(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.ordering"
	storage := newMapStorage()

	var order []string
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			order = append(order, "1.0.0→1.1.0")
			s.Set("v1.1", "done")
			return nil
		},
	})
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.1.0",
		ToVersion:   "1.2.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			order = append(order, "1.1.0→1.2.0")
			s.Set("v1.2", "done")
			return nil
		},
	})
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.2.0",
		ToVersion:   "2.0.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			order = append(order, "1.2.0→2.0.0")
			s.Set("v2.0", "done")
			return nil
		},
	})

	// Register out of order to verify sorting
	// (already done above: 1.0→1.1, 1.1→1.2, 1.2→2.0)

	err := RunMigrations(context.Background(), pluginID, "2.0.0", storage)
	if err != nil {
		t.Fatalf("RunMigrations() error: %v", err)
	}

	if len(order) != 3 {
		t.Fatalf("executed %d migrations, want 3", len(order))
	}
	if order[0] != "1.0.0→1.1.0" || order[1] != "1.1.0→1.2.0" || order[2] != "1.2.0→2.0.0" {
		t.Errorf("execution order = %v, want [1.0.0→1.1.0, 1.1.0→1.2.0, 1.2.0→2.0.0]", order)
	}

	// All intermediate versions should be recorded
	raw, _ := storage.Get(migrationKey)
	var rec migrationRecord
	json.Unmarshal([]byte(raw), &rec)
	if len(rec.Applied) != 3 {
		t.Errorf("applied count = %d, want 3", len(rec.Applied))
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_RollbackOnFailure
// ---------------------------------------------------------------------------

func TestPluginMigration_RollbackOnFailure(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.rollback"
	storage := newMapStorage()

	// Pre-populate data
	storage.Set("users", `["alice","bob"]`)
	storage.Set("settings", `{"lang":"en"}`)

	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			// Partially transform, then fail
			s.Set("users", `["alice","bob","charlie"]`)
			s.Delete("settings")
			return fmt.Errorf("simulated migration error")
		},
	})

	err := RunMigrations(context.Background(), pluginID, "1.1.0", storage)
	if err == nil {
		t.Fatal("RunMigrations() should have returned an error")
	}

	// Verify rollback: original data should be intact
	users, ok := storage.Get("users")
	if !ok {
		t.Fatal("users key missing after rollback")
	}
	if users != `["alice","bob"]` {
		t.Errorf("users = %q, want original [\"alice\",\"bob\"]", users)
	}

	settings, ok := storage.Get("settings")
	if !ok {
		t.Fatal("settings key missing after rollback")
	}
	if settings != `{"lang":"en"}` {
		t.Errorf("settings = %q, want original {\"lang\":\"en\"}", settings)
	}

	// Migration record should not exist (failed migration was never recorded)
	if raw, ok := storage.Get(migrationKey); ok {
		t.Errorf("migration record should not exist after failed migration, got %q", raw)
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_AlreadyApplied
// ---------------------------------------------------------------------------

func TestPluginMigration_AlreadyApplied(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.skip"
	storage := newMapStorage()

	var callCount int
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			callCount++
			return nil
		},
	})
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.1.0",
		ToVersion:   "1.2.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			callCount++
			return nil
		},
	})

	// First run: both migrations should execute
	err := RunMigrations(context.Background(), pluginID, "1.2.0", storage)
	if err != nil {
		t.Fatalf("first RunMigrations() error: %v", err)
	}
	if callCount != 2 {
		t.Fatalf("first run: callCount = %d, want 2", callCount)
	}

	// Second run: no migrations should execute (already applied)
	callCount = 0
	err = RunMigrations(context.Background(), pluginID, "1.2.0", storage)
	if err != nil {
		t.Fatalf("second RunMigrations() error: %v", err)
	}
	if callCount != 0 {
		t.Errorf("second run: callCount = %d, want 0 (already applied)", callCount)
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_FutureMigrationSkipped
// ---------------------------------------------------------------------------

func TestPluginMigration_FutureMigrationSkipped(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.future"
	storage := newMapStorage()

	var executed bool
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.1.0",
		ToVersion:   "1.2.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			executed = true
			return nil
		},
	})

	// currentVersion is 1.1.0, but migration goes to 1.2.0 — should skip
	err := RunMigrations(context.Background(), pluginID, "1.1.0", storage)
	if err != nil {
		t.Fatalf("RunMigrations() error: %v", err)
	}
	if executed {
		t.Error("future migration should not have been executed")
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_ChainContinuity
// ---------------------------------------------------------------------------

func TestPluginMigration_ChainContinuity(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.chain"
	storage := newMapStorage()

	var executed []string
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			executed = append(executed, "1.0.0→1.1.0")
			return nil
		},
	})
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.1.0",
		ToVersion:   "1.2.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			executed = append(executed, "1.1.0→1.2.0")
			return nil
		},
	})
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.2.0",
		ToVersion:   "1.3.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			executed = append(executed, "1.2.0→1.3.0")
			return nil
		},
	})

	// Run up to 1.2.0 — only first two migrations should execute
	err := RunMigrations(context.Background(), pluginID, "1.2.0", storage)
	if err != nil {
		t.Fatalf("RunMigrations() error: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("executed %d migrations, want 2", len(executed))
	}

	// Now upgrade to 1.3.0 — only the third migration should run
	executed = nil
	err = RunMigrations(context.Background(), pluginID, "1.3.0", storage)
	if err != nil {
		t.Fatalf("RunMigrations() error: %v", err)
	}
	if len(executed) != 1 {
		t.Fatalf("executed %d migrations, want 1", len(executed))
	}
	if executed[0] != "1.2.0→1.3.0" {
		t.Errorf("executed = %v, want [1.2.0→1.3.0]", executed)
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_ContextCancellation
// ---------------------------------------------------------------------------

func TestPluginMigration_ContextCancellation(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.cancel"
	storage := newMapStorage()

	started := make(chan struct{})
	RegisterMigration(pluginID, PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})

	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	var runErr error
	go func() {
		defer wg.Done()
		runErr = RunMigrations(ctx, pluginID, "1.1.0", storage)
	}()

	<-started
	cancel()
	wg.Wait()

	if runErr == nil {
		t.Fatal("RunMigrations() should return error on context cancellation")
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_ConcurrentRegistration
// ---------------------------------------------------------------------------

func TestPluginMigration_ConcurrentRegistration(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.concurrent"

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			RegisterMigration(pluginID, PluginMigration{
				FromVersion: "1.0.0",
				ToVersion:   fmt.Sprintf("1.0.%d", n),
				Migrate: func(ctx context.Context, s StorageAccessor) error {
					return nil
				},
			})
		}(i)
	}
	wg.Wait()

	migs := getMigrations(pluginID)
	if len(migs) != 100 {
		t.Errorf("registered %d migrations, want 100", len(migs))
	}
}

// ---------------------------------------------------------------------------
// TestPluginMigration_DuplicateRegistration
// ---------------------------------------------------------------------------

func TestPluginMigration_DuplicateRegistration(t *testing.T) {
	cleanup := resetMigrationRegistry()
	defer cleanup()

	pluginID := "test.migration.dup"
	storage := newMapStorage()

	var callCount int
	mig := PluginMigration{
		FromVersion: "1.0.0",
		ToVersion:   "1.1.0",
		Migrate: func(ctx context.Context, s StorageAccessor) error {
			callCount++
			return nil
		},
	}
	RegisterMigration(pluginID, mig)
	RegisterMigration(pluginID, mig) // duplicate

	err := RunMigrations(context.Background(), pluginID, "1.1.0", storage)
	if err != nil {
		t.Fatalf("RunMigrations() error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("callCount = %d, want 1 (duplicate should not re-execute)", callCount)
	}
}
