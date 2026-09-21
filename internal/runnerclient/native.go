package runnerclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"xbot/internal/cmdbuilder"
)

// maxDownloadSize 是下载操作的最大文件大小（100MB）。
const maxDownloadSize = 100 * 1024 * 1024

// httpClient 是下载操作的专用 HTTP 客户端。
var httpClient = &http.Client{Timeout: 0} // 使用 context timeout

// NativeExecutor 使用 os.* 原生 API 执行操作。
type NativeExecutor struct {
	Workspace string
}

// NewNativeExecutor 创建一个 NativeExecutor。
func NewNativeExecutor(workspace string) *NativeExecutor {
	return &NativeExecutor{Workspace: workspace}
}

func (e *NativeExecutor) Close() error { return nil }

func (e *NativeExecutor) Exec(ctx context.Context, spec ExecSpec) (*ExecResult, error) {
	cmd, err := cmdbuilder.Build(ctx, spec.Shell, spec.Command, spec.Args,
		"", spec.Env, cmdbuilder.Config{RunAsUser: spec.RunAsUser})
	if err != nil {
		return nil, err
	}

	// 创建新进程组，超时时可以 kill 所有子进程
	setProcessAttrs(cmd)
	// ⛔ 2026-09-19 用户实机 P0：**绝不因"工作目录不存在/不可写"让命令失败**。
	// 旧实现直接 `cmd.Dir = spec.Dir | e.Workspace`：目录缺失（另一台机器遗留的 CWD、
	// 或 `/workspace` 这类非 root 用户建不出来的路径）⇒ `cmd.Run()` 在 chdir 处失败，
	// 用户看到的是"shell 执行失败"，而真因是权限/目录缺失。
	// 现在按优先级解析：请求目录 → workspace → 用户 home；
	// 每一级先看是否存在，缺失则尝试创建；都不可用时**退到最近存在的祖先目录**
	// 并把替换原因作为警告返回（命令照常执行，绝不失败）。
	dir, dirWarn := e.resolveWorkDir(spec.Dir)
	if dir != "" {
		cmd.Dir = dir
	}
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if dirWarn != "" {
		// ⛔ 绝不静默替换目录：把"为什么换了工作目录"写进 stderr，让模型/用户看得见。
		fmt.Fprintf(&stderr, "[runner] %s\n", dirWarn)
	}

	start := time.Now()
	err = cmd.Run()
	_ = time.Since(start)

	exitCode, timedOut, rawErr := extractExitInfo(err, ctx.Err())
	if rawErr != nil {
		return nil, rawErr
	}
	if timedOut {
		if cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
		}
	}

	return &ExecResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		TimedOut: timedOut,
	}, nil
}

func (e *NativeExecutor) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (e *NativeExecutor) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

func (e *NativeExecutor) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{
		Name:    info.Name(),
		Size:    info.Size(),
		Mode:    info.Mode(),
		ModTime: info.ModTime(),
		IsDir:   info.IsDir(),
	}, nil
}

func (e *NativeExecutor) ReadDir(path string) ([]DirEntry, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]DirEntry, 0, len(entries))
	for _, e := range entries {
		info, ierr := e.Info()
		var size int64
		if ierr == nil {
			size = info.Size()
		}
		result = append(result, DirEntry{
			Name:  e.Name(),
			IsDir: e.IsDir(),
			Size:  size,
		})
	}
	return result, nil
}

func (e *NativeExecutor) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// resolveWorkDir 解析一个**可用**的工作目录 —— **绝不因为"目录缺失/不可写"让命令失败**。
//
// 优先级：请求目录 → executor 的 workspace → 用户 home。每一级：
//   - 已存在且是目录 ⇒ 直接用；
//   - 缺失 ⇒ 尝试 MkdirAll（0o755）；
//   - 都不可用 ⇒ 退到**最近存在的祖先目录**；
//   - 连祖先都找不到 ⇒ 返回 ""（让 exec 继承 runner 进程自身的 cwd）。
//
// 返回的 warning 非空即表示"发生了替换或降级"，调用方必须把它暴露出去（写入 stderr），
// 绝不静默 —— 用户报告（2026-09-19）：runner 总是试图创建自己没权限的目录，然后
// shell 执行失败；根因是这里没有任何降级路径。
func (e *NativeExecutor) resolveWorkDir(requested string) (string, string) {
	candidates := make([]string, 0, 3)
	cleaned := ""
	if requested != "" {
		cleaned = filepath.Clean(requested)
		candidates = append(candidates, cleaned)
	}
	if e.Workspace != "" {
		candidates = append(candidates, filepath.Clean(e.Workspace))
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates, home)
	}

	firstErr := ""
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil {
			if st.IsDir() {
				return c, ""
			}
			if firstErr == "" {
				firstErr = c + " exists but is not a directory"
			}
			continue
		}
		if err := os.MkdirAll(c, 0o755); err == nil {
			return c, "created missing work dir " + c
		} else if firstErr == "" {
			firstErr = err.Error()
		}
	}

	// 全部不可用：向上找最近存在的祖先（保证 exec 仍能跑）。
	if cleaned != "" {
		for d := filepath.Dir(cleaned); d != "" && d != "." && d != string(filepath.Separator); d = filepath.Dir(d) {
			if st, err := os.Stat(d); err == nil && st.IsDir() {
				return d, fmt.Sprintf("work dir %s unusable (%s); fell back to nearest existing ancestor %s", cleaned, firstErr, d)
			}
		}
	}
	return "", fmt.Sprintf("no usable work dir for %q (%s); running in the runner's inherited cwd", requested, firstErr)
}

func (e *NativeExecutor) Remove(path string) error {
	return os.Remove(path)
}

func (e *NativeExecutor) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (e *NativeExecutor) DownloadFile(ctx context.Context, url, outputPath string) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	dir := filepath.Dir(outputPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, fmt.Errorf("create dir: %w", err)
	}

	f, err := os.Create(outputPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	written, err := io.Copy(f, io.LimitReader(resp.Body, maxDownloadSize))
	if err != nil {
		return 0, fmt.Errorf("write file: %w", err)
	}
	if written >= maxDownloadSize {
		return 0, fmt.Errorf("file exceeds maximum size (%d bytes)", maxDownloadSize)
	}
	return written, nil
}
