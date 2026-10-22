//go:build windows

package serverapp

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockFileExclusive 以非阻塞方式取独占锁（Windows：LockFileEx）。
// 语义对齐 flock(LOCK_EX|LOCK_NB)：独占 + 拿不到立刻失败。
func lockFileExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
}

// unlockFile 释放锁（Windows）。
func unlockFile(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
