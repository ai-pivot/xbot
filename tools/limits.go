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

	// Directory listing limits
	MaxDirEntries        = 30
	MaxProjectFilesShown = 12

	// BgTask notification channel buffer
	BgTaskNotifyChBuffer = 64

	// Sandbox context timeout
	SandboxCtxTimeout = 30 * time.Second
)
