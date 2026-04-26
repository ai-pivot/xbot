package tools

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func defaultWorkspaceRoot(ctx *ToolContext) string {
	if ctx == nil {
		return ""
	}
	// Remote sandbox: the runner handles its own path enforcement.
	// The server doesn't have the runner's filesystem, so skip checks.
	if ctx.Sandbox != nil && ctx.Sandbox.Name() == "remote" {
		return ""
	}
	if ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
		return ctx.Sandbox.Workspace(ctx.OriginUserID)
	}
	if ctx.WorkspaceRoot != "" {
		return ctx.WorkspaceRoot
	}
	return ctx.WorkingDir
}

func resolveScopedBase(ctx *ToolContext) (string, error) {
	root := defaultWorkspaceRoot(ctx)
	if root == "" {
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		root = cwd
	}
	absRoot, err := cleanAbsPath(root)
	if err != nil {
		return "", fmt.Errorf("invalid workspace root: %w", err)
	}
	return absRoot, nil
}

// effectiveCWD returns the current working directory for path resolution.
// Priority: ctx.CurrentDir (set by Cd tool) > effectiveCWD(ctx) (process CWD).
//
// Bug fix: In unrestricted mode (none/remote sandbox), all path resolution
// functions used os.Getwd() which returns the OS process CWD, not the
// virtual CWD set by the Cd tool. The Cd tool only sets ctx.CurrentDir
// (session-persisted), it does NOT call os.Chdir(). This caused relative
// paths in Read/FileCreate/FileReplace/Grep/Glob to resolve against the
// wrong directory after a Cd.
func effectiveCWD(ctx *ToolContext) (string, error) {
	if ctx != nil && ctx.CurrentDir != "" {
		return ctx.CurrentDir, nil
	}
	return os.Getwd()
}

// isUnrestricted returns true when path restrictions should be skipped.
// None sandbox: user has full filesystem access.
// Remote sandbox: the runner handles its own path enforcement.
func isUnrestricted(ctx *ToolContext) bool {
	if ctx == nil || ctx.Sandbox == nil {
		return false
	}
	name := ctx.Sandbox.Name()
	return name == "none" || name == "remote"
}

// ResolveWritePath resolves inputPath to an absolute path and validates it is within the workspace write scope.
//
// Relative path resolution priority: CurrentDir (set by Cd) > WorkspaceRoot/WorkingDir.
// Absolute paths are validated directly, unaffected by CurrentDir.
func ResolveWritePath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}

	// Unrestricted mode (none/remote sandbox): skip path checks.
	if isUnrestricted(ctx) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0 && !ctx.SandboxEnabled) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	root, err := resolveScopedBase(ctx)
	if err != nil {
		return "", err
	}

	candidate := inputPath
	if !filepath.IsAbs(candidate) {
		// prefer CurrentDir (set by Cd), otherwise fall back to root
		if ctx != nil && ctx.CurrentDir != "" {
			candidate = filepath.Join(ctx.CurrentDir, candidate)
		} else {
			candidate = filepath.Join(root, candidate)
		}
	}
	candidate, err = cleanAbsPath(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	// Check target or parent directory (handles symlinks)
	checkPath := candidate
	if _, err := os.Stat(candidate); err != nil {
		checkPath = filepath.Dir(candidate)
	}
	realCheckPath, err := filepath.EvalSymlinks(checkPath)
	if err == nil {
		checkPath = realCheckPath
	}
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}

	if !isWithinRoot(checkPath, realRoot) {
		// Fallback: compare without EvalSymlinks (handles Windows short paths
		// where intermediate directories don't exist yet)
		if isWithinRoot(candidate, root) {
			return candidate, nil
		}
		return "", fmt.Errorf("write path escapes workspace: %s", inputPath)
	}
	return candidate, nil
}

// ResolveReadPath resolves inputPath to an absolute path and validates it is within the allowed read scope.
//
// Relative path resolution priority: CurrentDir (set by Cd) > WorkspaceRoot/WorkingDir.
// Absolute paths are validated directly, unaffected by CurrentDir.
// allowed read range includes workspace root and directories listed in ReadOnlyRoots.
func ResolveReadPath(ctx *ToolContext, inputPath string) (string, error) {
	if inputPath == "" {
		return "", fmt.Errorf("path is required")
	}

	// Unrestricted mode (none/remote sandbox): skip path checks.
	if isUnrestricted(ctx) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	if ctx == nil || (ctx.WorkspaceRoot == "" && ctx.WorkingDir == "" && len(ctx.ReadOnlyRoots) == 0 && !ctx.SandboxEnabled) {
		if filepath.IsAbs(inputPath) {
			return cleanAbsPath(inputPath)
		}
		cwd, err := effectiveCWD(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get working directory: %w", err)
		}
		return cleanAbsPath(filepath.Join(cwd, inputPath))
	}

	root, err := resolveScopedBase(ctx)
	if err != nil {
		return "", err
	}

	candidate := inputPath
	if !filepath.IsAbs(candidate) {
		// prefer CurrentDir (set by Cd), otherwise fall back to root
		if ctx != nil && ctx.CurrentDir != "" {
			candidate = filepath.Join(ctx.CurrentDir, candidate)
		} else {
			candidate = filepath.Join(root, candidate)
		}
	}
	candidate, err = cleanAbsPath(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}

	realCandidate, err := filepath.EvalSymlinks(candidate)
	if err == nil {
		candidate = realCandidate
	}

	allowedRoots := []string{root}
	allowedRoots = append(allowedRoots, ctx.ReadOnlyRoots...)

	for _, allowed := range allowedRoots {
		if allowed == "" {
			continue
		}
		absAllowed, err := cleanAbsPath(allowed)
		if err != nil {
			continue
		}
		realAllowed, err := filepath.EvalSymlinks(absAllowed)
		if err == nil {
			absAllowed = realAllowed
		}
		if isWithinRoot(candidate, absAllowed) {
			return candidate, nil
		}
		// Fallback: compare without EvalSymlinks (handles Windows short/long
		// path name mismatches when file doesn't exist yet)
		if isWithinRoot(candidate, allowed) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("read path is outside allowed roots: %s", inputPath)
}

// sandboxBaseDir returns the sandbox working directory prefix.
// Returns Sandbox.Workspace(userID) (typically "/workspace" in docker mode, runner workspace in remote mode).
// Returns empty string to indicate no sandbox path constraint (none mode); callers should skip path validation.
func sandboxBaseDir(ctx *ToolContext) string {
	if ctx != nil && ctx.Sandbox != nil && ctx.Sandbox.Name() != "none" {
		return ctx.Sandbox.Workspace(ctx.OriginUserID)
	}
	return ""
}

// ShouldUseSandbox determines whether the Sandbox should be used for filesystem access.
// returns true only when Sandbox is available and not in none mode.
func ShouldUseSandbox(ctx *ToolContext) bool {
	return ctx != nil && ctx.Sandbox != nil && ctx.Sandbox.Name() != "none"
}

// shouldUseSandbox is the unexported alias used within the tools package.
func shouldUseSandbox(ctx *ToolContext) bool {
	return ShouldUseSandbox(ctx)
}

// resolveSandboxCWD resolves CurrentDir to an absolute path inside the sandbox.
// supports two formats:
//   - sandbox path (e.g. /workspace/src) → return directly
//   - host path (e.g. /data/users/ou_xxx/workspace/src) → convert to sandbox path
//
// Returns empty string to indicate unable to resolve (CurrentDir is empty or not under a known root directory).
func resolveSandboxCWD(ctx *ToolContext, sandboxBase string) string {
	if ctx == nil || ctx.CurrentDir == "" {
		return ""
	}
	// Normalize separators for cross-platform comparison
	currentDir := filepath.ToSlash(ctx.CurrentDir)
	sandboxBaseSlash := filepath.ToSlash(sandboxBase)
	if currentDir == sandboxBaseSlash || strings.HasPrefix(currentDir, sandboxBaseSlash+"/") {
		return ctx.CurrentDir
	}
	if ctx.WorkspaceRoot != "" {
		wsRoot := filepath.ToSlash(ctx.WorkspaceRoot)
		if strings.HasPrefix(currentDir, wsRoot) {
			rel, err := filepath.Rel(ctx.WorkspaceRoot, ctx.CurrentDir)
			if err == nil {
				rel = filepath.ToSlash(rel)
				if rel == "." {
					return sandboxBase
				}
				return path.Join(sandboxBase, rel)
			}
		}
	}
	return ""
}

// shellEscape performs shell single-quote escaping to prevent command injection.
// Replace single quotes in the string with '\'\” (end single quote, escaped single quote, start new single quote).
func shellEscape(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
