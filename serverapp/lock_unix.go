//go:build !windows

package serverapp

import (
	"os"
	"syscall"
)

// lockFileExclusive 以非阻塞方式取独占锁（Unix：flock(2)）。
//
// 平台隔离（2026-10-22 CI）：`syscall.Flock`/`LOCK_EX`/`LOCK_NB`/`LOCK_UN` 是
// Unix-only，写在跨平台文件里会让 `GOOS=windows go build` 编译失败。
func lockFileExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlockFile 释放锁（Unix）。
func unlockFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
