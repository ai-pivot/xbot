//go:build windows

package sqlite

import (
	"os"

	"golang.org/x/sys/windows"
)

// flockExclusive 以非阻塞方式尝试取独占锁（Windows：LockFileEx）。
//
// 语义对齐 Unix 的 flock(LOCK_EX|LOCK_NB)：
//   - LOCKFILE_EXCLUSIVE_LOCK：独占
//   - LOCKFILE_FAIL_IMMEDIATELY：拿不到立刻失败（不阻塞）
//
// 锁的是文件的首字节（nNumberOfBytesToLockLow=1），与 Unix 侧"锁整个 fd"的
// 有效范围一致——两边都只是用来做"同一时刻只有一个进程能打开这个库"的互斥。
func flockExclusive(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
}

// funlock 释放锁（Windows）。
func funlock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
