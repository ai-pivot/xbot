package tools

import "time"

// Size, timeout, and count limits for tools and sandboxes.
// Centralised here to avoid magic numbers scattered across files.

const (
	// Sandbox file/download limits
	MaxSandboxFileSize  = 500 * 1024 * 1024 // 500MB
	MaxNoneDownloadSize = 100 * 1024 * 1024 // 100MB
	DownloadTimeout     = 5 * time.Minute

	// Background task limits
	MaxBgOutputSize   = 50 * 1024 // 50KB
	MaxBgTaskLifetime = 24 * time.Hour

	// Shell limits
	DefaultShellTimeout = 120 * time.Second
	MaxShellTimeout     = 600 * time.Second

	// Grep limits
	MaxGrepMatches    = 200
	MaxGrepFileSize   = 1 * 1024 * 1024
	MaxGrepLineLength = 500

	// Per-tool local timeouts (non-sandbox mode)
	GrepLocalTimeout = 60 * time.Second // large codebase search
	GlobLocalTimeout = 30 * time.Second // file pattern matching
	ReadLocalTimeout = 10 * time.Second // single file I/O
	EditLocalTimeout = 10 * time.Second // single file I/O

	// Per-tool file size limits (non-sandbox mode)
	MaxReadFileSize = 10 * 1024 * 1024 // 10MB
	MaxEditFileSize = 10 * 1024 * 1024 // 10MB

	// Glob result limit
	MaxGlobResults = 200

	// Directory listing limits
	MaxDirEntries        = 30
	MaxProjectFilesShown = 12

	// BgTask notification channel buffer
	BgTaskNotifyChBuffer = 64

	// Sandbox context timeout
	SandboxCtxTimeout = 30 * time.Second

	// HTTP timeouts
	FetchHTTPTimeout    = 30 * time.Second // fetch.go HTTP client
	DownloadHTTPTimeout = 60 * time.Second // download.go file download
	TokenHTTPTimeout    = 30 * time.Second // download.go token request

	// RPC / communication timeouts
	AgentRPCTimeout      = 30 * time.Second // send_message.go agent RPC
	MCPConnectTimeout    = 30 * time.Second // mcp_common.go MCP connection
	LoginShellEnvTimeout = 10 * time.Second // mcp_common.go shell env detection

	// Remote sandbox timeouts
	RemoteSandboxExecTimeout = 60 * time.Second // remote_sandbox_exec.go default exec
	RemoteSandboxSyncTimeout = 60 * time.Second // remote_sandbox.go sync operation

	// Docker command timeouts
	DockerCmdTimeout  = 30 * time.Second  // normal docker commands
	DockerSlowTimeout = 120 * time.Second // slow docker operations (export/import)
)
