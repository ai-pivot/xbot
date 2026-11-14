//go:build linux

package sqlite

import (
	"os"
	"strings"
)

// sidecarOrphanFD 判定本进程里是否还有 fd 指着 <path>-wal 的**孤儿 inode**
// （已被外部进程删掉/换掉的那个文件）。
//
// 为什么这是唯一可靠的判据：良性的「我们自己最后一个连接关闭」也会让 SQLite
// last-close unlink 掉 -wal（PERSIST_WAL 只保护池子最初那几条连接），下一次查询
// 再在新 inode 上建一个新的。这类良性事件里，进程里**没有任何 fd 指向旧 inode**；
// 而真异常（外部进程删/换 -wal）里，活连接的 fd 还紧紧抱着那个孤儿 inode，
// 之后的写入全部落在一个别人看不见的文件里。
//
// 「池里开了几条连接」不能当判据：良性场景下，下一次查询可能在两次巡检之间
// 就把连接池重新开起来了（OpenConnections>0），而旧身份照样过期。
//
// cur 是此刻 <path>-wal 的 os.Stat 结果（可能为 nil = 路径上没有文件）。
// 返回 ("", true) = 没有孤儿 fd；(fd, true) = e.g. ("11", true) 有孤儿 fd；
// ("", false) = 本平台无法判定（调用方退回保守判据）。
func sidecarOrphanFD(path string, cur os.FileInfo) (string, bool) {
	walPath := path + "-wal"
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return "", false
	}
	for _, e := range entries {
		fdPath := "/proc/self/fd/" + e.Name()
		target, readErr := os.Readlink(fdPath)
		if readErr != nil {
			continue
		}
		// 只关心指向 <path>-wal 的 fd：活着的（target == walPath）或已被 unlink 的
		// （内核在 readlink 末尾追加 " (deleted)"）。
		if target != walPath && !strings.HasPrefix(target, walPath+" (deleted)") {
			continue
		}
		if cur == nil {
			// 路径上已经没有 -wal，却还有 fd 指着它 ⇒ 孤儿。
			return e.Name(), true
		}
		fi, statErr := os.Stat(fdPath)
		if statErr != nil {
			continue
		}
		if !os.SameFile(fi, cur) {
			// fd 指着的是另一个 inode（被 rename 替换 / unlink 后重建）。
			return e.Name(), true
		}
	}
	return "", true
}
