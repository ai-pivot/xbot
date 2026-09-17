package tools

import (
	"database/sql"
	"sync"

	"xbot/config"
	log "xbot/logger"
)

var (
	globalSandbox       Sandbox
	globalSandboxMu     sync.RWMutex // 保护 globalSandbox 的并发读写
	globalRunnerTokenDB *sql.DB
)
var sandboxInitOnce sync.Once

// InitSandbox 初始化全局沙箱实例（由 main.go 在启动时调用）。
//
// 沙箱统一走 SandboxRouter：RemoteMode 非空时用 runner（remote），否则本机（none）。
func InitSandbox(sandboxCfg config.SandboxConfig, workDir string) {
	sandboxInitOnce.Do(func() {
		reinitSandbox(sandboxCfg, workDir)
	})
}

// ReinitSandbox reinitializes the global sandbox (used when sandbox_mode changes at runtime).
func ReinitSandbox(sandboxCfg config.SandboxConfig, workDir string) {
	// Close old sandbox if possible
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

// GetSandbox 获取全局沙箱实例
func GetSandbox() Sandbox {
	sandboxInitOnce.Do(func() {
		// Fallback: 如果 InitSandbox 未被调用（例如测试场景），使用 NoneSandbox
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

// SetSandbox 设置全局沙箱实例（用于测试）
func SetSandbox(s Sandbox) {
	globalSandboxMu.Lock()
	globalSandbox = s
	globalSandboxMu.Unlock()
}

// SetRunnerTokenDB sets the DB connection used for per-user runner token persistence.
// Must be called before any runner connections are authenticated.
func SetRunnerTokenDB(db *sql.DB) {
	globalSandboxMu.Lock()
	defer globalSandboxMu.Unlock()
	globalRunnerTokenDB = db
	store := NewRunnerTokenStore(db)
	switch sb := globalSandbox.(type) {
	case *SandboxRouter:
		sb.SetTokenStore(store)
		if sb.remote != nil {
			sb.remote.SetTokenStore(store)
		}
	case *RemoteSandbox:
		sb.SetTokenStore(store)
	}
}

// GetRunnerTokenDB returns the DB connection for runner tokens.
func GetRunnerTokenDB() *sql.DB {
	return globalRunnerTokenDB
}
