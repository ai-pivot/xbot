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
	// DefaultShellTimeout is the default foreground wait before a running shell is
	// AUTO-PROMOTED to a background task (1 min). The process is NOT killed — it
	// keeps running and reports completion as a notification, so a short default
	// keeps the agent loop responsive (the model should never block on a long
	// command; pass an explicit timeout for genuinely long runs).
	DefaultShellTimeout = 60 * time.Second
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
	AgentRPCTimeout = 30 * time.Second // send_message.go agent RPC
	// SendMessageAwaitReply 是 send_message 对 agent 目标"顺手拿回复"的等待窗口：超过它
	// 立刻返回"已投递"，投递在后台继续 —— 工具绝不能被目标 agent 的忙碌/卡住拖死
	//（用户 2026-09-14：「sendmessage 工具有可能卡死，必须立刻成功」）。
	SendMessageAwaitReply = 2 * time.Second
	MCPConnectTimeout     = 30 * time.Second // mcp_common.go MCP connection
	LoginShellEnvTimeout  = 10 * time.Second // mcp_common.go shell env detection

	// Remote sandbox timeouts
	RemoteSandboxExecTimeout = 60 * time.Second // remote_sandbox_exec.go default exec
	RemoteSandboxSyncTimeout = 60 * time.Second // remote_sandbox.go sync operation

)
