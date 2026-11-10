package pg

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	log "xbot/logger"
	"xbot/storage/common"
)

// RunnerStore is the backend-neutral managed-runner registry contract.
//
// runnerStore implements RunnerStore on PostgreSQL.
//
// The semantics mirror sqliteRunnerStore (and the original tools.RunnerStore)
// exactly: re-creating a name is an idempotent upsert that rotates the token,
// listing never exposes tokens, and Rename rebinds tenants.runner_id in the
// same transaction so sessions do not silently lose their machine.
type runnerStore struct {
	pool *pgxpool.Pool
}

func newRunnerStore(pool *pgxpool.Pool) *runnerStore {
	return &runnerStore{pool: pool}
}

var _ RunnerStore = (*runnerStore)(nil)

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
func (s *runnerStore) Create(name, mode, dockerImage, workspace string, llm common.RunnerLLMSettings) (string, error) {
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
	now := time.Now().UTC()
	// created_at is intentionally not updated on conflict: it records when the
	// machine was first registered, matching the SQLite implementation.
	_, err = s.pool.Exec(context.Background(), `
		INSERT INTO runners (name, token, mode, docker_image, workspace,
		                     llm_provider, llm_api_key, llm_model, llm_base_url, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (name) DO UPDATE SET
			token = EXCLUDED.token,
			mode = EXCLUDED.mode,
			docker_image = EXCLUDED.docker_image,
			workspace = EXCLUDED.workspace,
			llm_provider = EXCLUDED.llm_provider,
			llm_api_key = EXCLUDED.llm_api_key,
			llm_model = EXCLUDED.llm_model,
			llm_base_url = EXCLUDED.llm_base_url
	`, name, token, mode, dockerImage, workspace,
		llm.Provider, llm.APIKey, llm.Model, llm.BaseURL, now)
	if err != nil {
		return "", fmt.Errorf("create runner: %w", err)
	}
	return token, nil
}

// RotateToken issues a new token for an existing runner.
func (s *runnerStore) RotateToken(name string) (string, error) {
	token, err := newRunnerToken()
	if err != nil {
		return "", err
	}
	tag, err := s.pool.Exec(context.Background(),
		"UPDATE runners SET token = $1 WHERE name = $2", token, name)
	if err != nil {
		return "", fmt.Errorf("rotate runner token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return "", fmt.Errorf("runner %q not found", name)
	}
	return token, nil
}

// Token returns the current token of a runner (internal use: building the
// connect command). Never expose this through the list API.
func (s *runnerStore) Token(name string) (string, error) {
	var token string
	err := s.pool.QueryRow(context.Background(),
		"SELECT token FROM runners WHERE name = $1", name).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("runner %q not found", name)
	}
	if err != nil {
		return "", fmt.Errorf("query runner %q token: %w", name, err)
	}
	return token, nil
}

// List returns all runners (token excluded).
func (s *runnerStore) List() ([]common.RunnerInfo, error) {
	rows, err := s.pool.Query(context.Background(), `
		SELECT name, mode, COALESCE(docker_image,''), COALESCE(workspace,''), created_at,
		       COALESCE(llm_provider,''), COALESCE(llm_api_key,''), COALESCE(llm_model,''), COALESCE(llm_base_url,'')
		FROM runners ORDER BY created_at, name`)
	if err != nil {
		return nil, fmt.Errorf("list runners: %w", err)
	}
	defer rows.Close()

	var runners []common.RunnerInfo
	for rows.Next() {
		var r common.RunnerInfo
		var createdAt time.Time
		if err := rows.Scan(&r.Name, &r.Mode, &r.DockerImage, &r.Workspace, &createdAt,
			&r.LLMProvider, &r.LLMAPIKey, &r.LLMModel, &r.LLMBaseURL); err != nil {
			log.WithError(err).Warn("Failed to scan runner row, skipping")
			continue
		}
		// The contract exposes created_at as an RFC3339 string (that is what the
		// SQLite backend stores); PG keeps it native TIMESTAMPTZ and formats here.
		r.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		runners = append(runners, r)
	}
	return runners, rows.Err()
}

// Get returns one runner by name.
func (s *runnerStore) Get(name string) (*common.RunnerInfo, error) {
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
func (s *runnerStore) Delete(name string) error {
	if _, err := s.pool.Exec(context.Background(),
		"DELETE FROM runners WHERE name = $1", name); err != nil {
		return fmt.Errorf("delete runner: %w", err)
	}
	return nil
}

// Rename renames a runner. The new name must be free.
func (s *runnerStore) Rename(oldName, newName string) error {
	if oldName == "" || newName == "" {
		return fmt.Errorf("old and new runner names are required")
	}
	if oldName == newName {
		return nil
	}
	ctx := context.Background()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rename runner: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var count int
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM runners WHERE name = $1", oldName).Scan(&count); err != nil {
		return fmt.Errorf("rename runner: check old: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("runner %q not found", oldName)
	}
	if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM runners WHERE name = $1", newName).Scan(&count); err != nil {
		return fmt.Errorf("rename runner: check new: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("runner name %q already exists", newName)
	}
	if _, err := tx.Exec(ctx, "UPDATE runners SET name = $1 WHERE name = $2", newName, oldName); err != nil {
		return fmt.Errorf("rename runner: update: %w", err)
	}
	// Session bindings are stored as runner *names* (tenants.runner_id) — carry
	// them over so a rename does not silently unbind every session. The tenants
	// table is part of the PG schema (initSchema always creates it), so no
	// existence probe is needed here.
	if _, err := tx.Exec(ctx, "UPDATE tenants SET runner_id = $1 WHERE runner_id = $2", newName, oldName); err != nil {
		return fmt.Errorf("rename runner: rebind sessions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("rename runner: commit: %w", err)
	}
	return nil
}

// UpdateLLM upserts the runner's local-LLM declaration. Creates a placeholder
// row when the runner connected directly without being registered first.
func (s *runnerStore) UpdateLLM(name string, llm common.RunnerLLMSettings) error {
	if name == "" {
		return fmt.Errorf("runner name is required")
	}
	// created_at is left to the column DEFAULT (now()), matching the SQLite
	// implementation's datetime('now') placeholder.
	_, err := s.pool.Exec(context.Background(), `
		INSERT INTO runners (name, token, mode, workspace, llm_provider, llm_api_key, llm_model, llm_base_url)
		VALUES ($1, '', 'native', '', $2, $3, $4, $5)
		ON CONFLICT (name) DO UPDATE SET
			llm_provider = EXCLUDED.llm_provider,
			llm_api_key = EXCLUDED.llm_api_key,
			llm_model = EXCLUDED.llm_model,
			llm_base_url = EXCLUDED.llm_base_url
	`, name, llm.Provider, llm.APIKey, llm.Model, llm.BaseURL)
	if err != nil {
		return fmt.Errorf("update runner llm: %w", err)
	}
	return nil
}

// FindByToken resolves a runner name from a connect token.
func (s *runnerStore) FindByToken(token string) (string, bool) {
	if token == "" {
		return "", false
	}
	var name string
	err := s.pool.QueryRow(context.Background(),
		"SELECT name FROM runners WHERE token = $1", token).Scan(&name)
	if err != nil {
		return "", false
	}
	return name, true
}

// Validate reports whether the token belongs to any registered runner.
// Constant-time comparison per row to avoid leaking token prefixes.
func (s *runnerStore) Validate(token string) bool {
	if token == "" {
		return false
	}
	rows, err := s.pool.Query(context.Background(), "SELECT token FROM runners")
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
