package tools

import (
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"

	log "xbot/logger"
)

// RunnerLLMSettings holds the optional local LLM configuration of a runner.
//
// A runner may execute the LLM locally (its own model endpoint) instead of
// proxying back to the server. Only "can this runner do LLM" is needed at the
// server side; the credentials live in the runner's own config, so APIKey is
// informational here.
type RunnerLLMSettings struct {
	Provider string
	APIKey   string
	Model    string
	BaseURL  string
}

// HasLLM reports whether the runner declares LLM capability.
func (l *RunnerLLMSettings) HasLLM() bool {
	return l != nil && l.Provider != ""
}

// RunnerInfo describes one managed remote machine (runner).
//
// Single-operator design: runners are globally scoped — there is no owner
// column and no per-user partitioning. The Token is deliberately NOT part of
// this struct: listing runners must never leak credentials to the UI. Use
// RunnerStore.Token/RotateToken for the explicit accessors.
type RunnerInfo struct {
	Name        string `json:"name"`
	Mode        string `json:"mode"`
	DockerImage string `json:"docker_image"`
	Workspace   string `json:"workspace"`
	CreatedAt   string `json:"created_at"`
	// Online is a read-side projection filled by the caller from the live
	// WebSocket connection registry (never persisted).
	Online bool `json:"online"`
	// Version is reported by the runner at registration (empty when unknown).
	Version string `json:"version,omitempty"`

	LLMProvider string `json:"llm_provider,omitempty"`
	LLMAPIKey   string `json:"llm_api_key,omitempty"`
	LLMModel    string `json:"llm_model,omitempty"`
	LLMBaseURL  string `json:"llm_base_url,omitempty"`
}

// LLMSettings returns the runner's LLM configuration.
func (r RunnerInfo) LLMSettings() RunnerLLMSettings {
	return RunnerLLMSettings{
		Provider: r.LLMProvider,
		APIKey:   r.LLMAPIKey,
		Model:    r.LLMModel,
		BaseURL:  r.LLMBaseURL,
	}
}

// RunnerStore persists the managed runners in SQLite.
//
// One global table, one row per runner name. There is exactly one operator
// (see the v63 multi-user removal), so no ownership dimension exists.
type RunnerStore struct {
	db *sql.DB
}

// NewRunnerStore creates a runner store backed by the given database.
func NewRunnerStore(db *sql.DB) *RunnerStore {
	return &RunnerStore{db: db}
}

// Runner mode values. A runner either executes directly on the host ("native")
// or inside a container it manages itself ("docker"). Anything else is a
// configuration error and is rejected at the boundary — an invalid mode used to
// sit silently in the DB (production had mode='remote') and only surfaced as a
// confusing failure much later.
const (
	RunnerModeNative = "native"
	RunnerModeDocker = "docker"
)

// NormalizeRunnerMode validates and canonicalizes a runner mode.
// Empty means native. Unknown values are rejected.
func NormalizeRunnerMode(mode string) (string, error) {
	switch mode {
	case "":
		return RunnerModeNative, nil
	case RunnerModeNative, RunnerModeDocker:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid runner mode %q (valid: %s, %s)", mode, RunnerModeNative, RunnerModeDocker)
	}
}

// newRunnerToken mints a 256-bit URL-safe token.
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
func (s *RunnerStore) Create(name, mode, dockerImage, workspace string, llm RunnerLLMSettings) (string, error) {
	if name == "" {
		return "", fmt.Errorf("runner name is required")
	}
	mode, err := NormalizeRunnerMode(mode)
	if err != nil {
		return "", err
	}
	if mode == RunnerModeDocker && dockerImage == "" {
		dockerImage = "ubuntu:22.04"
	}
	if mode != RunnerModeDocker {
		dockerImage = ""
	}
	token, err := newRunnerToken()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = s.db.Exec(`
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
func (s *RunnerStore) RotateToken(name string) (string, error) {
	token, err := newRunnerToken()
	if err != nil {
		return "", err
	}
	res, err := s.db.Exec("UPDATE runners SET token = ? WHERE name = ?", token, name)
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
func (s *RunnerStore) Token(name string) (string, error) {
	var token string
	if err := s.db.QueryRow("SELECT token FROM runners WHERE name = ?", name).Scan(&token); err != nil {
		return "", fmt.Errorf("runner %q not found", name)
	}
	return token, nil
}

// List returns all runners (token excluded).
func (s *RunnerStore) List() ([]RunnerInfo, error) {
	rows, err := s.db.Query(`
		SELECT name, mode, COALESCE(docker_image,''), COALESCE(workspace,''), COALESCE(created_at,''),
		       COALESCE(llm_provider,''), COALESCE(llm_api_key,''), COALESCE(llm_model,''), COALESCE(llm_base_url,'')
		FROM runners ORDER BY created_at, name`)
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	defer rows.Close()

	var runners []RunnerInfo
	for rows.Next() {
		var r RunnerInfo
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
func (s *RunnerStore) Get(name string) (*RunnerInfo, error) {
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
func (s *RunnerStore) Delete(name string) error {
	if _, err := s.db.Exec("DELETE FROM runners WHERE name = ?", name); err != nil {
		return fmt.Errorf("delete runner: %w", err)
	}
	return nil
}

// Rename renames a runner. The new name must be free.
func (s *RunnerStore) Rename(oldName, newName string) error {
	if oldName == "" || newName == "" {
		return fmt.Errorf("old and new runner names are required")
	}
	if oldName == newName {
		return nil
	}
	tx, err := s.db.Begin()
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
	if ok, err := tableExistsTx(tx, "tenants"); err != nil {
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
func (s *RunnerStore) UpdateLLM(name string, llm RunnerLLMSettings) error {
	if name == "" {
		return fmt.Errorf("runner name is required")
	}
	_, err := s.db.Exec(`
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
func (s *RunnerStore) FindByToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var name string
	if err := s.db.QueryRow("SELECT name FROM runners WHERE token = ?", token).Scan(&name); err != nil {
		return "", false
	}
	return name, true
}

// Validate reports whether the token belongs to any registered runner.
// Constant-time comparison per row to avoid leaking token prefixes.
func (s *RunnerStore) Validate(token string) bool {
	if token == "" {
		return false
	}
	rows, err := s.db.Query("SELECT token FROM runners")
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

// ListAllRunners returns every runner with live online status populated from
// the shared sandbox router. Token is never included.
func ListAllRunners() ([]RunnerInfo, error) {
	db := GetRunnerTokenDB()
	if db == nil {
		return nil, fmt.Errorf("runner management not configured")
	}
	runners, err := NewRunnerStore(db).List()
	if err != nil {
		return nil, err
	}
	PopulateRunnerOnlineStatus(runners)
	return runners, nil
}

// PopulateRunnerOnlineStatus fills Online (and Version) from the live connection
// registry. Shared by the RPC layer and the agent tool layer.
func PopulateRunnerOnlineStatus(runners []RunnerInfo) {
	router, ok := GetSandbox().(*SandboxRouter)
	if !ok || router == nil {
		return
	}
	for i := range runners {
		runners[i].Online = router.IsRunnerOnline(runners[i].Name)
		if v := router.RunnerVersion(runners[i].Name); v != "" {
			runners[i].Version = v
		}
	}
}

// tableExistsTx is the transaction-scoped variant of tableExists.
func tableExistsTx(tx *sql.Tx, table string) (bool, error) {
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
