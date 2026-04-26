package sqlite

import (
	"fmt"

	log "xbot/logger"
)

// createSchema creates the initial database schema (v2 baseline).
// After creation, migrateSchema is called to bring it to the current version.
func (db *DB) createSchema() error {
	schema := schemaCore + schemaMemory + schemaEvents + schemaUsers + schemaRunners + schemaShared + schemaJobs + `
CREATE TABLE schema_version (
    version INTEGER PRIMARY KEY
);
INSERT INTO schema_version (version) VALUES (30);
`
	if _, err := db.Conn().Exec(schema); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}
	log.Info("Database schema initialized (v2)")
	return nil
}
