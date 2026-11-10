package sqlite

import (
	"runtime"
	"testing"
)

// requirePOSIXUnlink 跳过那些靠「外部进程删掉本进程仍打开着的 -wal/-shm」
// 注入故障的用例。
//
// 两个平台事实使它们在 Windows 上无法成立：
//  1. Windows 拒绝删除被进程打开的文件（SQLite 的 win32 VFS 打开 -wal 时不带
//     FILE_SHARE_DELETE），所以 os.Remove 直接返回 sharing violation —— 故障
//     根本注入不进去，被测的故障模式在该平台也不存在；
//  2. 这些用例的核心判据 deletedSidecarFDs 读 /proc/self/fd，只有 Linux 有实现
//     （见 walguard_fd_other.go 的非 linux 桩）。
//
// 生产跑 Linux，那里这些用例照常运行，覆盖不缩水。
func requirePOSIXUnlink(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("requires POSIX unlink semantics: Windows denies deleting a -wal held open by SQLite")
	}
}
