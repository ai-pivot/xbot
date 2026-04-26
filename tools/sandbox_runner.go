package tools

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"xbot/config"
	log "xbot/logger"
)

const (
	dockerCmdTimeout  = 30 * time.Second  // 普通 docker 命令超时
	dockerSlowTimeout = 120 * time.Second // 慢操作（export/import）超时
)

// dockerExec runs a docker command with a timeout (0 = no timeout), returning combined output.
func dockerExec(timeout time.Duration, args ...string) ([]byte, error) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).CombinedOutput()
}

// dockerRun runs a docker command with a timeout (0 = no timeout), returning only error.
func dockerRun(timeout time.Duration, args ...string) error {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	defer cancel()
	return exec.CommandContext(ctx, "docker", args...).Run()
}

// dockerPipelineExportImport pipes docker export stdout into docker import stdin,
// avoiding a large intermediate tar file on disk. Falls back to temp-file approach on error.
func dockerPipelineExportImport(ctx context.Context, containerName string, importArgs []string) ([]byte, error) {
	exportCmd := exec.CommandContext(ctx, "docker", "export", containerName)
	importCmd := exec.CommandContext(ctx, "docker", importArgs...)

	pipe, err := exportCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	importCmd.Stdin = pipe
	importCmd.Stderr = nil // will be captured via CombinedOutput on importCmd

	var importOut bytes.Buffer
	importCmd.Stdout = &importOut
	importCmd.Stderr = &importOut

	if err := exportCmd.Start(); err != nil {
		return nil, fmt.Errorf("start export: %w", err)
	}
	if err := importCmd.Start(); err != nil {
		exportCmd.Process.Kill()
		exportCmd.Wait()
		return nil, fmt.Errorf("start import: %w", err)
	}

	exportErr := exportCmd.Wait()
	importErr := importCmd.Wait()

	out := importOut.Bytes()
	if exportErr != nil {
		return out, fmt.Errorf("export: %w", exportErr)
	}
	if importErr != nil {
		return out, fmt.Errorf("import: %w", importErr)
	}
	return out, nil
}

// global sandbox instance
var (
	globalSandbox       Sandbox
	globalSandboxMu     sync.RWMutex // 保护 globalSandbox 的并发读写
	globalRunnerTokenDB *sql.DB
)
var sandboxInitOnce sync.Once

// InitSandbox 初始化global sandbox instance（由 main.go 在启动时调用）。
// 启动时自动清理上次残留的临时文件和悬空 Docker 资源。
//
// When RemoteMode is set (non-empty), both docker and remote sandbox instances
// are created and wrapped in a SandboxRouter for per-user routing.
// Otherwise, falls back to the legacy single-sandbox behavior.
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
	if sandboxCfg.RemoteMode != "" {
		// Dual-mode: create SandboxRouter with both docker and remote
		globalSandbox = NewSandboxRouter(sandboxCfg, workDir)
		log.Infof("Sandbox initialized: %s (router)", globalSandbox.Name())
	} else {
		// Legacy single-mode
		if sandboxCfg.Mode == "docker" {
			cleanupStaleTmpFiles()
			pruneDockerResources()
		}
		globalSandbox = NewSandbox(sandboxCfg, workDir, nil)
		log.Infof("Sandbox initialized: %s", globalSandbox.Name())
	}
}

// GetSandbox returns the global sandbox instance
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

// SetSandbox sets the global sandbox instance (for testing)
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

// cleanupStaleTmpFiles clean leftover export temp files from previous abnormal exit.
// 进程被 OOM kill 或系统重启时，defer os.Remove 不会执行，tar 文件会残留在 /tmp。
func cleanupStaleTmpFiles() {
	matches, err := filepath.Glob(filepath.Join(os.TempDir(), "xbot-export-*.tar"))
	if err != nil {
		return
	}
	for _, f := range matches {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		// only clean files older than 10 minutes (avoid deleting in-use files)
		if time.Since(info.ModTime()) > 10*time.Minute {
			if err := os.Remove(f); err == nil {
				log.Infof("Cleaned up stale tmp file: %s (%.1f MB)", f, float64(info.Size())/(1024*1024))
			}
		}
	}
}

// pruneDockerResources 清理悬空 Docker 镜像。
// run once at startup to prevent dangling images from last abnormal exit consuming disk.
// 注意：不清理已停止的容器，容器生命周期由使用者控制。
func pruneDockerResources() {
	// 清理悬空镜像（<none>:<none>），这些是异常退出时未被 rmi 的旧镜像
	if out, err := dockerExec(dockerCmdTimeout, "image", "prune", "-f"); err == nil {
		log.Debugf("Docker image prune: %s", strings.TrimSpace(string(out)))
	}
	// second cleanup: ensure all dangling images are deleted
	// docker image prune may miss images referenced by containers; run builder prune again
	dockerRun(dockerCmdTimeout, "image", "prune", "-f", "--filter", "until=168h")
}

// parseJSONStringArray parses a JSON string array like ["foo","bar"] into a Go slice.
func parseJSONStringArray(s string) []string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return nil
	}
	s = s[1 : len(s)-1]
	if s == "" {
		return nil
	}
	var result []string
	for _, item := range strings.Split(s, ",") {
		item = strings.TrimSpace(item)
		if len(item) >= 2 && item[0] == '"' && item[len(item)-1] == '"' {
			result = append(result, item[1:len(item)-1])
		}
	}
	return result
}

// userImageName returns the user-specific image name
func userImageName(userID string) string {
	return fmt.Sprintf("xbot-%s:latest", userID)
}

// validUserIDRe validates userID format for Docker container/image naming.
// Only allows lowercase alphanumeric, underscores, hyphens, and dots —
// the safe subset of Docker's [a-zA-Z0-9][a-zA-Z0-9_.-]+ naming rules.
var validUserIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,127}$`)

// validateUserID checks that userID contains only characters safe for Docker
// container and image names. Returns an error if the userID is invalid.
func validateUserID(userID string) error {
	if userID == "" {
		return fmt.Errorf("userID must not be empty")
	}
	if !validUserIDRe.MatchString(userID) {
		return fmt.Errorf("invalid userID %q: must match ^[a-z0-9][a-z0-9_.-]{0,127}$ (Docker-safe characters only)", userID)
	}
	return nil
}
