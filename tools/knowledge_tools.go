package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// NOTE: knowledge_write and knowledge_list tools have been removed.
// Project knowledge is now managed via AGENTS.md + docs/agent/ directly
// using the standard Read/FileReplace/FileCreate tools.
// The flat memory tools (memory_write, memory_list) remain for personal memory.

// writeFileSandboxAware writes a file, auto-creating parent directories.
// Uses Sandbox API when in sandbox mode.
func writeFileSandboxAware(ctx *ToolContext, path string, data []byte) error {
	if shouldUseSandbox(ctx) {
		userID := ctx.OriginUserID
		if userID == "" {
			userID = ctx.SenderID
		}
		// Sandbox: create parent dirs then write
		dir := filepath.Dir(path)
		if err := ctx.Sandbox.MkdirAll(ctx.Ctx, dir, 0o755, userID); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
		return ctx.Sandbox.WriteFile(ctx.Ctx, path, data, 0o644, userID)
	}
	// OS: create parent dirs then write
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	return os.WriteFile(path, data, 0o644)
}
