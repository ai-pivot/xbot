package agent

import (
	"os"

	log "xbot/logger"
)

// cwdStore 是本守卫真正需要的 TenantSession 面 —— 只依赖 Get/Set，
// 便于在单测里用假实现，不必拉一整个数据库会话起来。
type cwdStore interface {
	GetCurrentDir() string
	SetCurrentDir(dir string)
}

// resetStaleCWD 返回本轮应当使用的会话工作目录；当持久化的 CWD 指向一个
// **已经不存在**（或被删成非目录）的路径时，复位到 workspaceRoot 并返回它。
//
// 为什么必须有这道守卫（与任何具体工具无关）：
//   - 会话 CWD 持久化在 tenants.cwd，跨重启保留；
//   - 该目录可能被外部抹掉：开发机迁移、临时目录回收、工作树/副本清理……
//   - CWD 缺失后每一次 Shell 都会失败（`working directory "..." is not
//     accessible`，issue #457）；local none sandbox 刻意**不**静默退到 home
//     —— 在错误目录里跑 rm/git 比明确失败更危险 —— 所以 agent 连 pwd/ls 都跑不了，
//     只能靠模型自己想起来 `Cd`。
//
// 复位点在 turn 开始前（buildPrompt），比工具层兜底更早，且只针对"目录真的没了"
// 这一种确定失效，不做任何"看起来可疑"的猜测性复位。
func resetStaleCWD(s cwdStore, sessionKey, workspaceRoot string) string {
	cwd := s.GetCurrentDir()
	if cwd == "" || dirExists(cwd) {
		return cwd
	}
	log.WithFields(log.Fields{
		"session":        sessionKey,
		"cwd":            cwd,
		"workspace_root": workspaceRoot,
	}).Warn("Persisted session CWD is inaccessible, resetting to workspace root")
	s.SetCurrentDir(workspaceRoot)
	return workspaceRoot
}

// cwdIsHostPath reports whether this session's CWD lives on **this host's**
// filesystem. docker / remote / offline 沙箱会话的 CWD 是沙箱内路径，宿主
// os.Stat 看不到 —— 对这些会话套用宿主判据会把**正常**的 CWD 误判成失效并复位，
// 所以守卫只在宿主可见路径上生效。
func (a *Agent) cwdIsHostPath(sessionKey string) bool {
	if a.sandbox == nil {
		return true
	}
	switch a.sandboxNameForSession(sessionKey) {
	case "", "none":
		return true
	default:
		return false
	}
}

// dirExists reports whether path exists and is a directory.
func dirExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
