//go:build !linux

package sqlite

import "os"

// sidecarOrphanFD 在非 linux 平台无法枚举本进程的 fd，返回 ok=false 让调用方
// 退回保守判据（见 walIdentityAnomaly：池里一条连接都没开着才算良性）。
//
// 生产跑在 linux（判据 /proc/<pid>/fd 也是在 linux 上数），这条分支只影响开发机。
func sidecarOrphanFD(path string, cur os.FileInfo) (string, bool) {
	_ = path
	_ = cur
	return "", false
}
