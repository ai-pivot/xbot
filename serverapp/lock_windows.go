//go:build windows

package serverapp

import (
	"os"

	"golang.org/x/sys/windows"
)

// lockOffset 是被 LockFileEx 独占的字节区间起点（1 MiB，远在 pid 文本之后）。
//
// 为什么不是从 0 开始（2026-10-22 CI，`TestServerLockRecordsOwnerPid`）：
// Windows 的字节区间锁对**同一进程的第二个句柄**同样生效 —— MSDN：
// "If the locking process opens the file a second time, it cannot access the
// specified region through this second handle until it unlocks the region."
// 锁 [0,1) 恰好覆盖 pid 文本，于是任何想读锁文件诊断持锁者的代码
// （AcquireServerLock 自己的报错路径、owner-pid 测试、运维的 `type`/`cat`）
// 都会拿到 ERROR_LOCK_VIOLATION（"another process has locked a portion of
// the file"）。flock(2) 是 advisory 锁，读写不受影响 —— 两者语义并不等价。
//
// 把锁挪到 EOF 之后的一个固定字节：互斥语义完全一致（同一 home 只允许一个
// serve 进程，服务端自己也只取一次），而 pid 文本区始终可读。
// （Windows 允许锁定超出当前文件末尾的区间，见 LockFileEx 文档。）
const lockOffset = 1 << 20

// lockFileExclusive 以非阻塞方式取独占锁（Windows：LockFileEx）。
// 语义对齐 flock(LOCK_EX|LOCK_NB)：独占 + 拿不到立刻失败。
func lockFileExclusive(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
}

// unlockFile 释放锁（Windows）。
func unlockFile(f *os.File) error {
	ol := &windows.Overlapped{Offset: lockOffset}
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, ol)
}
