package serverapp

import (
	"database/sql"
	"errors"
	"fmt"

	log "xbot/logger"
	"xbot/storage/sqlite"
	"xbot/tools"
)

// runnerBindingStore persists session→runner bindings.
//
// Authority is tenants.runner_id: a session key "channel:chatID" maps to the
// machine that session runs on. In-memory maps exist only as a cache (see
// tools.SandboxRouter), so a server restart keeps every binding.
type runnerBindingStore struct {
	svc *sqlite.TenantService
}

func newRunnerBindingStore(db *sqlite.DB) *runnerBindingStore {
	return &runnerBindingStore{svc: sqlite.NewTenantService(db)}
}

// SetSessionRunner persists the binding for a session (runnerName == "" unbinds).
func (s *runnerBindingStore) SetSessionRunner(sessionKey, runnerName string) error {
	channelName, chatID := tools.SplitSessionKey(sessionKey)
	if channelName == "" || chatID == "" {
		return fmt.Errorf("invalid session key %q (want \"channel:chatID\")", sessionKey)
	}
	return s.svc.SetTenantRunner(channelName, chatID, runnerName)
}

// GetSessionRunner reads the binding for a session ("" when unbound).
func (s *runnerBindingStore) GetSessionRunner(sessionKey string) (string, error) {
	channelName, chatID := tools.SplitSessionKey(sessionKey)
	if channelName == "" || chatID == "" {
		return "", nil
	}
	name, err := s.svc.GetTenantRunner(channelName, chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return name, nil
}

// wireRunnerBindingStore attaches session→runner binding persistence to the
// global sandbox router. Called wherever the runner database becomes available
// (server core and the web/server entrypoint).
func wireRunnerBindingStore(db *sqlite.DB) {
	if db == nil {
		return
	}
	router, ok := tools.GetSandbox().(*tools.SandboxRouter)
	if !ok || router == nil {
		log.Warn("runner binding store not wired: sandbox router unavailable")
		return
	}
	router.SetBindingStore(newRunnerBindingStore(db))
	log.Info("Session→runner bindings persist in tenants.runner_id")
}
