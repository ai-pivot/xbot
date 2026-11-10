package serverapp

import (
	"fmt"

	log "xbot/logger"
	"xbot/storage"
	"xbot/tools"
)

// runnerBindingStore persists session→runner bindings.
//
// Authority is tenants.runner_id: a session key "channel:chatID" maps to the
// machine that session runs on. In-memory maps exist only as a cache (see
// tools.SandboxRouter), so a server restart keeps every binding.
//
// It depends on storage.TenantStore (not *sqlite.TenantService) so the same
// wiring works on the PG backend, where there is no SQLite handle.
type runnerBindingStore struct {
	svc storage.TenantStore
}

func newRunnerBindingStore(tenant storage.TenantStore) *runnerBindingStore {
	return &runnerBindingStore{svc: tenant}
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
	if err != nil {
		return "", err
	}
	return name, nil
}

// wireRunnerBindingStore attaches session→runner binding persistence to the
// global sandbox router. Called wherever the storage backend becomes available
// (server core and the web/server entrypoint) — on every backend, including PG.
func wireRunnerBindingStore(tenant storage.TenantStore) {
	if tenant == nil {
		return
	}
	router, ok := tools.GetSandbox().(*tools.SandboxRouter)
	if !ok || router == nil {
		log.Warn("runner binding store not wired: sandbox router unavailable")
		return
	}
	router.SetBindingStore(newRunnerBindingStore(tenant))
	log.Info("Session→runner bindings persist in tenants.runner_id")
}
