package tools

import (
	"database/sql"
	"sync"

	"xbot/config"
	log "xbot/logger"
)

var (
	globalSandbox       Sandbox
	globalSandboxMu     sync.RWMutex
	globalRunnerTokenDB *sql.DB
)
var sandboxInitOnce sync.Once

// InitSandbox initializes the global sandbox instance (called by the entrypoint).
//
// Sandboxing is unified behind SandboxRouter: a session bound to a runner runs
// remotely, everything else runs on the local host.
func InitSandbox(sandboxCfg config.SandboxConfig, workDir string) {
	sandboxInitOnce.Do(func() {
		reinitSandbox(sandboxCfg, workDir)
	})
}

// ReinitSandbox reinitializes the global sandbox (used when the mode changes at runtime).
func ReinitSandbox(sandboxCfg config.SandboxConfig, workDir string) {
	globalSandboxMu.Lock()
	old := globalSandbox
	globalSandbox = nil
	globalSandboxMu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	reinitSandbox(sandboxCfg, workDir)
}

func reinitSandbox(sandboxCfg config.SandboxConfig, workDir string) {
	// 沙箱统一走 SandboxRouter：remote（runner）或 none（本机直连，默认）。
	// 本地 docker sandbox 已于 2026-09-16 整体删除（用户要求：沙箱统一走 runner 接入）。
	globalSandbox = NewSandboxRouter(sandboxCfg, workDir)
	log.Infof("Sandbox initialized: %s (router)", globalSandbox.Name())
}

// GetSandbox returns the global sandbox instance.
func GetSandbox() Sandbox {
	sandboxInitOnce.Do(func() {
		// Fallback for tests that never call InitSandbox.
		log.Warn("GetSandbox called before InitSandbox, falling back to NoneSandbox")
		globalSandboxMu.Lock()
		globalSandbox = &NoneSandbox{}
		globalSandboxMu.Unlock()
	})
	globalSandboxMu.RLock()
	s := globalSandbox
	globalSandboxMu.RUnlock()
	return s
}

// SetSandbox overrides the global sandbox (tests).
func SetSandbox(s Sandbox) {
	globalSandboxMu.Lock()
	globalSandbox = s
	globalSandboxMu.Unlock()
}

// SetRunnerTokenDB wires the DB used for runner persistence plus the
// session→runner binding store. Must be called before any runner connects.
func SetRunnerTokenDB(db *sql.DB) {
	globalSandboxMu.Lock()
	defer globalSandboxMu.Unlock()
	globalRunnerTokenDB = db
	store := NewRunnerStore(db)
	switch sb := globalSandbox.(type) {
	case *SandboxRouter:
		sb.SetRunnerStore(store)
	case *RemoteSandbox:
		sb.SetRunnerStore(store)
	}
}

// GetRunnerTokenDB returns the DB connection used for runner persistence.
func GetRunnerTokenDB() *sql.DB {
	return globalRunnerTokenDB
}
