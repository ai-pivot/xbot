//go:build !windows

package sqlite

import (
	"os"
	"syscall"
)

// flockExclusive 以非阻塞方式尝试取独占锁（Unix：flock(2)）。
//
// 平台隔离原因（2026-10-22 CI 失败）：`syscall.Flock` / `LOCK_EX` / `LOCK_NB` /
// `LOCK_UN` 是 Unix-only，直接写在跨平台文件里会导致 `GOOS=windows go build` 编译
// 失败（CI 的 Build (Windows) job）。这里把"如何取锁"抽象成一个函数，各平台各实现。
func flockExclusive(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// funlock 释放锁（Unix）。
func funlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
