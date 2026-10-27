package sqlite

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	log "xbot/logger"
	"xbot/storage/common"
)

// sqliteRunnerStore persists the managed runners in the main SQLite database.
//
// One global table, one row per runner name. There is exactly one operator (see
// the v63 multi-user removal), so no ownership dimension exists. This is the
// port of the original tools.RunnerStore SQL into the backend-agnostic storage
// layer; the SQL and its semantics are unchanged.
type sqliteRunnerStore struct {
	db *DB
}

// newSQLiteRunnerStore creates a runner store backed by the given main DB.
func newSQLiteRunnerStore(db *DB) *sqliteRunnerStore {
	return &sqliteRunnerStore{db: db}
}

var _ RunnerStore = (*sqliteRunnerStore)(nil)

// newRunnerToken mints a 256-bit URL-safe token.
//
// Duplicated from tools/runner_store.go on purpose: storage must never import
// tools (tools depends on the storage layer, so the reverse edge is a cycle).
func newRunnerToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate runner token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// Create registers a new runner and returns its freshly minted token.
// Re-creating an existing name rotates the token and updates the settings
// (idempotent upsert) — that is the documented way to re-key a machine.
func (s *sqliteRunnerStore) Create(name, mode, dockerImage, workspace string, llm common.RunnerLLMSettings) (string, error) {
	if name == "" {
		return "", fmt.Errorf("runner name is required")
	}
	mode, err := common.NormalizeRunnerMode(mode)
	if err != nil {
		return "", err
	}
	if mode == common.RunnerModeDocker && dockerImage == "" {
		dockerImage = "ubuntu:22.04"
	}
	if mode != common.RunnerModeDocker {
		dockerImage = ""
	}
	token, err := newRunnerToken()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Conn().Exec(`
		INSERT INTO runners (name, token, mode, docker_image, workspace,
		                     llm_provider, llm_api_key, llm_model, llm_base_url, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			token = excluded.token,
			mode = excluded.mode,
			docker_image = excluded.docker_image,
			workspace = excluded.workspace,
			llm_provider = excluded.llm_provider,
			llm_api_key = excluded.llm_api_key,
			llm_model = excluded.llm_model,
			llm_base_url = excluded.llm_base_url
	`, name, token, mode, dockerImage, workspace,
		llm.Provider, llm.APIKey, llm.Model, llm.BaseURL, now)
	if err != nil {
		return "", fmt.Errorf("create runner: %w", err)
	}
	return token, nil
}

// RotateToken issues a new token for an existing runner.
func (s *sqliteRunnerStore) RotateToken(name string) (string, error) {
	token, err := newRunnerToken()
	if err != nil {
		return "", err
	}
	res, err := s.db.Conn().Exec("UPDATE runners SET token = ? WHERE name = ?", token, name)
	if err != nil {
		return "", fmt.Errorf("rotate runner token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", fmt.Errorf("runner %q not found", name)
	}
	return token, nil
}

// Token returns the current token of a runner (internal use: building the
// connect command). Never expose this through the list API.
func (s *sqliteRunnerStore) Token(name string) (string, error) {
	var token string
	if err := s.db.Conn().QueryRow("SELECT token FROM runners WHERE name = ?", name).Scan(&token); err != nil {
		return "", fmt.Errorf("runner %q not found", name)
	}
	return token, nil
}

// List returns all runners (token excluded).
func (s *sqliteRunnerStore) List() ([]common.RunnerInfo, error) {
	rows, err := s.db.Conn().Query(`
		SELECT name, mode, COALESCE(docker_image,''), COALESCE(workspace,''), COALESCE(created_at,''),
		       COALESCE(llm_provider,''), COALESCE(llm_api_key,''), COALESCE(llm_model,''), COALESCE(llm_base_url,'')
		FROM runners ORDER BY created_at, name`)
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	defer rows.Close()

	var runners []common.RunnerInfo
	for rows.Next() {
		var r common.RunnerInfo
		if err := rows.Scan(&r.Name, &r.Mode, &r.DockerImage, &r.Workspace, &r.CreatedAt,
			&r.LLMProvider, &r.LLMAPIKey, &r.LLMModel, &r.LLMBaseURL); err != nil {
			log.WithError(err).Warn("Failed to scan runner row, skipping")
			continue
		}
		runners = append(runners, r)
	}
	return runners, rows.Err()
}

// Get returns one runner by name.
func (s *sqliteRunnerStore) Get(name string) (*common.RunnerInfo, error) {
	runners, err := s.List()
	if err != nil {
		return nil, err
	}
	for i := range runners {
		if runners[i].Name == name {
			return &runners[i], nil
		}
	}
	return nil, fmt.Errorf("runner %q not found", name)
}

// Delete removes a runner by name.
func (s *sqliteRunnerStore) Delete(name string) error {
	if _, err := s.db.Conn().Exec("DELETE FROM runners WHERE name = ?", name); err != nil {
		return fmt.Errorf("delete runner: %w", err)
	}
	return nil
}

// Rename renames a runner. The new name must be free.
func (s *sqliteRunnerStore) Rename(oldName, newName string) error {
	if oldName == "" || newName == "" {
		return fmt.Errorf("old and new runner names are required")
	}
	if oldName == newName {
		return nil
	}
	tx, err := s.db.Conn().Begin()
	if err != nil {
		return fmt.Errorf("rename runner: begin tx: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM runners WHERE name = ?", oldName).Scan(&count); err != nil {
		return fmt.Errorf("rename runner: check old: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("runner %q not found", oldName)
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM runners WHERE name = ?", newName).Scan(&count); err != nil {
		return fmt.Errorf("rename runner: check new: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("runner name %q already exists", newName)
	}
	if _, err := tx.Exec("UPDATE runners SET name = ? WHERE name = ?", newName, oldName); err != nil {
		return fmt.Errorf("rename runner: update: %w", err)
	}
	// Session bindings are stored as runner *names* (tenants.runner_id) — carry
	// them over so a rename does not silently unbind every session.
	if ok, err := runnerStoreTableExistsTx(tx, "tenants"); err != nil {
		return fmt.Errorf("rename runner: check tenants: %w", err)
	} else if ok {
		if _, err := tx.Exec("UPDATE tenants SET runner_id = ? WHERE runner_id = ?", newName, oldName); err != nil {
			return fmt.Errorf("rename runner: rebind sessions: %w", err)
		}
	}
	return tx.Commit()
}

// UpdateLLM upserts the runner's local-LLM declaration. Creates a placeholder
// row when the runner connected directly without being registered first.
func (s *sqliteRunnerStore) UpdateLLM(name string, llm common.RunnerLLMSettings) error {
	if name == "" {
		return fmt.Errorf("runner name is required")
	}
	_, err := s.db.Conn().Exec(`
		INSERT INTO runners (name, token, mode, workspace, llm_provider, llm_api_key, llm_model, llm_base_url, created_at)
		VALUES (?, '', 'native', '', ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(name) DO UPDATE SET
			llm_provider = excluded.llm_provider,
			llm_api_key = excluded.llm_api_key,
			llm_model = excluded.llm_model,
			llm_base_url = excluded.llm_base_url
	`, name, llm.Provider, llm.APIKey, llm.Model, llm.BaseURL)
	if err != nil {
		return fmt.Errorf("update runner llm: %w", err)
	}
	return nil
}

// FindByToken resolves a runner name from a connect token.
func (s *sqliteRunnerStore) FindByToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var name string
	if err := s.db.Conn().QueryRow("SELECT name FROM runners WHERE token = ?", token).Scan(&name); err != nil {
		return "", false
	}
	return name, true
}

// Validate reports whether the token belongs to any registered runner.
// Constant-time comparison per row to avoid leaking token prefixes.
func (s *sqliteRunnerStore) Validate(token string) bool {
	if token == "" {
		return false
	}
	rows, err := s.db.Conn().Query("SELECT token FROM runners")
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(stored), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

// runnerStoreTableExistsTx is the transaction-scoped table probe used by Rename.
//
// The name is deliberately runner-scoped: package-level `tableExists` (in
// migrations.go) takes a *sql.DB and cannot see a transaction's uncommitted
// state, and a generic `tableExistsTx` risks colliding with sibling features
// landing in this package.
func runnerStoreTableExistsTx(tx *sql.Tx, table string) (bool, error) {
	var name string
	err := tx.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
